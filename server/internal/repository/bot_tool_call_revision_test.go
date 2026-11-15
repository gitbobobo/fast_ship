package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-217：调用行 revision 单调代次的仓储行为。所有改投影字段的更新与状态
// CAS 在同一条 UPDATE 里 revision+1；0 行（CAS 失败）不 bump；
// BackfillResultItems 只补 result_items（不进 API 投影）明确不 bump。

func setupBotToolCallRevisionDB(t *testing.T) (*gorm.DB, *BotToolCallRepository) {
	t.Helper()
	// 不开外键：孤立 call 行足够验证 revision 行为，不必连带建 bot/request 行。
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewBotToolCallRepository(db)
}

func insertRevisionFixtureCall(t *testing.T, db *gorm.DB, id string, status model.BotToolCallStatus) {
	t.Helper()
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: id, BotID: "bot-1", RequestID: "req-" + id,
		ProviderToolCallID: "prov-" + id, Ordinal: 1,
		ToolName: "fs_project", Action: "create",
		ArgumentsJSON: `{"action":"create"}`, ArgumentsHash: "h",
		ExecutionMode: string(model.BotExecutionAsync), RequiresConfirmation: true,
		Status: status, ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := db.Create(call).Error; err != nil {
		t.Fatalf("insert call: %v", err)
	}
}

func revisionOfCall(t *testing.T, db *gorm.DB, id string) (model.BotToolCallStatus, int64) {
	t.Helper()
	var row model.BotToolCall
	if err := db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("reload call: %v", err)
	}
	return row.Status, row.Revision
}

func TestBotToolCallRevisionBumpsOnUpdates(t *testing.T) {
	db, repo := setupBotToolCallRevisionDB(t)
	now := time.Now().UTC()
	insertRevisionFixtureCall(t, db, "call-1", model.BotCallStatusPendingConfirmation)
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 0 {
		t.Fatalf("新建行 revision 应为 0，实际 %d", rev)
	}

	// 确认决定 CAS（pending_confirmation → queued）。
	ok, err := repo.UpdateWhereTx(db, "call-1", string(model.BotCallStatusPendingConfirmation),
		map[string]interface{}{"status": model.BotCallStatusQueued, "decision": "approve", "decided_at": now})
	if !ok || err != nil {
		t.Fatalf("decide CAS: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 1 {
		t.Fatalf("决定后 revision 应为 1，实际 %d", rev)
	}

	// 认领 CAS（queued → running）。
	if ok, err = repo.ClaimQueuedTx(db, "call-1", now); !ok || err != nil {
		t.Fatalf("claim CAS: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 2 {
		t.Fatalf("认领后 revision 应为 2，实际 %d", rev)
	}

	// 并发第二个认领 0 行：单胜者，revision 不再多 bump。
	if ok, err = repo.ClaimQueuedTx(db, "call-1", now); ok || err != nil {
		t.Fatalf("重复认领应 0 行: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 2 {
		t.Fatalf("失败认领不得 bump，revision 应为 2，实际 %d", rev)
	}

	// 取消标记路径（cancel_requested_at 进投影，同样 bump）。
	if ok, err = repo.UpdateWhere("call-1", string(model.BotCallStatusRunning),
		map[string]interface{}{"cancel_requested_at": now}); !ok || err != nil {
		t.Fatalf("cancel request: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 3 {
		t.Fatalf("取消标记后 revision 应为 3，实际 %d", rev)
	}

	// 终态严格版 CAS 条件 cancel_requested_at IS NULL 不满足：0 行不 bump。
	if ok, err = repo.UpdateWhereRunningFreshTx(db, "call-1",
		map[string]interface{}{"status": model.BotCallStatusSucceeded, "finished_at": now}); ok || err != nil {
		t.Fatalf("带取消标记的成功终态应 0 行: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 3 {
		t.Fatalf("0 行终态不得 bump，revision 应为 3，实际 %d", rev)
	}

	// unfinished 定界更新（取消收尾）。
	if ok, err = repo.UpdateWhereUnfinishedTx(db, "call-1",
		map[string]interface{}{"status": model.BotCallStatusCancelled, "finished_at": now}); !ok || err != nil {
		t.Fatalf("cancel settle: ok=%v err=%v", ok, err)
	}
	if st, rev := revisionOfCall(t, db, "call-1"); st != model.BotCallStatusCancelled || rev != 4 {
		t.Fatalf("取消收尾后应为 cancelled/4，实际 %s/%d", st, rev)
	}

	// BackfillResultItems 只补 result_items（不进 API 投影）：明确不 bump。
	if ok, err = repo.BackfillResultItems("call-1", 3); !ok || err != nil {
		t.Fatalf("backfill: ok=%v err=%v", ok, err)
	}
	if _, rev := revisionOfCall(t, db, "call-1"); rev != 4 {
		t.Fatalf("BackfillResultItems 不得 bump，revision 应为 4，实际 %d", rev)
	}
}
