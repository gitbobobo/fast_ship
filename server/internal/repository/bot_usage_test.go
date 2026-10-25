package repository

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 花费页（INT-162）的聚合索引：迁移后必须存在，重复启动（幂等 DDL）不报错。
func TestEnsureBotSchemaUsageIndexes(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureBotSchema(db); err != nil {
			t.Fatalf("EnsureBotSchema 第 %d 次执行不应失败: %v", i+1, err)
		}
	}
	for _, name := range []string{
		"idx_bot_runs_bot_kind_created",
		"idx_bot_maintenance_runs_bot_created",
		"idx_bot_maintenance_runs_parent",
	} {
		var n int64
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).
			Scan(&n).Error; err != nil {
			t.Fatalf("query sqlite_master: %v", err)
		}
		if n != 1 {
			t.Fatalf("索引 %s 应在迁移后存在", name)
		}
	}
}

// ChunkIDs 按 size 切批：整除、余批、空集合与 size<1 兜底。
func TestChunkIDs(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e"}
	got := ChunkIDs(ids, 2)
	if len(got) != 3 || fmt.Sprint(got[0]) != "[a b]" || fmt.Sprint(got[1]) != "[c d]" || fmt.Sprint(got[2]) != "[e]" {
		t.Fatalf("切批结果不符: %v", got)
	}
	if got := ChunkIDs(ids, 5); len(got) != 1 || len(got[0]) != 5 {
		t.Fatalf("整除应只有一批: %v", got)
	}
	if got := ChunkIDs(nil, 500); len(got) != 0 {
		t.Fatalf("空集合应无批次: %v", got)
	}
	if got := ChunkIDs([]string{"a"}, 0); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("size<1 应按 1 兜底: %v", got)
	}
}

// 分批路径（F3）：批大小缩到 2，跨批的 IN 查询结果与单批一致——
// MaintenanceCostsByParentRun 与 RequestAggregatesByRun 各覆盖一遍。
func TestUsageQueriesBatchIN(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	old := UsageQueryBatch
	UsageQueryBatch = 2
	defer func() { UsageQueryBatch = old }()

	now := time.Now().UTC()
	const parents = 5
	parentIDs := make([]string, 0, parents)
	for i := 0; i < parents; i++ {
		parent := &model.BotRun{
			ID: uuid.NewString(), BotID: "bot-1",
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: "batch-" + uuid.NewString(),
			Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
			ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		}
		if err := db.Create(parent).Error; err != nil {
			t.Fatalf("seed parent run: %v", err)
		}
		parentIDs = append(parentIDs, parent.ID)

		maint := &model.BotMaintenanceRun{
			ID: uuid.NewString(), BotID: "bot-1",
			Kind: model.BotMaintenanceCompaction, TriggerKey: "batch-maint-" + uuid.NewString(),
			ParentRunID: &parent.ID, Status: model.BotMaintenanceSucceeded,
			ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
			CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		}
		if err := db.Create(maint).Error; err != nil {
			t.Fatalf("seed maintenance run: %v", err)
		}
		cost := float64(i+1) * 0.1
		prompt := int64(100)
		req := &model.BotMaintenanceRequest{
			ID: uuid.NewString(), MaintenanceRunID: maint.ID, Ordinal: 1,
			Purpose: model.BotPurposeSummary, Model: "fake-model",
			Status: model.BotModelRequestSucceeded, CostEstimate: &cost,
			UsageComplete: true, PromptTokens: &prompt, TotalTokens: &prompt,
			PrefixHash: "p", ToolsetVersion: 1, StartedAt: now, FinishedAt: &now,
		}
		if err := db.Create(req).Error; err != nil {
			t.Fatalf("seed maintenance request: %v", err)
		}
		// 主线请求：计数与用量聚合的分批路径。
		mainReq := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: parent.ID, Ordinal: 1,
			Status: model.BotModelRequestSucceeded, StartedAt: now,
			PrefixHash: "p", ToolsHash: "t", RequestHash: "hash", TokenEstimator: "chars/4",
			CostEstimate: &cost, PromptTokens: &prompt, TotalTokens: &prompt,
		}
		if err := db.Create(mainReq).Error; err != nil {
			t.Fatalf("seed model request: %v", err)
		}
	}

	repo := NewBotUsageRepository(db)
	costs, err := repo.MaintenanceCostsByParentRun(parentIDs)
	if err != nil {
		t.Fatalf("MaintenanceCostsByParentRun: %v", err)
	}
	if len(costs) != parents {
		t.Fatalf("跨 3 批应取全 %d 个父 run，实得 %d", parents, len(costs))
	}
	for i, id := range parentIDs {
		want := float64(i+1) * 0.1
		if costs[id] == nil || *costs[id] < want-1e-9 || *costs[id] > want+1e-9 {
			t.Fatalf("父 run %d 的维护成本应为 %v，实得 %v", i, want, costs[id])
		}
	}

	aggs, err := repo.RequestAggregatesByRun(parentIDs)
	if err != nil {
		t.Fatalf("RequestAggregatesByRun: %v", err)
	}
	if len(aggs) != parents {
		t.Fatalf("跨 3 批应取全 %d 个 run 的聚合，实得 %d", parents, len(aggs))
	}
	for _, id := range parentIDs {
		if aggs[id].Total != 1 || aggs[id].Priced != 1 {
			t.Fatalf("run %s 聚合不符: %+v", id, aggs[id])
		}
	}
}

// INT-163 新查询的分批路径（批大小缩到 2）：统一请求聚合（含 cached 逐行
// 钳制）、首请求、重试计数、失败聚合与工具调用组合跨批结果与单批一致。
func TestInsightsQueriesBatchIN(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	old := UsageQueryBatch
	UsageQueryBatch = 2
	defer func() { UsageQueryBatch = old }()

	now := time.Now().UTC()
	const runs = 5
	runIDs := make([]string, 0, runs)
	for i := 0; i < runs; i++ {
		run := &model.BotRun{
			ID: uuid.NewString(), BotID: "bot-1",
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: "insights-" + uuid.NewString(),
			Status: model.BotRunStatusFailed, SessionKind: model.BotSessionKindMain,
			ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		}
		if err := db.Create(run).Error; err != nil {
			t.Fatalf("seed run: %v", err)
		}
		runIDs = append(runIDs, run.ID)

		prompt, cached, completion := int64(1000), int64(400), int64(100)
		cost := float64(i+1) * 0.1
		req := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
			Status: model.BotModelRequestSucceeded, StartedAt: now,
			PrefixHash: "p", ToolsHash: "t", RequestHash: "hash", TokenEstimator: "chars/4",
			PromptTokens: &prompt, CachedTokens: &cached, CompletionTokens: &completion,
			CostEstimate: &cost, ToolsTokensEstimate: 50,
		}
		if err := db.Create(req).Error; err != nil {
			t.Fatalf("seed request: %v", err)
		}
		retry := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: 2,
			Status: model.BotModelRequestFailed, StartedAt: now,
			PrefixHash: "p", ToolsHash: "t", RequestHash: "hash", TokenEstimator: "chars/4",
			ErrorCode: ptrRepoString("rate_limit"),
		}
		if err := db.Create(retry).Error; err != nil {
			t.Fatalf("seed failed request: %v", err)
		}
		if err := db.Create(&model.BotToolCall{
			ID: uuid.NewString(), BotID: "bot-1", RequestID: req.ID,
			ProviderToolCallID: "tc", Ordinal: 1,
			ToolName: "fs_project", Action: "list",
			ArgumentsJSON: "{}", ArgumentsHash: "ah", ExecutionMode: "sync",
			Status: model.BotCallStatusSucceeded, CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed tool call: %v", err)
		}
	}

	// 异常行（cached>prompt）：逐行钳到 prompt，机器人级/按天/按任务同分子。
	anomalyPrompt, anomalyCached := int64(300), int64(500)
	anomalyCost := 0.9
	anomaly := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: runIDs[0], Ordinal: 3,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "p", ToolsHash: "t", TokenEstimator: "chars/4",
		PromptTokens: &anomalyPrompt, CachedTokens: &anomalyCached,
		CostEstimate: &anomalyCost,
	}
	if err := db.Create(anomaly).Error; err != nil {
		t.Fatalf("seed anomaly request: %v", err)
	}

	repo := NewBotUsageRepository(db)
	aggs, err := repo.RequestAggregatesByRun(runIDs)
	if err != nil {
		t.Fatalf("RequestAggregatesByRun: %v", err)
	}
	if len(aggs) != runs {
		t.Fatalf("统一聚合跨批应取全 %d 个 run，实得 %d", runs, len(aggs))
	}
	// runIDs[0] 多一条异常行：cached 合计 = 400 + MIN(500,300) = 700，
	// prompt 合计 = 1000 + 300 = 1300（usage 完整与已计价口径各自一致）。
	agg0 := aggs[runIDs[0]]
	if agg0.Total != 3 || agg0.Priced != 2 || agg0.Counted != 2 {
		t.Fatalf("异常行计数不符: %+v", agg0)
	}
	if agg0.PromptSum != 1300 || agg0.CachedSum != 700 || agg0.CompletionSum != 100 || agg0.ToolsSum != 50 {
		t.Fatalf("cached 应逐行钳制（400+300）: %+v", agg0)
	}
	if agg0.PricedPromptSum != 1300 || agg0.PricedCachedSum != 700 || agg0.PricedCompletionSum != 100 {
		t.Fatalf("已计价 token 口径应与 usage 完整口径一致钳制: %+v", agg0)
	}
	if agg0.CostSum == nil || *agg0.CostSum < 0.1-1e-9 || *agg0.CostSum > 1.0+1e-9 {
		t.Fatalf("cost 合计应为 0.1+0.9，实得 %v", agg0.CostSum)
	}
	for _, id := range runIDs[1:] {
		agg := aggs[id]
		if agg.Total != 2 || agg.Priced != 1 ||
			agg.PromptSum != 1000 || agg.CachedSum != 400 ||
			agg.CompletionSum != 100 || agg.ToolsSum != 50 {
			t.Fatalf("统一聚合不符: %+v", agg)
		}
		if agg.PricedPromptSum != 1000 || agg.PricedCachedSum != 400 {
			t.Fatalf("priced 聚合不符: %+v", agg)
		}
	}

	// 维护表分支同一钳制口径（usage_complete 判定）。
	maint := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: "bot-1",
		Kind: model.BotMaintenanceCompaction, TriggerKey: "agg-maint-" + uuid.NewString(),
		Status:             model.BotMaintenanceSucceeded,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := db.Create(maint).Error; err != nil {
		t.Fatalf("seed maintenance run: %v", err)
	}
	mPrompt, mCached := int64(200), int64(900)
	maintReq := &model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: maint.ID, Ordinal: 1,
		Purpose: model.BotPurposeSummary, Model: "fake-model",
		Status: model.BotModelRequestSucceeded, UsageComplete: true,
		PromptTokens: &mPrompt, CachedTokens: &mCached,
		PrefixHash: "p", ToolsetVersion: 1, StartedAt: now, FinishedAt: &now,
	}
	if err := db.Create(maintReq).Error; err != nil {
		t.Fatalf("seed maintenance request: %v", err)
	}
	maintAggs, err := repo.MaintenanceRequestAggregatesByRun([]string{maint.ID})
	if err != nil {
		t.Fatalf("MaintenanceRequestAggregatesByRun: %v", err)
	}
	if maintAggs[maint.ID].PromptSum != 200 || maintAggs[maint.ID].CachedSum != 200 {
		t.Fatalf("维护表 cached 也应逐行钳制到 prompt: %+v", maintAggs[maint.ID])
	}

	first, err := repo.FirstRequestInputByRun(runIDs)
	if err != nil {
		t.Fatalf("FirstRequestInputByRun: %v", err)
	}
	if len(first) != runs || first[runIDs[0]].InputTokens != 1000 || first[runIDs[0]].Estimated {
		t.Fatalf("首请求扫描不符: %+v", first[runIDs[0]])
	}

	// 首请求内部不自洽（cached>prompt）：按估算列画点并标 estimated，
	// 与运行明细页「不可信实测」口径一致。
	anomalyRun := &model.BotRun{
		ID: uuid.NewString(), BotID: "bot-1",
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "first-anomaly-" + uuid.NewString(),
		Status: model.BotRunStatusFailed, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := db.Create(anomalyRun).Error; err != nil {
		t.Fatalf("seed anomaly run: %v", err)
	}
	anomalyFirst := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: anomalyRun.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "p", ToolsHash: "t", TokenEstimator: "chars/4",
		PromptTokens: &anomalyPrompt, CachedTokens: &anomalyCached,
		InputTokensEstimate: 1600,
	}
	if err := db.Create(anomalyFirst).Error; err != nil {
		t.Fatalf("seed anomaly first request: %v", err)
	}
	got, err := repo.FirstRequestInputByRun([]string{anomalyRun.ID})
	if err != nil {
		t.Fatalf("FirstRequestInputByRun anomaly: %v", err)
	}
	if !got[anomalyRun.ID].Estimated || got[anomalyRun.ID].InputTokens != 1600 {
		t.Fatalf("cached>prompt 首请求应取估算并标 estimated: %+v", got[anomalyRun.ID])
	}
	retries, err := repo.RetryCountsByRun(runIDs)
	if err != nil {
		t.Fatalf("RetryCountsByRun: %v", err)
	}
	if len(retries) != runs || retries[runIDs[0]] != 1 {
		t.Fatalf("重试计数应为 1，实得 %+v", retries[runIDs[0]])
	}
	failed, err := repo.FailedRequestAggsByRun(runIDs)
	if err != nil {
		t.Fatalf("FailedRequestAggsByRun: %v", err)
	}
	if len(failed) != runs {
		t.Fatalf("失败聚合跨批应取全 %d 行，实得 %d", runs, len(failed))
	}
	if failed[0].Requests != 1 || failed[0].ErrorCode == nil || *failed[0].ErrorCode != "rate_limit" || failed[0].CostSum != nil {
		t.Fatalf("失败聚合行不符: %+v", failed[0])
	}
	triples, err := repo.RunToolCallTriples(runIDs)
	if err != nil {
		t.Fatalf("RunToolCallTriples: %v", err)
	}
	if len(triples) != runs || triples[0].ToolName != "fs_project" || triples[0].Action != "list" {
		t.Fatalf("工具调用组合不符: %+v", triples)
	}
}

// 总览路径不回读 config_snapshot_json（P2 复审修复）：ListRunsInWindow /
// ListRunsByIDs 的 SELECT 不含快照列（单行数 KB、总览不消费），快照只经
// RunConfigSnapshotsByIDs 按 runIDs 单独补查。用查询回调捕获实际执行的 SQL
// 断言查询行为。
func TestUsageRunQueriesSkipSnapshotColumn(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var sqls []string
	captureSQL := func(tx *gorm.DB) {
		sqls = append(sqls, tx.Statement.SQL.String())
	}
	// 花费页查询全部走 Scan → Row 处理器（gorm:row），Find 走 Query 处理器
	// （gorm:query），两条都挂上才捕获得全。
	if err := db.Callback().Query().After("gorm:query").Register("test:capture_sql_query", captureSQL); err != nil {
		t.Fatalf("register query capture callback: %v", err)
	}
	if err := db.Callback().Row().After("gorm:row").Register("test:capture_sql_row", captureSQL); err != nil {
		t.Fatalf("register row capture callback: %v", err)
	}

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: "bot-snap",
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "snap-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: `{"catalog_id":"deepseek","model":"deepseek-flash"}`,
		CreatedAt:          now, StartedAt: &now, FinishedAt: &now,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}

	repo := NewBotUsageRepository(db)
	capture := func(run func()) []string {
		sqls = nil
		run()
		return sqls
	}
	assertNoSnapshot := func(what string, got []string) {
		t.Helper()
		for _, s := range got {
			if strings.Contains(s, "config_snapshot_json") {
				t.Fatalf("%s 的 SELECT 不应含 config_snapshot_json: %s", what, s)
			}
		}
	}
	assertNoSnapshot("ListRunsInWindow", capture(func() {
		if _, err := repo.ListRunsInWindow([]string{run.BotID}, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
			t.Fatalf("ListRunsInWindow: %v", err)
		}
	}))
	assertNoSnapshot("ListRunsByIDs", capture(func() {
		if _, err := repo.ListRunsByIDs([]string{run.ID}); err != nil {
			t.Fatalf("ListRunsByIDs: %v", err)
		}
	}))

	snapshotSQLs := capture(func() {
		snaps, err := repo.RunConfigSnapshotsByIDs([]string{run.ID})
		if err != nil {
			t.Fatalf("RunConfigSnapshotsByIDs: %v", err)
		}
		if snaps[run.ID] != run.ConfigSnapshotJSON {
			t.Fatalf("快照补查应取回原值，实得 %q", snaps[run.ID])
		}
	})
	foundSnapshotQuery := false
	for _, s := range snapshotSQLs {
		if strings.Contains(s, "config_snapshot_json") {
			foundSnapshotQuery = true
		}
	}
	if !foundSnapshotQuery {
		t.Fatalf("RunConfigSnapshotsByIDs 应查询 config_snapshot_json 列: %v", snapshotSQLs)
	}
}

func ptrRepoString(s string) *string { return &s }
