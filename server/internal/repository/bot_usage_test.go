package repository

import (
	"fmt"
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
