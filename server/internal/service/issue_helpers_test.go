package service

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// applyExplicitWorkflowStatus 的状态→时间戳矩阵按分桶归类（INT-239）：
// unset 与 todo 同支、in_progress、done 各一支，未识别取值不动时间戳。
func TestApplyExplicitWorkflowStatusBucketMatrix(t *testing.T) {
	now := time.Now().UTC()
	started := now.Add(-time.Hour)
	completed := now

	newMeta := func() *model.IssueInternalMeta { return &model.IssueInternalMeta{} }

	t.Run("空串与 todo 同支：未完成时清 started_at", func(t *testing.T) {
		for _, status := range []model.IssueWorkflowStatus{"", model.IssueWorkflowStatusTodo} {
			meta := newMeta()
			meta.StartedAt = &started
			applyExplicitWorkflowStatus(meta, status, now)
			if meta.StartedAt != nil {
				t.Fatalf("status %q: StartedAt 应清空", status)
			}
		}
	})

	t.Run("空串与 todo 同支：已完成保留 started_at", func(t *testing.T) {
		for _, status := range []model.IssueWorkflowStatus{"", model.IssueWorkflowStatusTodo} {
			meta := newMeta()
			meta.StartedAt = &started
			meta.CompletedAt = &completed
			applyExplicitWorkflowStatus(meta, status, now)
			if meta.StartedAt == nil || !meta.StartedAt.Equal(started) {
				t.Fatalf("status %q: StartedAt 应保留", status)
			}
		}
	})

	t.Run("in_progress 补 started_at、已有不覆盖", func(t *testing.T) {
		meta := newMeta()
		applyExplicitWorkflowStatus(meta, model.IssueWorkflowStatusInProgress, now)
		if meta.StartedAt == nil || !meta.StartedAt.Equal(now) {
			t.Fatalf("应写入 now，got %v", meta.StartedAt)
		}
		meta2 := newMeta()
		meta2.StartedAt = &started
		applyExplicitWorkflowStatus(meta2, model.IssueWorkflowStatusInProgress, now)
		if !meta2.StartedAt.Equal(started) {
			t.Fatalf("已有 started_at 不应覆盖")
		}
	})

	t.Run("done 补齐 started_at 与 completed_at", func(t *testing.T) {
		meta := newMeta()
		applyExplicitWorkflowStatus(meta, model.IssueWorkflowStatusDone, now)
		if meta.StartedAt == nil || meta.CompletedAt == nil {
			t.Fatalf("两个时间戳都应写入")
		}
	})

	t.Run("未识别取值不动时间戳", func(t *testing.T) {
		meta := newMeta()
		meta.StartedAt = &started
		applyExplicitWorkflowStatus(meta, "bogus", now)
		if !meta.StartedAt.Equal(started) || meta.CompletedAt != nil {
			t.Fatalf("未知取值不应改时间戳")
		}
		if meta.WorkflowStatus != "bogus" {
			t.Fatalf("WorkflowStatus 仍应写入原值")
		}
	})
}
