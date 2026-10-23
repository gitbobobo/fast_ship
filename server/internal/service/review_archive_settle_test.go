package service

import (
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-158 归档收尾宽限：结果落库后 agent 的 T3 run 还在写收尾回复时立刻归档
// 会切断 provider 事件流。判定纯函数用真实形状投影 fixture（与 INT-147 共用
// prepProjectionFixture）钉契约；归档阶段行为（宽限内等待、404/超宽限照常归
// 档、未确认启动先停线程）用 fakeReviewT3 钉。

// TestThreadTurnSettledFromProjection 收尾判定的契约：非终态 run（活动态、
// queued、未识别状态，按 userMessageId 匹配、同一消息多条取 ordinal 最大）
// 未收尾返回 false；run 落已知终态集、线程已归档返回 true；投影里没有该消
// 息的 run 不能证明已收尾，同样返回 false。
func TestThreadTurnSettledFromProjection(t *testing.T) {
	// 基线 fixture：run-1（msg-review-1，ordinal 1）status=failed 已终态。
	if settled := threadTurnSettledFromProjection(decodePrepFixture(t, nil), "msg-review-1"); !settled {
		t.Fatal("终态 run 应判定已收尾")
	}
	// 另一条 completed run 的消息同样已收尾。
	if settled := threadTurnSettledFromProjection(decodePrepFixture(t, nil), "msg-0"); !settled {
		t.Fatal("completed run 应判定已收尾")
	}
	// 投影里没有该消息的 run：不能证明已收尾，按未收尾处理（等宽限兜底）。
	if settled := threadTurnSettledFromProjection(decodePrepFixture(t, nil), "msg-absent"); settled {
		t.Fatal("投影无该消息应判定未收尾")
	}
	// messageID 为空（存量行无 initial_message_id）：空串会撞上缺
	// userMessageId 的 run，同样按未收尾处理。
	parsed := decodePrepFixture(t, func(m map[string]any) {
		runs, _ := m["runs"].([]any)
		m["runs"] = append(runs, map[string]any{
			"id": "run-nomsg", "ordinal": 9, "status": "failed",
		})
	})
	if settled := threadTurnSettledFromProjection(parsed, ""); settled {
		t.Fatal("空 messageID 不得匹配缺 userMessageId 的 run，应判定未收尾")
	}
	// 线程已归档/已删除 + run 仍报活动态：归档覆盖活动态——没有可切断的
	// 事件流，按已收尾处理（用活动 run 钉住这条短路，终态 run 测不出）。
	for _, field := range []string{"archivedAt", "deletedAt"} {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			thread, _ := m["thread"].(map[string]any)
			thread[field] = "2026-01-02T00:00:00Z"
			if run := prepFixtureRun(m, "run-1"); run != nil {
				run["status"] = "running"
			}
		})
		if settled := threadTurnSettledFromProjection(parsed, "msg-review-1"); !settled {
			t.Fatalf("线程 %s 时活动 run 仍应判定已收尾", field)
		}
	}
	// 目标 run 处于活动态、排队或出现未识别状态：非终态，未收尾（「不在活动
	// 集」不等于「已收尾」——queued 与未来新增状态都不能放行）。
	for _, status := range []string{"preparing", "starting", "running", "waiting", "queued", "paused-new-state"} {
		parsed := decodePrepFixture(t, func(m map[string]any) {
			if run := prepFixtureRun(m, "run-1"); run != nil {
				run["status"] = status
			}
		})
		if settled := threadTurnSettledFromProjection(parsed, "msg-review-1"); settled {
			t.Fatalf("非终态 run（%s）应判定未收尾", status)
		}
	}
	// 同一消息多条 run 取 ordinal 最大：新 run 在排队（waiting），旧 failed
	// 快照不代表当前状态——取错旧条会误判已收尾。
	parsed = decodePrepFixture(t, func(m map[string]any) {
		runs, _ := m["runs"].([]any)
		m["runs"] = append(runs, map[string]any{
			"id": "run-2", "ordinal": 2, "status": "waiting", "userMessageId": "msg-review-1",
		})
	})
	if settled := threadTurnSettledFromProjection(parsed, "msg-review-1"); settled {
		t.Fatal("ordinal 最大的活动 run 应判定未收尾")
	}
}

// TestArchiveRunsWaitsForTurnSettle 宽限内的 completed 运行：绑定 run 未收尾
// 时不归档、不写 archived_at；收尾后下个 tick 归档成功。
func TestArchiveRunsWaitsForTurnSettle(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 81, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	t3 := &fakeReviewT3{settled: map[string]bool{run.ThreadID: false}}
	review := e.withT3(t3, nil, nil)

	review.archiveRuns(now)

	if len(t3.archived) != 0 {
		t.Fatalf("run 未收尾不得归档，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt != nil {
		t.Fatal("run 未收尾不得写 archived_at")
	}
	if len(t3.settledCalls) != 1 || t3.settledCalls[0] != run.ThreadID+":"+run.InitialMessageID {
		t.Fatalf("settle 判定应按 threadID:initial_message_id 查一次，calls = %v", t3.settledCalls)
	}
	if len(review.worktreeTries) != 0 {
		t.Fatalf("收尾等待不得消耗归档重试计数，tries = %v", review.worktreeTries)
	}

	// 收尾后：归档成功并写 archived_at。
	t3.settled[run.ThreadID] = true
	review.archiveRuns(now)
	if len(t3.archived) != 1 || t3.archived[0] != run.ThreadID {
		t.Fatalf("收尾后应归档，archived = %v", t3.archived)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("归档成功应写 archived_at")
	}
}

// TestArchiveRunsSettleGraceExpiry finished_at 已超宽限：跳过收尾判定（不再
// 读投影）直接归档——宽限即上限，不能有永久不归档的运行。
func TestArchiveRunsSettleGraceExpiry(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 82, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	e.svc.db.Model(run).Update("finished_at", now.Add(-11*time.Minute))
	t3 := &fakeReviewT3{settled: map[string]bool{run.ThreadID: false}}
	review := e.withT3(t3, nil, nil)

	review.archiveRuns(now)

	if len(t3.settledCalls) != 0 {
		t.Fatalf("超宽限应跳过收尾判定，calls = %v", t3.settledCalls)
	}
	if len(t3.archived) != 1 || t3.archived[0] != run.ThreadID {
		t.Fatalf("超宽限应直接归档，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("归档成功应写 archived_at")
	}
}

// TestArchiveRunsNoFinishedAt finished_at 为空（防御分支）：跳过收尾判定
// 直接归档，不读投影。
func TestArchiveRunsNoFinishedAt(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 80, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	e.svc.db.Model(run).Update("finished_at", nil)
	t3 := &fakeReviewT3{settled: map[string]bool{run.ThreadID: false}}
	review := e.withT3(t3, nil, nil)

	review.archiveRuns(now)

	if len(t3.settledCalls) != 0 {
		t.Fatalf("finished_at 为空应跳过收尾判定，calls = %v", t3.settledCalls)
	}
	if len(t3.archived) != 1 || t3.archived[0] != run.ThreadID {
		t.Fatalf("finished_at 为空应直接归档，archived = %v", t3.archived)
	}
}

// TestArchiveRunsSettleThreadNotFound 宽限内线程 404 不立即采信（与
// cleanupRunWorktree 同口径，防 T3 重启/抖动误判）：连续到上限认定无可归
// 对象直接落 archived_at——不走归档 RPC（无线程的 RPC 只会误记归档失败、
// 发假告警）。中途读成功即清零连续计数。
func TestArchiveRunsSettleThreadNotFound(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 83, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	t3 := &fakeReviewT3{settledErr: errT3ThreadNotFound}
	review := e.withT3(t3, nil, nil)

	// 上限前：既不走归档 RPC 也不落 archived_at。
	for i := 0; i < reviewWorktreeMaxTries-1; i++ {
		review.archiveRuns(now)
	}
	if len(t3.archived) != 0 {
		t.Fatalf("404 未到上限不得走归档 RPC，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt != nil {
		t.Fatal("404 未到上限不得写 archived_at")
	}
	// 中途读成功：连续 404 计数清零，重新计满才放行直接落账。
	t3.settledErr = nil
	t3.settled = map[string]bool{run.ThreadID: false}
	review.archiveRuns(now)
	if review.worktreeTries[run.ID+"|"+worktreeTryNotFound] != 0 {
		t.Fatalf("读成功应清零连续 404 计数，tries = %v", review.worktreeTries)
	}
	t3.settledErr = errT3ThreadNotFound
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.archiveRuns(now)
	}
	if len(t3.archived) != 0 {
		t.Fatalf("404 到上限应直接落账而非走归档 RPC，archived = %v", t3.archived)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("404 到上限应直接写 archived_at")
	}
}

// TestArchiveRunsUnconfirmedWaitsSettle 启动结果未确认（dispatched_at 为空）
// 且绑定 run 未收尾：先停线程、不归档；收尾后再归档。
func TestArchiveRunsUnconfirmedWaitsSettle(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-settle")
	run := e.seedRun(t, repo, 84, "sha-s", model.ReviewRunTimedOut, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"environment_id": env.ID, "dispatch_started_at": now, "dispatched_at": nil,
		"finished_at": now,
	})
	t3 := &fakeReviewT3{settled: map[string]bool{run.ThreadID: false}}
	review := e.withT3(t3, nil, nil)

	review.archiveRuns(now)

	if len(t3.stopped) != 1 || t3.stopped[0] != run.ThreadID {
		t.Fatalf("未确认启动应先停线程，stopped = %v", t3.stopped)
	}
	if len(t3.archived) != 0 {
		t.Fatalf("run 未收尾不得归档，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt != nil {
		t.Fatal("run 未收尾不得写 archived_at")
	}

	// 收尾后：归档成功。
	t3.settled[run.ThreadID] = true
	review.archiveRuns(now)
	if len(t3.archived) != 1 || t3.archived[0] != run.ThreadID {
		t.Fatalf("收尾后应归档，archived = %v", t3.archived)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("归档成功应写 archived_at")
	}
}

// TestArchiveRunsArchiveErrorThreadGone 超宽限/finished_at 为空的行跳过收
// 尾判定直接归档；归档 RPC 失败归因可能是「线程已没了」（dispatchCommand
// 对不存在的线程只回普通上游错误）：补读投影归因，404 与窗口内同一份有界
// 确认——到上限直接落 archived_at，不计归档失败重试、不置 failed。
func TestArchiveRunsArchiveErrorThreadGone(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 86, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	e.svc.db.Model(run).Update("finished_at", nil) // 跳过收尾判定直接归档
	t3 := &fakeReviewT3{archiveErr: errors.New("upstream command failed"), settledErr: errT3ThreadNotFound}
	review := e.withT3(t3, nil, nil)

	// 上限前：归档每 tick 尝试、归因 404 只计连续计数，不落账不计归档失败。
	review.archiveRuns(now)
	if len(t3.archived) != 1 {
		t.Fatalf("超宽限应直接归档，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt != nil {
		t.Fatal("404 未到上限不得写 archived_at")
	}
	if review.worktreeTries[run.ID+"|"+worktreeTryArchive] != 0 {
		t.Fatal("无线程不得计入归档失败重试")
	}
	// 到上限：直接落账，不置 worktree_state=failed。
	for i := 1; i < reviewWorktreeMaxTries; i++ {
		review.archiveRuns(now)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("404 到上限应直接写 archived_at")
	}
	if after.WorktreeState == model.ReviewWorktreeFailed {
		t.Fatal("无可归对象不得置 worktree_state=failed")
	}
}

// TestArchiveRunsSettleReadError 暂时读不到收尾状态：本 tick 不归档、不占归
// 档重试计数；恢复后照常归档。
func TestArchiveRunsSettleReadError(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedDispatchedRun(t, repo, 85, model.ReviewKindCode, model.ReviewRunCompleted, "", false, now)
	t3 := &fakeReviewT3{settledErr: errors.New("projection down")}
	review := e.withT3(t3, nil, nil)

	review.archiveRuns(now)
	if len(t3.archived) != 0 {
		t.Fatalf("读不到收尾状态不得归档，archived = %v", t3.archived)
	}
	if len(review.worktreeTries) != 0 {
		t.Fatalf("投影读失败的等待不得消耗归档重试计数，tries = %v", review.worktreeTries)
	}

	t3.settledErr = nil
	review.archiveRuns(now)
	if len(t3.archived) != 1 || t3.archived[0] != run.ThreadID {
		t.Fatalf("恢复后应归档，archived = %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.ArchivedAt == nil {
		t.Fatal("归档成功应写 archived_at")
	}
}
