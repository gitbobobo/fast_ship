package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-147 第一部分：T3 工作区准备失败自动重试。判定纯函数用真实形状投影
// fixture 钉契约；调度阶段行为（宽限等待、节流、计数先加再发、失败不回退、
// 上限终态、deadline 重置）用 fakeReviewT3 钉。

// prepT3FailureMessage 线上故障的失败原文（fixture 与断言共用）。
const prepT3FailureMessage = "Workspace preparation failed during provision worktree: Git command failed in GitVcsDriver.fetchRemote … Another Git operation or a stale lock may be blocking the fetch"

// prepProjectionFixture T3 线程投影的真实形状（节选）：目标 run 按
// userMessageId 对上 Fast Ship 的 initial_message_id，status=failed 且带
// workspacePreparation；turnItems 与 visibleTurnItems 各有一份
// workspace_preparation_failed 的 error 条目（同一底层条目的两次投影）。
const prepProjectionFixture = `{
  "thread": {
    "id": "thr-1",
    "projectId": "proj-1",
    "title": "审查 PR #12",
    "branch": "t3/review-abc",
    "worktreePath": "/repo/.t3/worktrees/review-abc",
    "lineage": null,
    "archivedAt": null,
    "deletedAt": null,
    "updatedAt": "2026-10-22T01:05:00.000Z"
  },
  "runs": [
    { "id": "run-0", "ordinal": 0, "status": "completed", "userMessageId": "msg-0" },
    { "id": "run-1", "ordinal": 1, "status": "failed", "userMessageId": "msg-review-1",
      "workspacePreparation": { "type": "worktree", "baseRef": "feature" } }
  ],
  "messages": [],
  "providerSessions": [],
  "providerThreads": [],
  "providerTurns": [],
  "runtimeRequests": [],
  "turnItems": [
    { "id": "item-0", "type": "user_message", "status": "completed", "text": "审查提示词", "updatedAt": "2026-10-22T01:01:00.000Z" },
    { "id": "item-err", "type": "error", "status": "failed", "runId": "run-1",
      "failure": { "class": "git", "code": "workspace_preparation_failed", "message": "` + prepT3FailureMessage + `" },
      "updatedAt": "2026-10-22T01:02:03.456Z", "completedAt": "2026-10-22T01:02:04.000Z" }
  ],
  "visibleTurnItems": [
    { "position": 0, "visibility": "visible", "sourceThreadId": "thr-1", "sourceItemId": "item-0",
      "item": { "id": "item-0", "type": "user_message", "status": "completed", "text": "审查提示词", "updatedAt": "2026-10-22T01:01:00.000Z" } },
    { "position": 1, "visibility": "visible", "sourceThreadId": "thr-1", "sourceItemId": "item-err",
      "item": { "id": "item-err", "type": "error", "status": "failed", "runId": "run-1",
        "failure": { "code": "workspace_preparation_failed", "message": "` + prepT3FailureMessage + `" },
        "updatedAt": "2026-10-22T01:02:03.456Z", "completedAt": "2026-10-22T01:02:04.000Z" } }
  ]
}`

// decodePrepFixture 把 fixture（可带变体改动）解码成投影，与线上同一解码路径。
func decodePrepFixture(t *testing.T, mutate func(map[string]any)) *t3ThreadProjection {
	t.Helper()
	var base map[string]any
	if err := json.Unmarshal([]byte(prepProjectionFixture), &base); err != nil {
		t.Fatalf("fixture 解析失败: %v", err)
	}
	if mutate != nil {
		mutate(base)
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("fixture 重编码失败: %v", err)
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		t.Fatalf("投影解码失败: %v", err)
	}
	return parsed
}

// prepFixtureRun 取 runs 数组里 id 匹配的行。
func prepFixtureRun(m map[string]any, id string) map[string]any {
	runs, _ := m["runs"].([]any)
	for _, r := range runs {
		if run, ok := r.(map[string]any); ok && run["id"] == id {
			return run
		}
	}
	return nil
}

// prepFixtureErrItem 在 turnItems（或 visibleTurnItems 行内）取 id=item-err
// 的条目本体。
func prepFixtureErrItem(m map[string]any, where string) map[string]any {
	var rows []any
	switch where {
	case "turnItems":
		rows, _ = m["turnItems"].([]any)
	case "visibleTurnItems":
		if vis, ok := m["visibleTurnItems"].([]any); ok {
			for _, rowAny := range vis {
				if row, ok := rowAny.(map[string]any); ok && row["sourceItemId"] == "item-err" {
					item, _ := row["item"].(map[string]any)
					return item
				}
			}
		}
	}
	for _, it := range rows {
		if item, ok := it.(map[string]any); ok && item["id"] == "item-err" {
			return item
		}
	}
	return nil
}

// TestPrepRetryProjectionVerdict 判定纯函数的契约：可重试的完整形状与各不可
// 重试变体。
func TestPrepRetryProjectionVerdict(t *testing.T) {
	t.Run("retryable extracts run id time and message", func(t *testing.T) {
		parsed := decodePrepFixture(t, nil)
		out := failedPreparationFromProjection(parsed, "msg-review-1")
		if out == nil {
			t.Fatal("want retryable, got nil")
		}
		if out.RunID != "run-1" {
			t.Fatalf("RunID = %q, want run-1", out.RunID)
		}
		if out.FailedAt != "2026-10-22T01:02:03.456Z" {
			t.Fatalf("FailedAt = %q, want updatedAt 2026-10-22T01:02:03.456Z", out.FailedAt)
		}
		if out.Message != prepT3FailureMessage {
			t.Fatalf("Message = %q", out.Message)
		}
	})

	t.Run("item only in visibleTurnItems still found", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			items := m["turnItems"].([]any)
			kept := items[:0]
			for _, it := range items {
				if item, ok := it.(map[string]any); ok && item["id"] != "item-err" {
					kept = append(kept, it)
				}
			}
			m["turnItems"] = kept
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out == nil {
			t.Fatal("visibleTurnItems 里的失败条目也应命中")
		}
	})

	t.Run("duplicate item across both lists yields single verdict", func(t *testing.T) {
		// fixture 本身就是两处各一份：判定应稳定返回同一事实，不受重复影响。
		parsed := decodePrepFixture(t, nil)
		out := failedPreparationFromProjection(parsed, "msg-review-1")
		if out == nil || out.RunID != "run-1" {
			t.Fatalf("out = %+v, want run-1", out)
		}
	})

	t.Run("failed run without failure item", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			m["turnItems"] = []any{}
			m["visibleTurnItems"] = []any{}
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("无失败条目应不可重试, got %+v", out)
		}
	})

	t.Run("failure item cancelled", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			for _, where := range []string{"turnItems", "visibleTurnItems"} {
				if item := prepFixtureErrItem(m, where); item != nil {
					item["status"] = "cancelled"
				}
			}
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("失败条目已 cancelled 应不可重试, got %+v", out)
		}
	})

	t.Run("thread archived", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			thread := m["thread"].(map[string]any)
			thread["archivedAt"] = "2026-10-22T01:04:00.000Z"
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("已归档线程应不可重试, got %+v", out)
		}
	})

	t.Run("thread soft deleted", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			thread := m["thread"].(map[string]any)
			thread["deletedAt"] = "2026-10-22T01:04:00.000Z"
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("软删线程应不可重试, got %+v", out)
		}
	})

	t.Run("another run occupies the thread", func(t *testing.T) {
		for _, status := range []string{"preparing", "starting", "running", "waiting"} {
			parsed := decodePrepFixture(t, func(m map[string]any) {
				runs := m["runs"].([]any)
				m["runs"] = append(runs, map[string]any{
					"id": "run-2", "ordinal": 2, "status": status, "userMessageId": "msg-2",
				})
			})
			if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
				t.Fatalf("同线程 %s 的 run 占用时应不可重试, got %+v", status, out)
			}
		}
	})

	t.Run("target run not failed", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			prepFixtureRun(m, "run-1")["status"] = "running"
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("run 未 failed 应不可重试, got %+v", out)
		}
	})

	t.Run("target run without workspacePreparation", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			delete(prepFixtureRun(m, "run-1"), "workspacePreparation")
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("非工作区准备失败应不可重试, got %+v", out)
		}
	})

	t.Run("messageID matches no run", func(t *testing.T) {
		parsed := decodePrepFixture(t, nil)
		if out := failedPreparationFromProjection(parsed, "msg-other"); out != nil {
			t.Fatalf("messageID 未命中 run 应不可重试, got %+v", out)
		}
	})

	t.Run("same message picks the highest ordinal run", func(t *testing.T) {
		// 同一消息出现两条 run：只看 ordinal 最大的一条（最新状态），旧 failed
		// 快照不作数。失败条目必须属于这条 run（T3 重试复用同一条 run，
		// 条目总是随当前 run 走）。
		parsed := decodePrepFixture(t, func(m map[string]any) {
			prepFixtureRun(m, "run-1")["status"] = "completed"
			m["runs"] = append(m["runs"].([]any), map[string]any{
				"id": "run-2", "ordinal": 2, "status": "failed", "userMessageId": "msg-review-1",
				"workspacePreparation": map[string]any{"type": "worktree", "baseRef": "feature"},
			})
			for _, where := range []string{"turnItems", "visibleTurnItems"} {
				if item := prepFixtureErrItem(m, where); item != nil {
					item["runId"] = "run-2"
				}
			}
		})
		out := failedPreparationFromProjection(parsed, "msg-review-1")
		if out == nil || out.RunID != "run-2" {
			t.Fatalf("out = %+v, want run-2（取 ordinal 最大）", out)
		}
	})

	t.Run("highest ordinal wins regardless of array order", func(t *testing.T) {
		// 投影里 run 数组顺序不保证按 ordinal：最大的排在最前也得选中它。
		parsed := decodePrepFixture(t, func(m map[string]any) {
			runs := m["runs"].([]any)
			// 先放一条 ordinal 2 的 completed、再放原始 ordinal 1 的 failed：
			// 取数组最后一条（旧实现按顺序覆盖）会错选旧 failed。
			runs = append([]any{map[string]any{
				"id": "run-2", "ordinal": 2, "status": "completed", "userMessageId": "msg-review-1",
			}}, runs...)
			m["runs"] = runs
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("最新 ordinal 的 run 已 completed 应不可重试, got %+v", out)
		}
	})

	t.Run("latest run of the message decides", func(t *testing.T) {
		// 最新那条已不是 failed（如已被 T3 恢复重跑）：整体不可重试，不回看
		// 旧 failed 快照。
		parsed := decodePrepFixture(t, func(m map[string]any) {
			m["runs"] = append(m["runs"].([]any), map[string]any{
				"id": "run-2", "ordinal": 2, "status": "completed", "userMessageId": "msg-review-1",
			})
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("最新 run 已 completed 应不可重试, got %+v", out)
		}
	})

	t.Run("failure code mismatch", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			for _, where := range []string{"turnItems", "visibleTurnItems"} {
				if item := prepFixtureErrItem(m, where); item != nil {
					failure := item["failure"].(map[string]any)
					failure["code"] = "agent_crashed"
				}
			}
		})
		if out := failedPreparationFromProjection(parsed, "msg-review-1"); out != nil {
			t.Fatalf("其他失败类别应不可重试, got %+v", out)
		}
	})

	t.Run("missing updatedAt falls back to completedAt", func(t *testing.T) {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			for _, where := range []string{"turnItems", "visibleTurnItems"} {
				if item := prepFixtureErrItem(m, where); item != nil {
					delete(item, "updatedAt")
				}
			}
		})
		out := failedPreparationFromProjection(parsed, "msg-review-1")
		if out == nil {
			t.Fatal("want retryable")
		}
		if out.FailedAt != "2026-10-22T01:02:04.000Z" {
			t.Fatalf("FailedAt = %q, want completedAt 2026-10-22T01:02:04.000Z", out.FailedAt)
		}
	})
}

// seedPrepRetryRun 落一行已确认派发的代码运行（retryFailedPreparations 的
// 检查对象形状）。
func seedPrepRetryRun(t *testing.T, e *reviewEngineEnv, repo string, pr int, seedAt time.Time) *model.ReviewRun {
	t.Helper()
	return e.seedDispatchedRun(t, repo, pr, model.ReviewKindCode, model.ReviewRunDispatched, "", false, seedAt)
}

// TestPrepRetrySchedulerSendsAfterGrace 满 30s 宽限后发一次重试：commandId
// 按计数派生、计数置 1、deadline 重置。
func TestPrepRetrySchedulerSendsAfterGrace(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 31, seedAt)
	t3 := &fakeReviewT3{prepFailed: map[string]*T3FailedPreparation{
		run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-time.Minute).Format(time.RFC3339), Message: prepT3FailureMessage},
	}}
	svc := e.withT3(t3, nil, nil)

	tickAt := seedAt.Add(time.Minute)
	svc.retryFailedPreparations(tickAt)

	if len(t3.retried) != 1 || t3.retried[0] != run.ThreadID {
		t.Fatalf("retried = %v, want [%s]", t3.retried, run.ThreadID)
	}
	if t3.retryRunIDs[0] != "t3-run-1" {
		t.Fatalf("retryRunIDs = %v, want [t3-run-1]", t3.retryRunIDs)
	}
	wantCmd := fmt.Sprintf("fs-review-prep-retry-%s-1", run.ID)
	if t3.retryCmdIDs[0] != wantCmd {
		t.Fatalf("commandId = %q, want %q", t3.retryCmdIDs[0], wantCmd)
	}
	var after model.ReviewRun
	if err := e.svc.db.First(&after, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if after.PrepRetryCount != 1 {
		t.Fatalf("prep_retry_count = %d, want 1", after.PrepRetryCount)
	}
	if !after.DeadlineAt.Equal(tickAt.Add(reviewTimeout(after.Kind))) {
		t.Fatalf("deadline_at = %v, want %v", after.DeadlineAt, tickAt.Add(reviewTimeout(after.Kind)))
	}
}

// TestPrepRetrySchedulerWaitsGrace 失败出现未满 30s：不查发 RPC、计数不变、
// deadline 不动；宽限过后下个节流窗口才重试。
func TestPrepRetrySchedulerWaitsGrace(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 32, seedAt)
	t3 := &fakeReviewT3{prepFailed: map[string]*T3FailedPreparation{
		run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-10 * time.Second).Format(time.RFC3339), Message: prepT3FailureMessage},
	}}
	svc := e.withT3(t3, nil, nil)

	svc.retryFailedPreparations(seedAt)
	if len(t3.retried) != 0 {
		t.Fatalf("宽限期内不应重试, retried = %v", t3.retried)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PrepRetryCount != 0 {
		t.Fatalf("prep_retry_count = %d, want 0", after.PrepRetryCount)
	}
	if !after.DeadlineAt.Equal(seedAt.Add(time.Hour)) {
		t.Fatalf("deadline_at 不应变化, got %v", after.DeadlineAt)
	}

	// 同一 tick 之后 61s（宽限已过）：把节流账回拨成已过窗（节流以实际时刻
	// 登记，tick 时间不推动它）后再跑一轮 → 重试。
	svc.prepRetryChecked[run.ID] = time.Now().Add(-reviewPrepRetryQueryEvery - time.Second)
	svc.retryFailedPreparations(seedAt.Add(61 * time.Second))
	if len(t3.retried) != 1 {
		t.Fatalf("宽限后应重试一次, retried = %v", t3.retried)
	}
}

// TestPrepRetrySchedulerCapFailsRun 计数已达 3：不发第 4 次，运行落 failed
// 终态（错误码/原文/通知/令牌消费/PR sync_pending），计数保留。
func TestPrepRetrySchedulerCapFailsRun(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 33, seedAt)
	if err := e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
		Update("prep_retry_count", 3).Error; err != nil {
		t.Fatalf("seed count: %v", err)
	}
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "hash-cap", CreatedAt: seedAt, ExpiresAt: seedAt.Add(time.Hour),
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 33, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: run.HeadSHA, RoundNumber: 1, UpdatedAt: seedAt,
	})
	t3 := &fakeReviewT3{prepFailed: map[string]*T3FailedPreparation{
		run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-time.Minute).Format(time.RFC3339), Message: prepT3FailureMessage},
	}}
	svc := e.withT3(t3, nil, nil)

	svc.retryFailedPreparations(seedAt)

	if len(t3.retried) != 0 {
		t.Fatalf("达上限不应再发重试, retried = %v", t3.retried)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunFailed {
		t.Fatalf("status = %q, want failed", after.Status)
	}
	if after.ErrorCode != "workspace_preparation_failed" {
		t.Fatalf("error_code = %q", after.ErrorCode)
	}
	if after.ErrorMessage != prepT3FailureMessage {
		t.Fatalf("error_message = %q, want T3 原文", after.ErrorMessage)
	}
	if after.PrepRetryCount != 3 {
		t.Fatalf("prep_retry_count = %d, want 3（保留）", after.PrepRetryCount)
	}
	var tok model.ReviewRunToken
	e.svc.db.First(&tok, "run_id = ?", run.ID)
	if tok.ConsumedAt == nil {
		t.Fatal("令牌应被消费")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 33)
	if !ledger.SyncPending {
		t.Fatal("PR 总账应置 sync_pending")
	}
	var n int64
	e.svc.db.Model(&model.Notification{}).
		Where("kind = ? AND user_id = ?", model.NotificationReviewDispatchFailed, e.user.ID).Count(&n)
	if n != 1 {
		t.Fatalf("review_dispatch_failed 通知 = %d, want 1", n)
	}
}

// TestPrepRetrySchedulerRPCFailureKeepsCount RPC 失败：计数不回退；节流窗口
// 内不重查；下个窗口按新计数派生 commandId 再试。
func TestPrepRetrySchedulerRPCFailureKeepsCount(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 34, seedAt)
	t3 := &fakeReviewT3{
		retryErr: errors.New("rpc down"),
		prepFailed: map[string]*T3FailedPreparation{
			run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-time.Minute).Format(time.RFC3339), Message: prepT3FailureMessage},
		},
	}
	svc := e.withT3(t3, nil, nil)
	if err := e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "hash-rpc-fail", CreatedAt: seedAt,
		ExpiresAt: run.DeadlineAt.Add(reviewTokenGrace),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var tokenBefore model.ReviewRunToken
	e.svc.db.First(&tokenBefore, "run_id = ?", run.ID)

	// tick 用晚于 seedAt 的时刻：若 RPC 失败却误走了延期，deadline/expires_at
	// 会明显变化（用 seedAt 时「新 deadline」恰好等于原 deadline，断言会失效）。
	svc.retryFailedPreparations(seedAt.Add(2 * time.Minute))
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PrepRetryCount != 1 || len(t3.retried) != 1 {
		t.Fatalf("count/retried = %d/%d, want 1/1", after.PrepRetryCount, len(t3.retried))
	}
	// RPC 失败不延 deadline、不延令牌（延期是可重试成功的后续动作，不是预扣）。
	if !after.DeadlineAt.Equal(run.DeadlineAt) {
		t.Fatalf("deadline_at = %v, want 不变 %v", after.DeadlineAt, run.DeadlineAt)
	}
	var tokenAfter model.ReviewRunToken
	e.svc.db.First(&tokenAfter, "run_id = ?", run.ID)
	if !tokenAfter.ExpiresAt.Equal(tokenBefore.ExpiresAt) {
		t.Fatalf("token expires_at = %v, want 不变 %v", tokenAfter.ExpiresAt, tokenBefore.ExpiresAt)
	}

	// 10s 后相邻 tick：节流内不查 T3，也不重复重试。
	svc.retryFailedPreparations(seedAt.Add(10 * time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if len(t3.prepCalls) != 1 || after.PrepRetryCount != 1 {
		t.Fatalf("节流内 prepCalls/count = %d/%d, want 1/1", len(t3.prepCalls), after.PrepRetryCount)
	}

	// 61s 后（把节流账回拨成已过窗：节流以实际时刻登记，tick 时间不推动它）：
	// 计数推进到 2，commandId 按新值派生。
	svc.prepRetryChecked[run.ID] = time.Now().Add(-reviewPrepRetryQueryEvery - time.Second)
	svc.retryFailedPreparations(seedAt.Add(61 * time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PrepRetryCount != 2 || len(t3.retried) != 2 {
		t.Fatalf("count/retried = %d/%d, want 2/2", after.PrepRetryCount, len(t3.retried))
	}
	wantCmd := fmt.Sprintf("fs-review-prep-retry-%s-2", run.ID)
	if t3.retryCmdIDs[1] != wantCmd {
		t.Fatalf("commandId = %q, want %q", t3.retryCmdIDs[1], wantCmd)
	}
}

// prepTestClock 可推进的假时钟：配合 retryFailedPreparationsBudgeted 注入
// 「慢投影读」，确定性复现阶段预算耗尽后的轮转，不用真等几十秒。
type prepTestClock struct{ at time.Time }

func (c *prepTestClock) now() time.Time { return c.at }

func (c *prepTestClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// TestPrepRetryRotatesPastBudget 慢投影读吃满一轮预算时，下一轮从断点继续：
// 5 条运行、每条读 30.5s、阶段预算 60s（每轮只够 2 条），第 3 轮必须轮到
// 排在最后的 E。若每轮都从 created_at 队首重新扫，A/B（或 C/D）会反复吃满
// 预算，E 直到超时都轮不到。
func TestPrepRetryRotatesPastBudget(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	var runs []*model.ReviewRun
	for i := 0; i < 5; i++ {
		runs = append(runs, seedPrepRetryRun(t, e, repo, 70+i, seedAt))
	}
	clock := &prepTestClock{at: seedAt}
	t3 := &fakeReviewT3{}
	t3.prepFn = func(string) (*T3FailedPreparation, error) {
		clock.advance(30*time.Second + 500*time.Millisecond)
		return nil, nil // 投影判定「不可重试」：只验查了谁
	}
	svc := e.withT3(t3, nil, nil)

	budget := 60 * time.Second
	// 假时钟同时驱动预算与 60s 节流窗口：每条读 30.5s，窗口会在三轮里真实
	// 到期。把轮转去掉（改回固定从 created_at 队首扫）时，第 3 轮会重复读
	// A/B 而查不到尾部，下面的计数与集合断言会失败——这条用例因此能识别
	// 「回到固定队首」的回归。
	svc.retryFailedPreparationsBudgeted(seedAt, budget, clock.now)
	if len(t3.prepCalls) != 2 {
		t.Fatalf("第 1 轮 prepCalls = %d, want 2（60s 预算 / 30.5s）", len(t3.prepCalls))
	}
	svc.retryFailedPreparationsBudgeted(seedAt, budget, clock.now)
	if len(t3.prepCalls) != 4 {
		t.Fatalf("第 2 轮累计 prepCalls = %d, want 4", len(t3.prepCalls))
	}
	svc.retryFailedPreparationsBudgeted(seedAt, budget, clock.now)
	// 三条轮的前 5 次调用必须覆盖全部 5 条运行：轮转保证第一圈走完再回头。
	// 去掉轮转（每轮从 created_at 队首扫）时第 3 轮会重复读前两条，前 5 次
	// 只覆盖 4 条，这里会失败。
	if len(t3.prepCalls) < 5 {
		t.Fatalf("三轮 prepCalls = %v, want 至少 5 次", t3.prepCalls)
	}
	firstLap := map[string]bool{}
	for _, call := range t3.prepCalls[:5] {
		firstLap[strings.SplitN(call, ":", 2)[0]] = true
	}
	for _, run := range runs {
		if !firstLap[run.ThreadID] {
			t.Fatalf("前 5 次调用未覆盖运行 %s（尾部被饿死）: %v", run.ThreadID, t3.prepCalls)
		}
	}
	// 第一圈内同一条运行至多读一次（每轮 60s 预算 / 单次 30.5s，节流窗口
	// 由同一假时钟驱动，重复读说明轮转没生效）。
	if dup := duplicateThreadCall(t3.prepCalls[:5]); dup != "" {
		t.Fatalf("第一圈内重复读运行 %s: %v", dup, t3.prepCalls)
	}
}

// TestPrepRetrySchedulerThrottlesQuery 每运行最多每 60s 查一次 T3 投影：
// 相邻 tick 不重复查，节流窗口过后恢复（节流以实际时刻登记，测试直接回拨账）。
func TestPrepRetrySchedulerThrottlesQuery(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 35, seedAt)
	t3 := &fakeReviewT3{} // 投影判定不可重试（无失败条目）
	svc := e.withT3(t3, nil, nil)

	svc.retryFailedPreparations(seedAt)
	svc.retryFailedPreparations(seedAt.Add(10 * time.Second))
	if len(t3.prepCalls) != 1 {
		t.Fatalf("相邻 tick prepCalls = %d, want 1", len(t3.prepCalls))
	}
	svc.prepRetryChecked[run.ID] = time.Now().Add(-reviewPrepRetryQueryEvery - time.Second)
	svc.retryFailedPreparations(seedAt.Add(61 * time.Second))
	if len(t3.prepCalls) != 2 {
		t.Fatalf("过窗后 prepCalls = %d, want 2", len(t3.prepCalls))
	}
	if len(t3.retried) != 0 {
		t.Fatalf("无失败条目不应重试, retried = %v", t3.retried)
	}
}

// TestPrepRetrySeparateReadAndRPCBudgets 投影读与重试 RPC 各用独立超时：慢读
// 吃掉读预算的大部分后，RPC 仍拿到完整的一份。共用同一个上下文时 RPC 只剩
// 残余时间，超时也会先加计数且不回退——连三次慢读就会在 T3 从未受理重试的
// 情况下把运行误落 workspace_preparation_failed。
func TestPrepRetrySeparateReadAndRPCBudgets(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	seedPrepRetryRun(t, e, repo, 80, seedAt)
	failedAt := seedAt.Add(-time.Minute).Format(time.RFC3339)

	var rpcRemaining time.Duration
	t3 := &fakeReviewT3{
		prepFn: func(string) (*T3FailedPreparation, error) {
			// 读占掉读预算的 3/4；共用上下文时 RPC 只剩 1/4。
			time.Sleep(30 * time.Millisecond)
			return &T3FailedPreparation{RunID: "t3-run-1", FailedAt: failedAt, Message: prepT3FailureMessage}, nil
		},
		retryFn: func(ctx context.Context, _ string) error {
			if deadline, ok := ctx.Deadline(); ok {
				rpcRemaining = time.Until(deadline)
			}
			return nil
		},
	}
	svc := e.withT3(t3, nil, nil)
	svc.prepRetryReadTimeout = 40 * time.Millisecond
	svc.prepRetryRPCTimeout = 40 * time.Millisecond

	svc.retryFailedPreparations(seedAt)
	if len(t3.retried) != 1 {
		t.Fatalf("retried = %v, want 一次重试", t3.retried)
	}
	if rpcRemaining <= 25*time.Millisecond {
		t.Fatalf("RPC 剩余预算 = %v，应有独立的完整预算（共用上下文会只剩残余）", rpcRemaining)
	}
}

// TestPrepRetryThrottleIgnoresStaleTickTime 节流以**实际时刻**登记，不随 tick
// 起点漂移：一个被慢投影读拖长的 tick 结束后，同一批队首不能因为「tick 时间
// 差值已过 60s」就再次重新到期、反复吃满预算（那会让尾部饿到超时）。
func TestPrepRetryThrottleIgnoresStaleTickTime(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 38, seedAt)
	t3 := &fakeReviewT3{}
	svc := e.withT3(t3, nil, nil)

	svc.retryFailedPreparations(seedAt)
	if len(t3.prepCalls) != 1 {
		t.Fatalf("首轮 prepCalls = %d, want 1", len(t3.prepCalls))
	}
	// 模拟「上一轮 tick 耗时 2 分钟」：现在传入的 tick 时间比上次晚 2 分钟，
	// 但墙上时钟只过了几毫秒——这条运行不能被认为已过 60s 节流窗口。
	svc.retryFailedPreparations(seedAt.Add(2 * time.Minute))
	if len(t3.prepCalls) != 1 {
		t.Fatalf("stale tick 时间不应绕过节流, prepCalls = %d, want 1（run %s）", len(t3.prepCalls), run.ID)
	}
}

// TestPrepRetrySkipsWithoutT3 T3 访问面未配置时整段跳过。
func TestPrepRetrySkipsWithoutT3(t *testing.T) {
	e := newReviewEngineEnv(t) // e.review 不带 t3
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	seedPrepRetryRun(t, e, repo, 36, seedAt)

	e.review.retryFailedPreparations(seedAt) // 不 panic 即通过
}

// TestPrepRetryExtendsTokenWithDeadline 重试受理后 deadline 与未消费令牌的
// 有效期一起延：expires_at = 新 deadline + 5min（令牌有效期原是签发时的
// deadline+5min，只延 deadline 会让 Agent 在旧过期点开始收到 40106）。
func TestPrepRetryExtendsTokenWithDeadline(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 37, seedAt)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "hash-extend", CreatedAt: seedAt,
		ExpiresAt: seedAt.Add(time.Hour).Add(reviewTokenGrace), // 首签时的旧值
	})
	t3 := &fakeReviewT3{prepFailed: map[string]*T3FailedPreparation{
		run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-time.Minute).Format(time.RFC3339), Message: prepT3FailureMessage},
	}}
	svc := e.withT3(t3, nil, nil)

	tickAt := seedAt.Add(time.Minute)
	svc.retryFailedPreparations(tickAt)

	var after model.ReviewRun
	if err := e.svc.db.First(&after, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if after.PrepRetryCount != 1 {
		t.Fatalf("prep_retry_count = %d, want 1", after.PrepRetryCount)
	}
	wantDeadline := tickAt.Add(reviewTimeout(after.Kind))
	if !after.DeadlineAt.Equal(wantDeadline) {
		t.Fatalf("deadline_at = %v, want %v", after.DeadlineAt, wantDeadline)
	}
	var tok model.ReviewRunToken
	if err := e.svc.db.First(&tok, "run_id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if tok.ConsumedAt != nil {
		t.Fatal("重试受理不应消费令牌")
	}
	if !tok.ExpiresAt.Equal(wantDeadline.Add(reviewTokenGrace)) {
		t.Fatalf("expires_at = %v, want 新 deadline+5min = %v（旧值 %v）",
			tok.ExpiresAt, wantDeadline.Add(reviewTokenGrace), seedAt.Add(time.Hour).Add(reviewTokenGrace))
	}
}

// TestPrepRetryTerminalRunUntouched RPC 期间运行被并发终态化（如被取消）：
// 返回后 deadline_at 与令牌 expires_at 都保持原值——终态行不得改写 deadline、
// 也不得重新获得活的令牌窗口；计数不回退。
func TestPrepRetryTerminalRunUntouched(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	run := seedPrepRetryRun(t, e, repo, 38, seedAt)
	origDeadline := seedAt.Add(time.Hour)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "hash-frozen", CreatedAt: seedAt,
		ExpiresAt: origDeadline.Add(reviewTokenGrace),
	})
	t3 := &fakeReviewT3{
		prepFailed: map[string]*T3FailedPreparation{
			run.ThreadID: {RunID: "t3-run-1", FailedAt: seedAt.Add(-time.Minute).Format(time.RFC3339), Message: prepT3FailureMessage},
		},
		// retryFn 在重试 RPC 里把运行改成 cancelled：模拟受理期间被用户取消。
		retryFn: func(context.Context, string) error {
			return e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).
				Updates(map[string]any{"status": model.ReviewRunCancelled, "error_code": "user_cancelled"}).Error
		},
	}
	svc := e.withT3(t3, nil, nil)

	svc.retryFailedPreparations(seedAt.Add(time.Minute))

	if len(t3.retried) != 1 {
		t.Fatalf("RPC 应已发出, retried = %v", t3.retried)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCancelled {
		t.Fatalf("status = %q, want cancelled（retryFn 的终态化应保留）", after.Status)
	}
	if after.PrepRetryCount != 1 {
		t.Fatalf("prep_retry_count = %d, want 1（先加不回退）", after.PrepRetryCount)
	}
	if !after.DeadlineAt.Equal(origDeadline) {
		t.Fatalf("deadline_at = %v, want 原值 %v（终态不改写）", after.DeadlineAt, origDeadline)
	}
	var tok model.ReviewRunToken
	e.svc.db.First(&tok, "run_id = ?", run.ID)
	if tok.ConsumedAt != nil {
		t.Fatal("本阶段不消费令牌")
	}
	if !tok.ExpiresAt.Equal(origDeadline.Add(reviewTokenGrace)) {
		t.Fatalf("expires_at = %v, want 原值 %v（终态不改写）", tok.ExpiresAt, origDeadline.Add(reviewTokenGrace))
	}
}

// TestPrepRetryBatchFairness 批量公平性：节流命中不消耗网络预算，最早的
// 20 条都在节流期时第 21 条仍被检查——旧的批量上限会让它永远排在 20 条
// 名额之外。节流先由第一轮 tick 建立，摘掉第 21 条的登记后用第二轮 tick 验证。
func TestPrepRetryBatchFairness(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	seedAt := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	var runs []*model.ReviewRun
	for i := 0; i < 21; i++ {
		runs = append(runs, seedPrepRetryRun(t, e, repo, 100+i, seedAt))
	}
	// 第 21 条显式排到 created_at 最后：它是最晚创建、旧逻辑最可能饿死的。
	if err := e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", runs[20].ID).
		Update("created_at", seedAt.Add(time.Second)).Error; err != nil {
		t.Fatalf("bump created_at: %v", err)
	}

	t3 := &fakeReviewT3{}
	svc := e.withT3(t3, nil, nil)

	// 第一轮：全部 21 条入节流表（fake 调用即时返回，不触发阶段预算）。
	svc.retryFailedPreparations(seedAt)
	if len(t3.prepCalls) != 21 {
		t.Fatalf("第一轮 prepCalls = %d, want 21", len(t3.prepCalls))
	}

	// 第二轮（仍在节流窗口内）：前 20 条节流命中直接跳过，只有第 21 条被查。
	delete(svc.prepRetryChecked, runs[20].ID)
	t3.prepCalls = nil
	svc.retryFailedPreparations(seedAt.Add(10 * time.Second))

	if len(t3.prepCalls) != 1 {
		t.Fatalf("第二轮 prepCalls = %v, want 只有第 21 条", t3.prepCalls)
	}
	if !strings.HasPrefix(t3.prepCalls[0], runs[20].ThreadID+":") {
		t.Fatalf("被检查的应是第 21 条（thread %s）, got %q", runs[20].ThreadID, t3.prepCalls[0])
	}
}

// duplicateThreadCall 返回第一个被重复检查的 threadID（无重复返回空串）。
func duplicateThreadCall(calls []string) string {
	seen := map[string]bool{}
	for _, call := range calls {
		th := strings.SplitN(call, ":", 2)[0]
		if seen[th] {
			return th
		}
		seen[th] = true
	}
	return ""
}

// TestPrepRetryCursorRotation 游标轮转的边界：空候选、游标失效、游标在末尾
// （环形回到队首）、游标在中间（从下一条开始）。
func TestPrepRetryCursorRotation(t *testing.T) {
	mk := func(ids ...string) []model.ReviewRun {
		out := make([]model.ReviewRun, 0, len(ids))
		for _, id := range ids {
			out = append(out, model.ReviewRun{ID: id})
		}
		return out
	}
	order := func(runs []model.ReviewRun) []string {
		out := make([]string, 0, len(runs))
		for i := range runs {
			out = append(out, runs[i].ID)
		}
		return out
	}
	e := newReviewEngineEnv(t)
	svc := e.withT3(&fakeReviewT3{}, nil, nil)

	if got := svc.prepRetryFromCursor(nil); len(got) != 0 {
		t.Fatalf("空候选应返回空, got %v", order(got))
	}
	runs := mk("a", "b", "c")
	if got := order(svc.prepRetryFromCursor(runs)); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("无游标应从队首开始, got %v", got)
	}
	svc.setPrepRetryCursor("gone")
	if got := order(svc.prepRetryFromCursor(runs)); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("游标失效应从队首开始, got %v", got)
	}
	svc.setPrepRetryCursor("b")
	if got := order(svc.prepRetryFromCursor(runs)); strings.Join(got, ",") != "c,a,b" {
		t.Fatalf("游标在中间应从下一条开始, got %v", got)
	}
	svc.setPrepRetryCursor("c")
	if got := order(svc.prepRetryFromCursor(runs)); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("游标在末尾应环形回队首, got %v", got)
	}
}
