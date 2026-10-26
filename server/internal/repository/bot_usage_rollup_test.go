package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 按天汇总仓库层测试（INT-164）：指纹判定（迟到写入判脏）、整日替换与覆盖
// 标记的原子写、部分唯一索引对 NULL task 的去重口径。

func usageRollupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := EnsureBotSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db
}

func seedRollupRun(t *testing.T, db *gorm.DB, botID string, at time.Time) *model.BotRun {
	t.Helper()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "rollup-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: at, StartedAt: &at, FinishedAt: &at,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

func seedRollupRequest(t *testing.T, db *gorm.DB, runID string, ordinal int64, cost *float64) {
	t.Helper()
	now := time.Now().UTC()
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: runID, Ordinal: ordinal,
		Status: model.BotModelRequestSucceeded, StartedAt: now, FinishedAt: &now,
		PrefixHash: "p", ToolsHash: "t", RequestHash: "rollup-" + uuid.NewString(),
		TokenEstimator: "chars/4",
	}
	if cost != nil {
		req.CostEstimate = cost
		prompt := int64(10)
		req.PromptTokens = &prompt
	}
	if err := db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
}

func seedRollupMaintenance(t *testing.T, db *gorm.DB, botID string, parentRunID *string, at time.Time, cost *float64) *model.BotMaintenanceRun {
	t.Helper()
	run := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "rollup-maint-" + uuid.NewString(),
		ParentRunID: parentRunID, Status: model.BotMaintenanceSucceeded,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: at, StartedAt: &at, FinishedAt: &at,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("seed maintenance: %v", err)
	}
	if cost != nil {
		req := &model.BotMaintenanceRequest{
			ID: uuid.NewString(), MaintenanceRunID: run.ID, Ordinal: 1,
			Purpose: model.BotPurposeSummary, Model: "fake-model",
			Status: model.BotModelRequestSucceeded, CostEstimate: cost,
			UsageComplete: true, PromptTokens: ptrInt64(5), TotalTokens: ptrInt64(5),
			PrefixHash: "p", ToolsetVersion: 1, StartedAt: at, FinishedAt: &at,
		}
		if err := db.Create(req).Error; err != nil {
			t.Fatalf("seed maintenance request: %v", err)
		}
	}
	return run
}

func ptrInt64(v int64) *int64 { return &v }

// 指纹：计数（run/请求/维护/维护请求）+ 最近写入；维护同时计入创建日与父
// run 日。迟到写入改变计数即判脏。
func TestUsageRollupFingerprint(t *testing.T) {
	db := usageRollupTestDB(t)
	repo := NewBotUsageRollupRepository(db)

	day1 := time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	spans := []UsageDaySpan{
		{Date: day1.Format("2006-01-02"), Start: day1, End: day2},
		{Date: day2.Format("2006-01-02"), Start: day2, End: day3},
	}

	run1 := seedRollupRun(t, db, "bot-1", day1.Add(10*time.Hour))
	seedRollupRequest(t, db, run1.ID, 1, nil)
	// 维护：day2 创建、父 run 在 day1——两天的指纹都计入这条维护。
	maint := seedRollupMaintenance(t, db, "bot-1", &run1.ID, day2.Add(3*time.Hour), ptrFloat(0.2))
	// 无父维护（day2）。
	seedRollupMaintenance(t, db, "bot-1", nil, day2.Add(4*time.Hour), nil)

	fps, err := repo.DayFingerprints("bot-1", spans)
	if err != nil {
		t.Fatalf("DayFingerprints: %v", err)
	}
	fp1, fp2 := fps[day1.Format("2006-01-02")], fps[day2.Format("2006-01-02")]
	if fp1.RunCount != 1 || fp1.RequestCount != 1 || fp1.MaintRunCount != 1 || fp1.MaintRequestCount != 1 {
		t.Fatalf("day1 指纹不符: %+v", fp1)
	}
	if fp2.RunCount != 0 || fp2.MaintRunCount != 2 || fp2.MaintRequestCount != 1 {
		t.Fatalf("day2 指纹不符: %+v", fp2)
	}
	if fp1.LastWriteNs == 0 || fp2.LastWriteNs == 0 {
		t.Fatalf("指纹应带最近写入时刻: %+v / %+v", fp1, fp2)
	}

	// 写入标记并验证 Equal；迟到请求 INSERT 后指纹不符。
	mark := &model.BotUsageRollupDay{
		UserID: "u1", BotID: "bot-1", Date: day1.Format("2006-01-02"),
		Timezone: "UTC", RolledAt: time.Now(),
		RunCount: fp1.RunCount, MaintRunCount: fp1.MaintRunCount,
		RequestCount: fp1.RequestCount, MaintRequestCount: fp1.MaintRequestCount,
		LastWriteNs: fp1.LastWriteNs,
	}
	if !fp1.Equal(*mark) {
		t.Fatalf("同口径指纹应相等")
	}
	seedRollupRequest(t, db, run1.ID, 2, ptrFloat(0.1))
	fps, err = repo.DayFingerprints("bot-1", spans)
	if err != nil {
		t.Fatalf("DayFingerprints: %v", err)
	}
	if fps[day1.Format("2006-01-02")].Equal(*mark) {
		t.Fatalf("迟到写入后 day1 指纹应判脏")
	}
	if !fps[day2.Format("2006-01-02")].Equal(model.BotUsageRollupDay{
		RunCount: fp2.RunCount, MaintRunCount: fp2.MaintRunCount,
		RequestCount: fp2.RequestCount, MaintRequestCount: fp2.MaintRequestCount,
		LastWriteNs: fp2.LastWriteNs,
	}) {
		t.Fatalf("day2 不受 day1 迟到写入影响")
	}
	// 收尾更新（finished_at 推进）同样判脏。
	later := time.Now().UTC().Add(time.Hour)
	if err := db.Model(&model.BotMaintenanceRun{}).Where("id = ?", maint.ID).
		Update("finished_at", later).Error; err != nil {
		t.Fatalf("update maintenance: %v", err)
	}
	fps, err = repo.DayFingerprints("bot-1", spans)
	if err != nil {
		t.Fatalf("DayFingerprints: %v", err)
	}
	if fps[day2.Format("2006-01-02")].LastWriteNs <= fp2.LastWriteNs {
		t.Fatalf("收尾更新应推进 last_write")
	}
}

// 整日替换 + 覆盖标记同一事务；重复替换幂等；清空按 bot 维度。
func TestUsageRollupReplaceAndWipe(t *testing.T) {
	db := usageRollupTestDB(t)
	repo := NewBotUsageRollupRepository(db)

	mkRow := func(cost float64) model.BotUsageDaily {
		return model.BotUsageDaily{
			ID: uuid.NewString(), UserID: "u1", BotID: "bot-1", BotName: "旧名",
			Date: "2026-02-08", Kind: model.BotUsageDayMain,
			Requests: 1, Runs: 1, DisplayCost: ptrFloat(cost), DisplayCosts: "[]",
			RolledAt: time.Now(),
		}
	}
	mkMark := func() *model.BotUsageRollupDay {
		return &model.BotUsageRollupDay{
			UserID: "u1", BotID: "bot-1", Date: "2026-02-08",
			Timezone: "UTC", RolledAt: time.Now(), RunCount: 1,
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return repo.ReplaceDayTx(tx, []model.BotUsageDaily{mkRow(0.5)}, mkMark())
	}); err != nil {
		t.Fatalf("replace day: %v", err)
	}
	// 同日第二次替换：内容更新（幂等语义 = 新计算结果覆盖旧行）。
	if err := db.Transaction(func(tx *gorm.DB) error {
		return repo.ReplaceDayTx(tx, []model.BotUsageDaily{mkRow(0.9)}, mkMark())
	}); err != nil {
		t.Fatalf("replace day 2nd: %v", err)
	}
	rows, err := repo.ListDailyRows("u1", []string{"2026-02-08"})
	if err != nil {
		t.Fatalf("list daily: %v", err)
	}
	if len(rows) != 1 || rows[0].DisplayCost == nil || *rows[0].DisplayCost != 0.9 {
		t.Fatalf("替换后应只剩新行: %+v", rows)
	}
	marks, err := repo.MarksByBot("bot-1")
	if err != nil || len(marks) != 1 {
		t.Fatalf("marks: %v %+v", err, marks)
	}
	if got, err := repo.MarksForBotsDates([]string{"bot-1"}, []string{"2026-02-08"}); err != nil || len(got) != 1 {
		t.Fatalf("marks for dates: %v %+v", err, got)
	}

	// StampBotNameTx 更新名称快照。
	if err := db.Transaction(func(tx *gorm.DB) error {
		return repo.StampBotNameTx(tx, "bot-1", "新名")
	}); err != nil {
		t.Fatalf("stamp name: %v", err)
	}
	rows, _ = repo.ListDailyRows("u1", []string{"2026-02-08"})
	if rows[0].BotName != "新名" {
		t.Fatalf("名称快照应更新为 新名，实得 %q", rows[0].BotName)
	}

	// WipeBotTx 清空该 bot 全部汇总与标记。
	if err := db.Transaction(func(tx *gorm.DB) error {
		return repo.WipeBotTx(tx, "bot-1")
	}); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	rows, _ = repo.ListDailyRows("u1", []string{"2026-02-08"})
	marks, _ = repo.MarksByBot("bot-1")
	if len(rows) != 0 || len(marks) != 0 {
		t.Fatalf("清空后不应剩余: rows=%d marks=%d", len(rows), len(marks))
	}
}

// 部分唯一索引：task_id 为空时 (bot,date,kind) 唯一；非空时四列全键唯一；
// 空与非空互不占位。
func TestUsageDailyPartialUniqueIndex(t *testing.T) {
	db := usageRollupTestDB(t)

	mk := func(taskID *string) *model.BotUsageDaily {
		return &model.BotUsageDaily{
			ID: uuid.NewString(), UserID: "u1", BotID: "b1", BotName: "b",
			ScheduleTaskID: taskID, Date: "2026-02-08", Kind: model.BotUsageDaySchedule,
			DisplayCosts: "[]", RolledAt: time.Now(),
		}
	}
	if err := db.Create(mk(nil)).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := db.Create(mk(nil)).Error; err == nil {
		t.Fatalf("同 (bot,date,kind,NULL) 二次插入应撞部分唯一索引")
	}
	t1 := "task-1"
	if err := db.Create(mk(&t1)).Error; err != nil {
		t.Fatalf("task 行与 NULL 行互不占位: %v", err)
	}
	if err := db.Create(mk(&t1)).Error; err == nil {
		t.Fatalf("同 (bot,date,kind,task) 二次插入应撞部分唯一索引")
	}
	t2 := "task-2"
	if err := db.Create(mk(&t2)).Error; err != nil {
		t.Fatalf("不同 task 应可共存: %v", err)
	}
}

func ptrFloat(v float64) *float64 { return &v }
