package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-170 两步回收（工作树 + 分支）与聚合通知的状态机测试：远程 T3 没有删
// 分支的 RPC，分支残留落 branch_pending 复查；删除结果不可见走待确认退避；
// 登记未读完不落 failed。回收失败通知按（环境, 仓库, 原因）聚合。

// seedBranchPendingRow 直接种一条 branch_pending 行（工作树已删、分支仍在）。
func seedBranchPendingRow(t *testing.T, e *reviewEngineEnv, repo string, pr int,
	env *model.T3Environment, branch string, now time.Time) *model.ReviewRun {
	t.Helper()
	run := e.seedCleanableRun(t, repo, pr, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_branch": branch,
		"worktree_state":  model.ReviewWorktreeBranchPending,
		"worktree_reason": worktreeReasonBranchPending,
		"worktree_error":  "工作树已删除，本轮专属分支 " + branch + " 仍存在",
	})
	run.WorktreeBranch, run.WorktreeState = branch, model.ReviewWorktreeBranchPending
	run.WorktreeReason = worktreeReasonBranchPending
	run.WorktreeError = "工作树已删除，本轮专属分支 " + branch + " 仍存在"
	return run
}

// TestCleanupBranchPendingEntryAndRecovery 工作树已不在登记 + 分支仍在 ref
// 全集 → branch_pending（通知）；复查分支消失 → removed 并刷新聚合通知
// （正文收缩为「受影响运行均已回收完成」）。
func TestCleanupBranchPendingEntryAndRecovery(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-bp")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 170, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/bp", Branch: "br-bp"},
		},
		// 工作树已不在登记（代理收尾删掉了），但分支 ref 还在——服务端
		// 只能核验，删不了。
		refNames: []string{"main", "br-bp"},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending ||
		after.WorktreeReason != worktreeReasonBranchPending {
		t.Fatalf("worktree = %q/%q, want branch_pending（分支仍在 ref 全集）",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "br-bp") ||
		!strings.Contains(after.WorktreeError, "无删除分支接口") {
		t.Fatalf("worktree_error = %q, 应写明分支与原因", after.WorktreeError)
	}
	if len(t3.removed) != 0 {
		t.Fatalf("工作树不在登记不得调删除，removed = %v", t3.removed)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("进入 branch_pending 应发一条聚合通知")
	}

	// 复查 #1：分支仍在 → 保持 branch_pending，排退避，不重复发通知。
	var row model.ReviewRun
	e.svc.db.First(&row, "id = ?", run.ID)
	review.cleanupRunWorktree(&row, now.Add(time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("分支仍在复查应保持 branch_pending, got %q", after.WorktreeState)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("复查分支仍在不应重发通知")
	}

	// 复查 #2（退避到点）：分支被代理删掉（ref 整条消失）→ removed + 通知
	// 收缩刷新。
	t3.refNames = []string{"main"}
	e.svc.db.First(&row, "id = ?", run.ID)
	review.cleanupRunWorktree(&row, now.Add(31*time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved ||
		after.WorktreeReason != "" || after.WorktreeError != "" {
		t.Fatalf("分支消失后 = %q/%q/%q, want removed 且清账",
			after.WorktreeState, after.WorktreeReason, after.WorktreeError)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("恢复刷新应复用同一 dedup 键，不新增通知行")
	}
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, "受影响运行均已回收完成") {
		t.Fatalf("恢复后通知正文应收缩，got %q", note.Body)
	}
}

// TestCleanupBothStepsGoneRemoved 工作树与分支都已不在登记：一步到位 removed，
// 不通知、不调删除。
func TestCleanupBothStepsGoneRemoved(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-gone2")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 171, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/gone", Branch: "br-gone"},
		},
		refNames: []string{"main"}, // 只剩主干：工作树与本轮分支都没了
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || after.WorktreeReason != "" {
		t.Fatalf("worktree = %q/%q, want removed 且无原因", after.WorktreeState, after.WorktreeReason)
	}
	if len(t3.removed) != 0 {
		t.Fatalf("登记无此工作树不得调删除, removed = %v", t3.removed)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("两步都消失不应通知")
	}
}

// TestCleanupRemoveUncertainThenConfirmed 删除结果不可见：先保持 none +
// remove_uncertain 挂退避（不通知）；退避到点后重发删除、复查发现远端实际
// 已删（分支为空走路径核验）→ removed。
func TestCleanupRemoveUncertainThenConfirmed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 172, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/unc", Branch: ""}, // 分支空：只凭路径核验
		},
		worktrees:       []T3Worktree{{Path: "/wt/unc"}},
		removeUncertain: true,
	}
	review := e.withT3(t3, nil, nil)

	// 第 1 tick：结果不可见 → 保持 none + 待确认，不发通知。
	review.cleanupRunWorktree(run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeNone ||
		after.WorktreeReason != worktreeReasonRemoveUncertain {
		t.Fatalf("第 1 tick = %q/%q, want none/remove_uncertain（不直接置 failed）",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "待确认") {
		t.Fatalf("worktree_error = %q, 应写明待确认", after.WorktreeError)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("单次待确认不应发通知")
	}

	// 第 2 tick（退避到点）：这次远端实际删掉了（复查发现已不在）→
	// 工作树步完成，分支空 → removed。
	t3.removeUncertainGone = true
	var row model.ReviewRun
	e.svc.db.First(&row, "id = ?", run.ID)
	review.cleanupRunWorktree(&row, now.Add(31*time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || after.WorktreeReason != "" {
		t.Fatalf("复查已删 = %q/%q, want removed 且清账", after.WorktreeState, after.WorktreeReason)
	}
	if len(t3.removed) != 2 {
		t.Fatalf("应重发一次删除再确认, removed = %v", t3.removed)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("确认完成不应发失败通知")
	}
}

// TestCleanupRemoveUncertainCapsAtFailed 删除结果连续 3 次不可见：failed +
// 聚合通知（远端状态请人工确认），不再无限重试。
func TestCleanupRemoveUncertainCapsAtFailed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unccap")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 173, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/unc2", Branch: ""},
		},
		worktrees:       []T3Worktree{{Path: "/wt/unc2"}},
		removeUncertain: true,
	}
	review := e.withT3(t3, nil, nil)

	// 三轮「不可见」之间按退避推进时钟；每轮都重过线程读取、登记读取与
	// 归属核验（重发删除前的安全前提）。
	at := now
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		var row model.ReviewRun
		e.svc.db.First(&row, "id = ?", run.ID)
		review.cleanupRunWorktree(&row, at)
		if i < reviewWorktreeMaxTries-1 {
			at = at.Add(91 * time.Second) // 覆盖 30s/60s 退避
		}
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonRemoveUncertain {
		t.Fatalf("worktree = %q/%q, want failed/remove_uncertain（连续 3 次到限）",
			after.WorktreeState, after.WorktreeReason)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("到限应发一条聚合通知")
	}
}

// TestCleanupRemoveDefiniteErrorFails 删除被远端明确拒绝（Exit Failure）且
// 复查登记仍在：failed(worktree_remove) + 通知。
func TestCleanupRemoveDefiniteErrorFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-rmerr")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 174, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/err", Branch: ""},
		},
		worktrees: []T3Worktree{{Path: "/wt/err"}},
		removeErr: errors.New("upstream command failed"),
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonWorktreeRemove {
		t.Fatalf("worktree = %q/%q, want failed/worktree_remove",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "删除工作区失败") {
		t.Fatalf("worktree_error = %q, 应含删除失败原因", after.WorktreeError)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("确定失败应发通知")
	}
}

// TestCleanupRegistryIncompleteStaysNone 登记未在预算内读完：不落 failed，
// 保持 none + registry_incomplete 挂退避；连续 3 次起发聚合通知；登记读成功
// 后原因清除、通知收缩（正文改为「受影响运行均已回收完成」）。
func TestCleanupRegistryIncompleteStaysNone(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-inc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 175, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/inc", Branch: ""},
		},
		listIncomplete: true,
	}
	review := e.withT3(t3, nil, nil)

	// 三轮「不完整」按退避推进；前两轮不发通知，第三轮起发。
	at := now
	readsBetween := 0
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		readsBetween = t3.threadWTCallsLen()
		var row model.ReviewRun
		e.svc.db.First(&row, "id = ?", run.ID)
		review.cleanupRunWorktree(&row, at)
		if got := t3.threadWTCallsLen(); got != readsBetween+1 {
			t.Fatalf("第 %d 轮未重读线程投影（退避未放行）", i+1)
		}
		if i < reviewWorktreeMaxTries-1 {
			at = at.Add(91 * time.Second)
		}
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeNone ||
		after.WorktreeReason != worktreeReasonRegistryIncomplete {
		t.Fatalf("worktree = %q/%q, want none/registry_incomplete（不落 failed）",
			after.WorktreeState, after.WorktreeReason)
	}
	if len(t3.removed) != 0 {
		t.Fatal("登记不完整不得删除")
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("连续 3 次应发一条聚合通知")
	}

	// 恢复：登记读得完整了 → 原因清除、删除照常、通知收缩。（第三轮排的
	// 退避是 30s<<2=120s，恢复时钟要越过它。）
	t3.listIncomplete = false
	t3.worktrees = []T3Worktree{{Path: "/wt/inc"}}
	var row model.ReviewRun
	e.svc.db.First(&row, "id = ?", run.ID)
	review.cleanupRunWorktree(&row, at.Add(121*time.Second))
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || after.WorktreeReason != "" {
		t.Fatalf("恢复后 = %q/%q, want removed 且清账", after.WorktreeState, after.WorktreeReason)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("恢复刷新应复用同一 dedup 键")
	}
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, "受影响运行均已回收完成") {
		t.Fatalf("恢复后通知正文应收缩，got %q", note.Body)
	}
}

// TestCleanupThreadNotFoundContinuesFromRowSnapshot 线程 404 连续到上限、
// 行内已有 path/branch 快照：改用行内快照继续走登记核验并删除（线程没了
// 工作区与分支可能还在），分支仍在 → branch_pending。
func TestCleanupThreadNotFoundContinuesFromRowSnapshot(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-404snap")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 176, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_path": "/wt/snap", "worktree_branch": "br-snap",
	})
	run.WorktreePath, run.WorktreeBranch = "/wt/snap", "br-snap"
	t3 := &fakeReviewT3{
		threadWTErr: errT3ThreadNotFound,
		worktrees:   []T3Worktree{{Path: "/wt/snap", RefNames: []string{"br-snap"}}},
		refNames:    []string{"br-snap"},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending || len(t3.removed) != 1 ||
		t3.removed[0] != "/wt/snap" {
		t.Fatalf("worktree = %q removed=%v, want branch_pending 且按行内快照删除",
			after.WorktreeState, t3.removed)
	}
}

// TestCleanupRowWithoutBranchRemoved 行内分支为空（线程投影 branch 空）：
// 工作树删除后无分支可判 → 旧行为 removed，不通知。
func TestCleanupRowWithoutBranchRemoved(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-nobr2")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 177, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/nb", Branch: ""},
		},
		worktrees: []T3Worktree{{Path: "/wt/nb", RefNames: []string{"whatever"}}},
		refNames:  []string{"main"},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || len(t3.removed) != 1 {
		t.Fatalf("worktree = %q removed=%v, want removed 且删除（无分支即两步完成）",
			after.WorktreeState, t3.removed)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("无分支不应通知")
	}
}

// TestCleanupEmptyPathWithRowBranchGoesBranchStep 线程投影没有工作树路径
// 但行内有分支快照：到空路径上限后跳过工作树步直接判分支，分支在登记
// ref 全集 → branch_pending。
func TestCleanupEmptyPathWithRowBranchGoesBranchStep(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-emptypath")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 178, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Update("worktree_branch", "br-only")
	run.WorktreeBranch = "br-only"
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "", Branch: ""}, // root 工作区形态
		},
		refNames: []string{"main", "br-only"},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("worktree = %q, want branch_pending（无路径但分支仍在）", after.WorktreeState)
	}
	if len(t3.removed) != 0 || t3.listCalls == 0 {
		t.Fatalf("无路径不得删除但应读登记, removed=%v listCalls=%d", t3.removed, t3.listCalls)
	}
}

// TestCleanupOwnershipMismatchReason 行内快照与线程真值冲突：failed 且
// reason=ownership_mismatch，不删。
func TestCleanupOwnershipMismatchReason(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-mis2")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 179, env, model.ReviewRunCompleted, "/wt/old", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/new", Branch: "br-n"},
		},
		worktrees: []T3Worktree{{Path: "/wt/new", RefNames: []string{"br-n"}}},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonOwnershipMismatch || len(t3.removed) != 0 {
		t.Fatalf("worktree = %q/%q removed=%v, want failed/ownership_mismatch 且不删",
			after.WorktreeState, after.WorktreeReason, t3.removed)
	}
}

// TestCleanupAggregateBatchesRunsByReason 同（环境, 仓库, 原因）的两条运行
// 先后进入失败态：只落一条通知，正文逐行列出两条受影响运行。
func TestCleanupAggregateBatchesRunsByReason(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-agg")
	// 故意不建绑定：worktreeCwdFor 拿不到根目录，两条都落 env_binding。

	r1 := e.seedCleanableRun(t, repo, 180, env, model.ReviewRunCompleted, "", now)
	r2 := e.seedCleanableRun(t, repo, 181, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			r1.ThreadID: {Path: "/wt/a1", Branch: "br-a1"},
			r2.ThreadID: {Path: "/wt/a2", Branch: "br-a2"},
		},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(r1, now)
	review.cleanupRunWorktree(r2, now)

	for _, r := range []*model.ReviewRun{r1, r2} {
		var after model.ReviewRun
		e.svc.db.First(&after, "id = ?", r.ID)
		if after.WorktreeState != model.ReviewWorktreeFailed ||
			after.WorktreeReason != worktreeReasonEnvBinding {
			t.Fatalf("run %s = %q/%q, want failed/env_binding", r.ID, after.WorktreeState, after.WorktreeReason)
		}
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatalf("同键两条运行应聚合为一条通知")
	}
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Title, "PR 审查工作区回收失败") ||
		!strings.Contains(note.Title, repo) {
		t.Fatalf("通知标题 = %q, 应含失败标题与仓库", note.Title)
	}
	if !strings.Contains(note.Body, "环境绑定缺失") {
		t.Fatalf("通知正文 = %q, 应先写原因说明", note.Body)
	}
	for _, want := range []string{"PR #180", "PR #181"} {
		if !strings.Contains(note.Body, want) {
			t.Fatalf("通知正文 = %q, 应逐行列出 %s", note.Body, want)
		}
	}
}

// threadWTCallsLen 读取桩的线程投影调用次数（避免直接触碰切片头）。
func (f *fakeReviewT3) threadWTCallsLen() int { return len(f.threadWTCalls) }
