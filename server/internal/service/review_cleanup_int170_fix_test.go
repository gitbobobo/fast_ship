package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

// INT-170 修复轮的行为钉子：调度公平性（F2）、NULL 快照兼容（F3）、投影空
// 分支回落（F4）、未发布键不造恢复通知（F6）、未送达删除（F7）、拨号预算
// 耗尽归待确认（F8）、退避恢复重爬梯（F9）、聚合清单边界（F11）。

// newCleanupTestT3Threads 在审查引擎测试库上组装一个真 T3ThreadsService
// （秒级预算收紧），供 ListRegistry 的拨号分类测试用。
func newCleanupTestT3Threads(t *testing.T, e *reviewEngineEnv) *T3ThreadsService {
	t.Helper()
	envRepo := repository.NewT3EnvironmentRepository(e.svc.db)
	hub := NewT3ShellHub(zap.NewNop())
	t.Cleanup(hub.Close)
	th := NewT3ThreadsService(envRepo, e.svc.cfg, zap.NewNop(), hub)
	th.budgets = t3RemoteBudgets{ThreadDetail: 500 * time.Millisecond}
	return th
}

// TestCleanupWorktreesSkipsNotDueAndStillReclaims（F2）调度公平性：20 条常驻
// branch_pending 占满一轮批量后，普通 none 行下一轮仍被处理（不再饿死）；
// 未到退避点的 branch_pending 行整轮跳过——不涨 listCalls 也不占名额。
func TestCleanupWorktreesSkipsNotDueAndStillReclaims(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-fair")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// 20 条常驻 branch_pending（finished_at 更老，排在前）：分支仍在登记 ref
	// 全集，复查只会续排退避。
	var refNames []string
	for i := 0; i < reviewStageBatch; i++ {
		branch := fmt.Sprintf("br-pend-%d", i)
		seedBranchPendingRow(t, e, repo, 200+i, env, branch, now.Add(-time.Duration(i+1)*time.Minute))
		refNames = append(refNames, branch)
	}
	// 第 21 条：普通 none 可回收行（最新，排在批量窗口之外）。
	fresh := e.seedCleanableRun(t, repo, 299, env, model.ReviewRunCompleted, "", now)
	freshBranch := "br-fair"
	refNames = append(refNames, freshBranch)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			fresh.ThreadID: {Path: "/wt/fair", Branch: freshBranch},
		},
		worktrees: []T3Worktree{{Path: "/wt/fair", RefNames: []string{freshBranch}}},
		refNames:  refNames,
	}
	review := e.withT3(t3, nil, nil)

	// 第 1 轮：20 条 pending 全部到期（尚无退避排程），恰好占满批量名额；
	// 第 21 条留在窗口外。
	review.cleanupWorktrees(now)
	if t3.listCalls != reviewStageBatch {
		t.Fatalf("第 1 轮 listCalls = %d, want %d（20 条 pending 各读一次）", t3.listCalls, reviewStageBatch)
	}
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", fresh.ID)
	if mid.WorktreeState != model.ReviewWorktreeNone {
		t.Fatalf("第 21 条在第 1 轮 = %q, want none（批量名额被 pending 占满）", mid.WorktreeState)
	}

	// 第 2 轮：pending 未到退避点（30s 后才到期）→ 整行跳过，不读登记；
	// 第 21 条被处理（不再饿死）。
	listCallsBefore := t3.listCalls
	review.cleanupWorktrees(now.Add(time.Second))
	if t3.listCalls != listCallsBefore+1 {
		t.Fatalf("第 2 轮 listCalls 增量 = %d, want 1（未到期行不读登记）",
			t3.listCalls-listCallsBefore)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", fresh.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("第 2 轮第 21 条 = %q, want branch_pending（不再被常驻行饿死）", after.WorktreeState)
	}
	if len(t3.removed) != 1 || t3.removed[0] != "/wt/fair" {
		t.Fatalf("removed = %v, want 删除一次 /wt/fair", t3.removed)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("同键 21 条 branch_pending 应聚合为一条通知")
	}
}

// TestWorktreeBackoffCapPerReason（F2）退避封顶按原因给：branch_pending 长期
// 等外部动作，爬到 1h 封顶；其余待确认维持 5min；清退避后从起步间隔重爬。
func TestWorktreeBackoffCapPerReason(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.review
	at := time.Now().UTC()

	for i := 0; i < 8; i++ {
		review.scheduleWorktreeBackoff("run-bp", worktreeReasonBranchPending, at)
	}
	if got := review.worktreeBackoff["run-bp|"+worktreeReasonBranchPending].nextAt.Sub(at); got != worktreeBackoffCapBranchPending {
		t.Fatalf("branch_pending 退避封顶 = %v, want %v", got, worktreeBackoffCapBranchPending)
	}
	for i := 0; i < 8; i++ {
		review.scheduleWorktreeBackoff("run-ri", worktreeReasonRegistryIncomplete, at)
	}
	if got := review.worktreeBackoff["run-ri|"+worktreeReasonRegistryIncomplete].nextAt.Sub(at); got != worktreeBackoffCap {
		t.Fatalf("registry_incomplete 退避封顶 = %v, want %v", got, worktreeBackoffCap)
	}
	review.clearWorktreeBackoff("run-bp", worktreeReasonBranchPending)
	review.scheduleWorktreeBackoff("run-bp", worktreeReasonBranchPending, at)
	if got := review.worktreeBackoff["run-bp|"+worktreeReasonBranchPending].nextAt.Sub(at); got != worktreeBackoffBase {
		t.Fatalf("清退避后首档 = %v, want %v", got, worktreeBackoffBase)
	}
}

// TestCleanupBackoffRestartsFromBaseAfterRecovery（F9）恢复后再入同一待确认
// 态：退避从起步间隔重爬，不接着半截阶梯跳档。
func TestCleanupBackoffRestartsFromBaseAfterRecovery(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-breset")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 210, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_state":  model.ReviewWorktreeNone,
		"worktree_reason": worktreeReasonRemoveUncertain,
		"worktree_error":  "删除工作区结果待确认（超时或连接中断），下轮复查远端状态",
	})
	run.WorktreeReason = worktreeReasonRemoveUncertain
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/breset", Branch: ""},
		},
		worktrees:       []T3Worktree{{Path: "/wt/breset"}},
		removeUncertain: true,
	}
	review := e.withT3(t3, nil, nil)
	// 预置半截阶梯（attempts=3 → 若不清再排就是 240s）。
	review.worktreeMu.Lock()
	review.worktreeBackoff[run.ID+"|"+worktreeReasonRemoveUncertain] =
		&worktreeBackoffEntry{nextAt: now, attempts: 3}
	review.worktreeMu.Unlock()

	review.cleanupRunWorktree(run, now)

	review.worktreeMu.Lock()
	entry := review.worktreeBackoff[run.ID+"|"+worktreeReasonRemoveUncertain]
	review.worktreeMu.Unlock()
	if entry == nil {
		t.Fatal("重入待确认态应重新排退避")
	}
	if got := entry.nextAt.Sub(now); got != worktreeBackoffBase {
		t.Fatalf("恢复后退避首档 = %v, want %v（不接半截阶梯）", got, worktreeBackoffBase)
	}
}

// TestCleanupNullBranchSnapshotBackfilledBeforePending（F3）旧库 NULL 行：
// worktree_branch 是新增可空列，等值条件匹配不到 NULL 会静默写不进 → 误进
// branch_pending → 下轮 HasRef("")=false 误收 removed。NULL 行必须能补写成功，
// 且快照落库后才进入 branch_pending。
func TestCleanupNullBranchSnapshotBackfilledBeforePending(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-nullbr")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 211, env, model.ReviewRunCompleted, "", now)
	if err := e.svc.db.Exec("UPDATE review_runs SET worktree_branch = NULL WHERE id = ?", run.ID).Error; err != nil {
		t.Fatalf("seed NULL branch: %v", err)
	}
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/nullb", Branch: "br-nullb"},
		},
		worktrees: []T3Worktree{{Path: "/wt/nullb", RefNames: []string{"br-nullb"}}},
		refNames:  []string{"br-nullb"},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("worktree_state = %q, want branch_pending", after.WorktreeState)
	}
	if after.WorktreeBranch != "br-nullb" {
		t.Fatalf("worktree_branch = %q, want br-nullb（NULL 行必须补写成功）", after.WorktreeBranch)
	}
	if len(t3.removed) != 1 || t3.removed[0] != "/wt/nullb" {
		t.Fatalf("removed = %v, want 删除一次", t3.removed)
	}
}

// TestCleanupEmptyThreadBranchFallsBackToRowSnapshot（F4）投影 branch 为空
// （重读丢字段）不再按冲突判死：回落行内快照继续流程；两边非空且不等仍判
// ownership_mismatch。
func TestCleanupEmptyThreadBranchFallsBackToRowSnapshot(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-fb")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// 投影 branch 空 + 行内有快照：回落快照 → 核验/删除/分支步照常走。
	runA := e.seedCleanableRun(t, repo, 212, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(runA).Update("worktree_branch", "br-rowfb")
	runA.WorktreeBranch = "br-rowfb"
	// 两边非空且不等：仍是归属冲突，不删（同 fixture 顺带钉住）。
	runB := e.seedCleanableRun(t, repo, 213, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(runB).Update("worktree_branch", "br-a")
	runB.WorktreeBranch = "br-a"
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			runA.ThreadID: {Path: "/wt/fb", Branch: ""}, // 投影丢分支字段
			runB.ThreadID: {Path: "/wt/xx", Branch: "br-b"},
		},
		worktrees: []T3Worktree{
			{Path: "/wt/fb", RefNames: []string{"br-rowfb"}},
			{Path: "/wt/xx", RefNames: []string{"br-b"}},
		},
		refNames: []string{"br-rowfb", "br-b"},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(runA, now)
	var afterA model.ReviewRun
	e.svc.db.First(&afterA, "id = ?", runA.ID)
	if afterA.WorktreeState != model.ReviewWorktreeBranchPending || len(t3.removed) != 1 {
		t.Fatalf("回落快照 = %q removed=%v, want branch_pending 且已删除（空投影不判冲突）",
			afterA.WorktreeState, t3.removed)
	}

	// 两边非空且不等：仍是归属冲突，不删。
	t3.threadWorktrees[runB.ThreadID] = T3ThreadWorktree{Path: "/wt/xx", Branch: "br-b"}
	removedBefore := len(t3.removed)
	review.cleanupRunWorktree(runB, now)
	var afterB model.ReviewRun
	e.svc.db.First(&afterB, "id = ?", runB.ID)
	if afterB.WorktreeState != model.ReviewWorktreeFailed ||
		afterB.WorktreeReason != worktreeReasonOwnershipMismatch || len(t3.removed) != removedBefore {
		t.Fatalf("两边非空冲突 = %q/%q removed=%v, want failed/ownership_mismatch 且不删",
			afterB.WorktreeState, afterB.WorktreeReason, t3.removed)
	}
}

// TestCleanupRecoverySkipsUnpublishedKey（F6）恢复刷新对从未发布过的键不得
// 凭空造「均已回收完成」通知；键已存在时照常收缩正文、不新增行。
func TestCleanupRecoverySkipsUnpublishedKey(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-skipkey")

	// 键不存在 + 受影响清单为空：纯恢复刷新 → 跳过，不建通知。
	r1 := e.seedCleanableRun(t, repo, 214, env, model.ReviewRunCompleted, "", now)
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.publishCleanupAggregate(r1, worktreeReasonRemoveUncertain, now)
	if got := e.cleanupFailedCount(t); got != 0 {
		t.Fatalf("未发布过键的恢复刷新建了 %d 条通知, want 0", got)
	}

	// 键存在 + 清单收缩：正常发布更新，不新增行。
	r2 := e.seedCleanableRun(t, repo, 215, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(r2).Updates(map[string]any{
		"worktree_state":  model.ReviewWorktreeNone,
		"worktree_reason": worktreeReasonRemoveUncertain,
		"worktree_error":  "删除工作区结果待确认",
	})
	review.publishCleanupAggregate(r2, worktreeReasonRemoveUncertain, now)
	if got := e.cleanupFailedCount(t); got != 1 {
		t.Fatalf("有受影响清单应建通知, got %d", got)
	}
	e.svc.db.Model(r2).Updates(map[string]any{
		"worktree_state":  model.ReviewWorktreeRemoved,
		"worktree_reason": "", "worktree_error": "",
	})
	review.publishCleanupAggregate(r2, worktreeReasonRemoveUncertain, now)
	if got := e.cleanupFailedCount(t); got != 1 {
		t.Fatalf("恢复刷新应复用同一行, got %d", got)
	}
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, "受影响运行均已回收完成") {
		t.Fatalf("恢复后正文应收缩, got %q", note.Body)
	}
}

// TestCleanupRemoveNotAttemptedGoesPendingThenConverges（F7 + 复审16轮 P2）
// 删除请求未送达（环境/连接不可用，T3 停机）：远端必然没执行——不再有限
// 三次快速判败（秒级停机会被放大成批量假告警），与登记侧同口径挂待确认
// 退避（remove_unavailable + 独立 remove-attempt 计数键），未到点整行跳
// 过、连续到上限才收敛 failed，终态文案写明未送达而非「被远端明确拒绝」。
func TestCleanupRemoveNotAttemptedGoesPendingThenConverges(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-notatt")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 216, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/notatt", Branch: ""},
		},
		worktrees:          []T3Worktree{{Path: "/wt/notatt"}},
		removeNotAttempted: true,
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.WorktreeState != model.ReviewWorktreeNone ||
		mid.WorktreeReason != worktreeReasonRemoveUnavailable {
		t.Fatalf("未送达应保持 none + remove_unavailable, got %q/%q",
			mid.WorktreeState, mid.WorktreeReason)
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRemoveAttempt]; got != 1 {
		t.Fatalf("remove-attempt 计数应记 1, got %d", got)
	}
	// 退避闸门：未到点的 tick 整行跳过——不再每 10s 重发删除+重读大登记。
	review.cleanupRunWorktree(run, now.Add(10*time.Second))
	if len(t3.removed) != 1 || t3.listCalls != 1 {
		t.Fatalf("退避窗口内不得重试, removed=%d listCalls=%d", len(t3.removed), t3.listCalls)
	}

	at := now.Add(6 * time.Minute) // 越过退避（重试类封顶 5min）
	for i := 1; i < reviewBudgetPendingMaxTries; i++ {
		review.cleanupRunWorktree(run, at)
		at = at.Add(6 * time.Minute)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonWorktreeRemove {
		t.Fatalf("连续未送达到上限应收 failed/worktree_remove, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "未能送达") {
		t.Fatalf("worktree_error = %q, 应写明未送达而非远端拒绝", after.WorktreeError)
	}
	if len(t3.removed) != reviewBudgetPendingMaxTries || t3.listCalls != reviewBudgetPendingMaxTries {
		t.Fatalf("每个到点 tick 应各读一次登记+试一次删除, removed=%d listCalls=%d",
			len(t3.removed), t3.listCalls)
	}
	// 通知两行：待确认键（tries≥3 起发）与终态 worktree_remove 键各占一
	// 条；待确认键在终态转移时被收缩重发，不新增行。
	if e.cleanupFailedCount(t) != 2 {
		t.Fatalf("应各有一条待确认与终态聚合通知, got %d", e.cleanupFailedCount(t))
	}
}

// TestListRegistryDialBudgetExhaustedIsIncomplete（F8）拨号把预算耗到点：
// 与翻页中途的预算耗尽同一「待确认」语义（errRegistryIncomplete），不再记
// 永久失败；预算内的明确失败（连接被拒）仍按上游错误。
func TestListRegistryDialBudgetExhaustedIsIncomplete(t *testing.T) {
	e := newReviewEngineEnv(t)
	th := newCleanupTestT3Threads(t, e)

	// 控制组：连接被立刻拒绝（预算未耗）→ 上游错误，不是 incomplete。
	refused := createT3TestEnv(t, e.svc.db, e.user.ID, "env-dial-refused")
	_, err := th.ListRegistry(context.Background(), e.user.ID, refused.ID, "/ws")
	if err == nil {
		t.Fatal("不可达环境应返回错误")
	}
	if errors.Is(err, errRegistryIncomplete) {
		t.Fatalf("预算内的明确失败应按上游错误, got %v", err)
	}

	// 挂起端：TCP 可连（listen 未 accept，内核背板兜住）但握手永无响应 →
	// 预算耗到点 → incomplete。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	hang := createT3TestEnv(t, e.svc.db, e.user.ID, "env-dial-hang")
	if err := e.svc.db.Model(hang).Update("address", "http://"+ln.Addr().String()+"/").Error; err != nil {
		t.Fatalf("update address: %v", err)
	}
	_, err = th.ListRegistry(context.Background(), e.user.ID, hang.ID, "/ws")
	if !errors.Is(err, errRegistryIncomplete) {
		t.Fatalf("拨号耗尽预算应归待确认, got %v", err)
	}
}

// TestCleanupAffectedLineLabelsByReason（F11）清单行标签按原因给：待确认只
// 用于真正等复查的原因；恒 none 的 archive_failed（视觉运行）给「已放弃」；
// 其余 none 给「未完成」；空错误不渲染孤立的「· 」尾巴。
func TestCleanupAffectedLineLabelsByReason(t *testing.T) {
	mk := func(state model.ReviewWorktreeState, reason, errText string) *model.ReviewRun {
		return &model.ReviewRun{
			ID: "abcd1234efgh", PRNumber: 7,
			WorktreeState: state, WorktreeReason: reason, WorktreeError: errText,
		}
	}
	cases := []struct {
		name string
		run  *model.ReviewRun
		want string
	}{
		{"登记待确认", mk(model.ReviewWorktreeNone, worktreeReasonRegistryIncomplete, ""),
			"PR #7 · 运行 abcd1234 · 待确认复查"},
		{"删除待确认", mk(model.ReviewWorktreeNone, worktreeReasonRemoveUncertain, "超时"),
			"PR #7 · 运行 abcd1234 · 待确认复查 · 超时"},
		{"归档放弃", mk(model.ReviewWorktreeNone, worktreeReasonArchiveFailed, ""),
			"PR #7 · 运行 abcd1234 · 已放弃"},
		{"其余未完成", mk(model.ReviewWorktreeNone, worktreeReasonThreadRead, ""),
			"PR #7 · 运行 abcd1234 · 未完成"},
		{"分支待清理", mk(model.ReviewWorktreeBranchPending, worktreeReasonBranchPending, ""),
			"PR #7 · 运行 abcd1234 · 分支待清理"},
		{"失败带原因", mk(model.ReviewWorktreeFailed, worktreeReasonEnvBinding, "绑定缺失"),
			"PR #7 · 运行 abcd1234 · 失败 · 绑定缺失"},
	}
	for _, tc := range cases {
		if got := cleanupAffectedLine(tc.run); got != tc.want {
			t.Fatalf("%s: line = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCleanupAggregateTruncatesAtLimitPlusOne（F11）清单截断按 limit+1 探测：
// 恰好 reviewStageBatch 条不算截断；第 21 条起截断并保留最老的 20 条。
func TestCleanupAggregateTruncatesAtLimitPlusOne(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-trunc")

	seed := func(pr int, age time.Duration) *model.ReviewRun {
		run := e.seedCleanableRun(t, repo, pr, env, model.ReviewRunCompleted, "", now.Add(-age))
		e.svc.db.Model(run).Updates(map[string]any{
			"worktree_state":  model.ReviewWorktreeNone,
			"worktree_reason": worktreeReasonRegistryIncomplete,
			"worktree_error":  fmt.Sprintf("第 %d 号", pr),
		})
		return run
	}
	// 最老的 20 条（清单按 finished_at ASC 全保留）。
	for i := 0; i < reviewStageBatch; i++ {
		seed(300+i, time.Duration(i+1)*time.Minute)
	}
	// 发布入参只提供环境/仓库/用户身份，自身不带该原因（不进清单）。
	head := e.seedCleanableRun(t, repo, 400, env, model.ReviewRunCompleted, "", now)

	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.publishCleanupAggregate(head, worktreeReasonRegistryIncomplete, now)
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if strings.Contains(note.Body, "清单截断") {
		t.Fatalf("恰好 %d 条不应截断, got %q", reviewStageBatch, note.Body)
	}
	if !strings.Contains(note.Body, "PR #319") {
		t.Fatalf("最老一条应保留在清单, got %q", note.Body)
	}

	// 第 21 条（最新）：截断标记出现，最老的 20 条保留、最新的被截掉。
	seed(320, 0)
	review.publishCleanupAggregate(head, worktreeReasonRegistryIncomplete, now)
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, "…（清单截断）") {
		t.Fatalf("超过 %d 条应写截断标记, got %q", reviewStageBatch, note.Body)
	}
	if !strings.Contains(note.Body, "PR #319") || strings.Contains(note.Body, "PR #320") {
		t.Fatalf("截断应保留最老的 %d 条: got %q", reviewStageBatch, note.Body)
	}
}

// TestCleanupRegistryCounterResetsOnSuccess（复审 P1）登记错误计数按「连续
// 失败」计：中途一次成功读必须复位，否则 branch_pending 这类生命周期无
// 上限的行会被跨天瞬时抖动累计成永久 failed。
func TestCleanupRegistryCounterResetsOnSuccess(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-rst")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 500, env, "br-rst", now)
	t3 := &fakeReviewT3{
		listErr:  errors.New("registry flaky"),
		refNames: []string{"br-rst"},
	}
	review := e.withT3(t3, nil, nil)

	// 两次瞬时登记错误：计数涨到 2。
	review.cleanupRunWorktree(run, now)
	review.cleanupRunWorktree(run, now.Add(2*time.Hour))
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRegistry]; got != 2 {
		t.Fatalf("两次错误后 registry 计数 = %d, want 2", got)
	}

	// 登记读成功（分支仍在，保持 pending）：计数必须复位。
	t3.listErr = nil
	review.cleanupRunWorktree(run, now.Add(4*time.Hour))
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRegistry]; got != 0 {
		t.Fatalf("登记读成功后 registry 计数 = %d, want 0（连续失败语义）", got)
	}
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("成功后状态 = %q, want branch_pending", mid.WorktreeState)
	}

	// 再一次瞬时错误：只算 1 次连续失败，不得 failed。
	t3.listErr = errors.New("registry flaky again")
	review.cleanupRunWorktree(run, now.Add(6*time.Hour))
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState == model.ReviewWorktreeFailed {
		t.Fatalf("累计语义把瞬时抖动误判成永久失败, reason = %q", after.WorktreeReason)
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRegistry]; got != 1 {
		t.Fatalf("恢复后又一次错误计数 = %d, want 1", got)
	}
}

// TestCleanupRecoverUncertainPublishesPriorKey（复审 P2）remove_uncertain 行
// 恢复时旧原因键的聚合通知必须收缩：reason 在 recoverFromPending 已清，
// 不在这里重发旧键，清单会永远滞留本运行。
func TestCleanupRecoverUncertainPublishesPriorKey(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// A：已 failed 的 remove_uncertain 行（通知来源）；B：恢复中的行。
	runA := e.seedCleanableRun(t, repo, 510, env, model.ReviewRunFailed, "/wt/a", now)
	e.svc.db.Model(runA).Updates(map[string]any{
		"worktree_state":  model.ReviewWorktreeFailed,
		"worktree_reason": worktreeReasonRemoveUncertain,
		"worktree_error":  "删除结果不可见",
	})
	runB := e.seedCleanableRun(t, repo, 511, env, model.ReviewRunFailed, "/wt/b", now)
	e.svc.db.Model(runB).Updates(map[string]any{
		"worktree_reason": worktreeReasonRemoveUncertain,
		"worktree_error":  "删除结果不可见",
	})
	runB.WorktreeReason = worktreeReasonRemoveUncertain

	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			runB.ThreadID: {Path: "/wt/b", Branch: ""},
		},
		worktrees: []T3Worktree{{Path: "/wt/b", RefNames: []string{"br-b"}}},
	}
	review := e.withT3(t3, nil, nil)

	// 既有通知：清单含 A、B 两行（A 的 failed 触发了聚合发布）。
	review.publishCleanupAggregate(runA, worktreeReasonRemoveUncertain, now)
	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, runA.ID[:8]) || !strings.Contains(note.Body, runB.ID[:8]) {
		t.Fatalf("恢复前清单应含 A、B: %q", note.Body)
	}

	// B 恢复：登记读成功 → recoverFromPending 清 reason 并重发旧键；随后
	// 工作树步删除、登记恢复分支名 br-b → branch_pending。
	review.cleanupRunWorktree(runB, now)
	var afterB model.ReviewRun
	e.svc.db.First(&afterB, "id = ?", runB.ID)
	if afterB.WorktreeState != model.ReviewWorktreeBranchPending ||
		afterB.WorktreeBranch != "br-b" {
		t.Fatalf("B 恢复后状态 = %q/%q, want branch_pending 且恢复分支名",
			afterB.WorktreeState, afterB.WorktreeBranch)
	}
	// 现在有两条通知（uncertain 键 + 新 branch_pending 键），按创建序取旧键。
	e.svc.db.Order("created_at ASC").First(&note,
		"kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if strings.Contains(note.Body, runB.ID[:8]) {
		t.Fatalf("B 恢复后仍滞留在受影响清单: %q", note.Body)
	}
	if !strings.Contains(note.Body, runA.ID[:8]) {
		t.Fatalf("A 的失败行应仍在清单: %q", note.Body)
	}
}

// TestCleanupBranchPendingLateWorktreeReclaimed（复审 Note）T3 建 worktree
// 滞后于首轮回收（首轮按 path 空只判分支进 pending）：复查发现登记出绑定
// 本分支的 worktree 时回到完整回收链路——写回路径转 none，下 tick 走归属
// 核验删除后分支仍在仍回 branch_pending。行内路径与绑定路径不同的歧义
// 场景保持 pending 不删。
func TestCleanupBranchPendingLateWorktreeReclaimed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-late")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 520, env, "br-late", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/late", Branch: "br-late"},
		},
		worktrees: []T3Worktree{{Path: "/wt/late", RefNames: []string{"br-late"}}},
		refNames:  []string{"br-late"},
	}
	review := e.withT3(t3, nil, nil)

	// 复查：登记出绑定本分支的 worktree → 路径写回 + 转 none。
	review.cleanupRunWorktree(run, now)
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.WorktreeState != model.ReviewWorktreeNone || mid.WorktreePath != "/wt/late" {
		t.Fatalf("迟到 worktree 应回填路径并转 none, got %q/%q", mid.WorktreeState, mid.WorktreePath)
	}

	// 下 tick：完整链路（投影路径一致 → 归属核验 → 删除 → 分支仍在 →
	// branch_pending）。
	review.cleanupRunWorktree(&mid, now.Add(time.Second))
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if len(t3.removed) != 1 || t3.removed[0] != "/wt/late" {
		t.Fatalf("removed = %v, want 删除迟到的 /wt/late", t3.removed)
	}
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("工作树删后分支仍在 = %q, want branch_pending", after.WorktreeState)
	}

	// 歧义场景：行内已有路径、登记绑定的是另一路径的 worktree → 归属不明
	// 记 failed（ownership_mismatch）交人工——保持 pending 是无信号的永久
	// 滞留（复审 P2）。
	run2 := seedBranchPendingRow(t, e, repo, 521, env, "br-amb", now.Add(-time.Minute))
	e.svc.db.Model(run2).Updates(map[string]any{"worktree_path": "/wt/ours-gone"})
	run2.WorktreePath = "/wt/ours-gone"
	t3.worktrees = []T3Worktree{{Path: "/wt/foreign", RefNames: []string{"br-amb"}}}
	t3.refNames = []string{"br-amb"}
	review.cleanupRunWorktree(run2, now.Add(2*time.Hour))
	var after2 model.ReviewRun
	e.svc.db.First(&after2, "id = ?", run2.ID)
	if after2.WorktreeState != model.ReviewWorktreeFailed ||
		after2.WorktreeReason != worktreeReasonOwnershipMismatch {
		t.Fatalf("绑定路径与行内不同的歧义 worktree = %q/%q, want failed/ownership_mismatch",
			after2.WorktreeState, after2.WorktreeReason)
	}
	if len(t3.removed) != 1 {
		t.Fatalf("歧义 worktree 不得删除, removed = %v", t3.removed)
	}
}

// TestCleanupOwnershipErrorsUseOwnCounter（复审2轮 P2）归属核验的 DB 读失败
// 与登记读取分键：登记读成功每 tick 复位 registry 键，不再把核验错误的
// 宽限刷回 1——持续性核验失败能累积到上限收尾。
func TestCleanupOwnershipErrorsUseOwnCounter(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-own")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// 种一条正常可回收费运行再删库行：对象各门字段齐全，核验阶段读行
	// 快照必失败（record not found）——模拟行读持续性故障。
	run := e.seedCleanableRun(t, repo, 530, env, model.ReviewRunCompleted, "/wt/x", now)
	run.WorktreeBranch = "br-x"
	if err := e.svc.db.Delete(&model.ReviewRun{}, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("delete phantom run: %v", err)
	}
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/x", Branch: "br-x"},
		},
		worktrees: []T3Worktree{{Path: "/wt/x", RefNames: []string{"br-x"}}},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)
	if got := review.worktreeTries[run.ID+"|"+worktreeTryOwnership]; got != 1 {
		t.Fatalf("核验错误应记 ownership 键, got %d", got)
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRegistry]; got != 0 {
		t.Fatalf("核验错误不得记 registry 键, got %d", got)
	}
	// 后续每轮登记读都成功（复位 registry），核验错误照常累计到上限。
	review.cleanupRunWorktree(run, now.Add(time.Second))
	review.cleanupRunWorktree(run, now.Add(2*time.Second))
	if _, ok := review.worktreeTries[run.ID+"|"+worktreeTryOwnership]; ok {
		t.Fatal("到上限收尾后计数应随 releaseWorktreeTries 释放")
	}
}

// TestCleanupAggregateFiltersByUser（复审2轮 Note）聚合清单按 user_id 过滤：
// 环境已按用户隔离，同（env, 仓库, 原因）下其他用户的行不得进本用户通知。
func TestCleanupAggregateFiltersByUser(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-usr")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	mine := e.seedCleanableRun(t, repo, 540, env, model.ReviewRunFailed, "", now)
	e.svc.db.Model(mine).Updates(map[string]any{
		"worktree_state": model.ReviewWorktreeFailed, "worktree_reason": worktreeReasonRegistryError,
	})
	other := e.seedCleanableRun(t, repo, 541, env, model.ReviewRunFailed, "", now)
	user2 := createTestUser(t, e.svc.db, "someone-else")
	e.svc.db.Model(other).Updates(map[string]any{
		"user_id": user2.ID, "worktree_state": model.ReviewWorktreeFailed,
		"worktree_reason": worktreeReasonRegistryError,
	})

	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.publishCleanupAggregate(mine, worktreeReasonRegistryError, now)

	var note model.Notification
	e.svc.db.First(&note, "kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID)
	if !strings.Contains(note.Body, mine.ID[:8]) {
		t.Fatalf("本用户的失败行应在清单: %q", note.Body)
	}
	if strings.Contains(note.Body, other.ID[:8]) {
		t.Fatalf("其他用户的行不得进本用户清单: %q", note.Body)
	}
}

// TestCleanupAggregateQueryErrorDoesNotPublish（复审3轮 P2）受影响清单查询
// 失败必须中断——带错走下去会把仍有失败行的聚合通知误刷成「均已回收完成」。
func TestCleanupAggregateQueryErrorDoesNotPublish(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-qerr")

	run := e.seedCleanableRun(t, repo, 550, env, model.ReviewRunFailed, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_state": model.ReviewWorktreeFailed, "worktree_reason": worktreeReasonRegistryError,
	})
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.publishCleanupAggregate(run, worktreeReasonRegistryError, now)
	var before model.Notification
	if err := e.svc.db.First(&before,
		"kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID).Error; err != nil {
		t.Fatalf("首条聚合通知未建成: %v", err)
	}
	if !strings.Contains(before.Body, run.ID[:8]) {
		t.Fatalf("清单应含失败行: %q", before.Body)
	}

	// 让受影响清单查询报错（缺列）：瞬时 DB 故障的等价物。
	if err := e.svc.db.Exec("ALTER TABLE review_runs DROP COLUMN worktree_reason").Error; err != nil {
		t.Skipf("sqlite 不支持 DROP COLUMN，无法模拟查询失败: %v", err)
	}
	review.publishCleanupAggregate(run, worktreeReasonRegistryError, now)

	var after model.Notification
	e.svc.db.First(&after, "id = ?", before.ID)
	if !strings.Contains(after.Body, run.ID[:8]) || strings.Contains(after.Body, "均已回收完成") {
		t.Fatalf("查询失败不得把清单刷成已恢复, got %q", after.Body)
	}
}

// TestCleanupPendingReasonSwitchRefreshesOldKey（复审3轮 Note）待确认态之间
// 换原因时旧键清单必须收缩——不补发，旧键的受影响清单永久滞留本运行。
func TestCleanupPendingReasonSwitchRefreshesOldKey(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-swap")

	run := e.seedCleanableRun(t, repo, 551, env, model.ReviewRunFailed, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_state":  model.ReviewWorktreeNone,
		"worktree_reason": worktreeReasonRegistryIncomplete, "worktree_error": "登记读取未在预算内完成",
	})
	run.WorktreeReason, run.WorktreeError = worktreeReasonRegistryIncomplete, "登记读取未在预算内完成"
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.publishCleanupAggregate(run, worktreeReasonRegistryIncomplete, now)
	var note model.Notification
	if err := e.svc.db.First(&note,
		"kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID).Error; err != nil {
		t.Fatalf("incomplete 键通知未建成: %v", err)
	}
	if !strings.Contains(note.Body, run.ID[:8]) {
		t.Fatalf("清单应含待确认行: %q", note.Body)
	}

	// 换原因（incomplete → uncertain）：旧键应按收缩后的清单重发。
	review.writePendingReason(run, worktreeReasonRemoveUncertain, "删除工作区结果待确认", now)

	e.svc.db.First(&note, "id = ?", note.ID)
	if !strings.Contains(note.Body, "受影响运行均已回收完成") {
		t.Fatalf("旧键清单应收缩为已恢复, got %q", note.Body)
	}
	if run.WorktreeReason != worktreeReasonRemoveUncertain {
		t.Fatalf("内存原因应切到新值, got %q", run.WorktreeReason)
	}
}

// TestCleanupRecoveredPathAvoidsPingPong（复审4轮 codex）登记迟到回收写回的
// 路径在投影仍空时必须被复用——空路径跳步会让运行 none↔branch_pending 打摆、
// 工作树永不删。
func TestCleanupRecoveredPathAvoidsPingPong(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-pong")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 560, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_path": "/wt/late", "worktree_branch": "br-late"})
	run.WorktreePath, run.WorktreeBranch = "/wt/late", "br-late"
	t3 := &fakeReviewT3{
		// 投影持续无路径（线程真的没登记 worktreePath）。
		worktrees: []T3Worktree{{Path: "/wt/late", RefNames: []string{"br-late"}}},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	if len(t3.removed) != 1 || t3.removed[0] != "/wt/late" {
		t.Fatalf("应以行快照路径删除迟到的 worktree, removed=%v", t3.removed)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("分支仍在应收 branch_pending, got %q", after.WorktreeState)
	}
}

// TestCleanupDerivesBranchFromRegistry（复审4轮 codex）行内只有路径没有分支
// 名（升级前 NULL 行）且线程 404 时，借登记恢复分支名：工作树删完分支仍在
// → 记名进 branch_pending，而不是收 removed 把存活分支丢成无人追踪。
func TestCleanupDerivesBranchFromRegistry(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-derive")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 561, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{"worktree_path": "/wt/legacy"})
	run.WorktreePath = "/wt/legacy"
	t3 := &fakeReviewT3{
		threadWorktreeErrs: map[string]error{run.ThreadID: errT3ThreadNotFound},
		worktrees:          []T3Worktree{{Path: "/wt/legacy", RefNames: []string{"br-legacy"}}},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	if len(t3.removed) != 1 || t3.removed[0] != "/wt/legacy" {
		t.Fatalf("工作树应被删除, removed=%v", t3.removed)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending ||
		after.WorktreeBranch != "br-legacy" {
		t.Fatalf("登记恢复的分支名应落行并进 branch_pending, got %q/%q",
			after.WorktreeState, after.WorktreeBranch)
	}
}

// TestWorktreeReclaimDueSkipsUnarchived（复审4轮 P2）已派发未归档行等归档
// 阶段，进 cleanupRunWorktree 也立即返回却空耗 processed 名额——到期预判判
// 未到期，防积压饿死真正到期的行。
func TestWorktreeReclaimDueSkipsUnarchived(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	now := time.Now().UTC()

	dispatchedUnarchived := &model.ReviewRun{
		ID: "a", DispatchStartedAt: &now, DispatchedAt: &now,
	}
	if review.worktreeReclaimDue(dispatchedUnarchived, now) {
		t.Fatal("已派发未归档行应判未到期")
	}
	archived := &model.ReviewRun{
		ID: "b", DispatchStartedAt: &now, DispatchedAt: &now, ArchivedAt: &now,
	}
	if !review.worktreeReclaimDue(archived, now) {
		t.Fatal("已归档行应到期")
	}
	neverDispatched := &model.ReviewRun{ID: "c"}
	if !review.worktreeReclaimDue(neverDispatched, now) {
		t.Fatal("未派发行无归档依赖，应到期（立即收 removed）")
	}
	neverLaunched := &model.ReviewRun{ID: "d", ErrorCode: "dispatch_failed"}
	if !review.worktreeReclaimDue(neverLaunched, now) {
		t.Fatal("未建线程行无归档依赖，应到期")
	}
}

// TestCleanupRemoveBudgetExhaustedGoesPending（复审4轮 Note）删除前置在调用
// 方预算内到点（errRegistryIncomplete 包装）：与登记读取同口径挂待确认退避，
// 不占 remove-attempt 三次重试落 failed。
func TestCleanupRemoveBudgetExhaustedGoesPending(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-budget")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 570, env, model.ReviewRunCompleted, "/wt/b", now)
	run.WorktreeBranch = "br-b"
	e.svc.db.Model(run).Updates(map[string]any{"worktree_branch": "br-b"})
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/b", Branch: "br-b"},
		},
		worktrees: []T3Worktree{{Path: "/wt/b", RefNames: []string{"br-b"}}},
		removeErr: fmt.Errorf("%w: %s", errRegistryIncomplete, context.DeadlineExceeded),
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeNone ||
		after.WorktreeReason != worktreeReasonRemoveBudgetPending {
		t.Fatalf("删除侧预算耗尽应保持 none + remove_budget_pending（与登记读取分键）, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRemoveAttempt]; got != 0 {
		t.Fatalf("预算耗尽不得计 remove-attempt 重试, got %d", got)
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRemoveBudget]; got != 1 {
		t.Fatalf("删除侧待确认计数应记 1（与登记读取分键）, got %d", got)
	}
}

// TestCleanupUnmappedBranchFailsNotRemoved（复审5轮 codex）行有路径、分支名
// 缺失且工作树已不在登记：无法把存活 ref 映射回本运行，收 removed 是撒谎——
// 记 failed(branch_unknown) 交人工核查。
func TestCleanupUnmappedBranchFailsNotRemoved(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unmapped")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 580, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{"worktree_path": "/wt/gone"})
	run.WorktreePath = "/wt/gone"
	t3 := &fakeReviewT3{
		threadWorktreeErrs: map[string]error{run.ThreadID: errT3ThreadNotFound},
		// 登记里 worktree 已不在，但疑似存活的专属分支 ref 仍在。
		refNames: []string{"t3-orphan"},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonBranchUnknown {
		t.Fatalf("无法映射分支应收 failed/branch_unknown, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("未解决的残留应发聚合通知")
	}
}

// TestCleanupBudgetPendingConvergesFailed（复审6轮 P2）registry_incomplete 待
// 确认不是无限期状态：连续预算耗尽到 reviewBudgetPendingMaxTries 收敛
// failed(registry_error)。
func TestCleanupBudgetPendingConvergesFailed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-cap")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 590, env, model.ReviewRunCompleted, "/wt/c", now)
	run.WorktreeBranch = "br-c"
	e.svc.db.Model(run).Updates(map[string]any{"worktree_branch": "br-c"})
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/c", Branch: "br-c"},
		},
		listIncomplete: true,
	}
	review := e.withT3(t3, nil, nil)

	at := now
	for i := 0; i < reviewBudgetPendingMaxTries; i++ {
		review.cleanupRunWorktree(run, at)
		at = at.Add(6 * time.Minute) // 越过退避（封顶 5min）
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonRegistryError {
		t.Fatalf("连续预算耗尽到上限应收敛 failed/registry_error, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanupAmbiguousRegistryRefsFailNotRemoved（复审8轮 bot）活投影明说无
// 分支，但登记把该路径绑定到多个 ref 名：无法唯一映射本运行专属分支，与
// 404 快照路径同为「无法证明无残留」——收 removed 是撒谎，记
// failed(branch_unknown)。
func TestCleanupAmbiguousRegistryRefsFailNotRemoved(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-amb")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 610, env, model.ReviewRunCompleted, "", now)
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/live", Branch: ""},
		},
		// 登记仍把该路径绑到两个 ref：工作树步删除后这份快照照旧，其中一
		// 个很可能就是本运行专属分支，但无法指认。
		worktrees: []T3Worktree{{Path: "/wt/live", RefNames: []string{"br-x", "br-y"}}},
		refNames:  []string{"br-x", "br-y"},
	}
	review := e.withT3(t3, nil, nil)

	review.cleanupRunWorktree(run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonBranchUnknown {
		t.Fatalf("登记多 ref 歧义应收 failed/branch_unknown, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanupBranchPendingConvergesFailed（复审8轮 Note）branch_pending 复
// 查确认分支仍存活也有上限：reviewBranchPendingMaxTries 次后收敛
// failed(branch_pending) 交人工，不再永久滞留续排。
func TestCleanupBranchPendingConvergesFailed(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-bpc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 620, env, "br-stuck", now.Add(-time.Hour))
	t3 := &fakeReviewT3{refNames: []string{"br-stuck"}}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewBranchPendingMaxTries; i++ {
		review.recheckBranchPending(context.Background(), run, now.Add(time.Duration(i)*time.Hour))
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonBranchPending {
		t.Fatalf("确认存活到上限应收 failed/branch_pending, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanup404RowBranchOnlyChecksRegistry（复审9轮 bot）线程 404 确认、行
// 内无路径但有分支快照：分支可能仍存活，须走登记判定而非直接 removed。
func TestCleanup404RowBranchOnlyChecksRegistry(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-404b")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 630, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Updates(map[string]any{"worktree_branch": "br-orphan"})
	run.WorktreeBranch = "br-orphan"
	t3 := &fakeReviewT3{
		threadWorktreeErrs: map[string]error{run.ThreadID: errT3ThreadNotFound},
		refNames:           []string{"br-orphan"},
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeBranchPending {
		t.Fatalf("分支仍存活应 branch_pending 而非 removed, got %q", after.WorktreeState)
	}
}

// TestCleanupBranchPendingIncompleteConverges（复审9轮 bot）branch_pending
// 复查的登记预算耗尽同 none 态口径收敛：连续 reviewBudgetPendingMaxTries
// 次后 failed(registry_error)。
func TestCleanupBranchPendingIncompleteConverges(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-bpi")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 640, env, "br-slow", now.Add(-time.Hour))
	t3 := &fakeReviewT3{listIncomplete: true}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewBudgetPendingMaxTries; i++ {
		review.recheckBranchPending(context.Background(), run, now.Add(time.Duration(i)*time.Hour))
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonRegistryError {
		t.Fatalf("连续预算耗尽到上限应收 failed/registry_error, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanupBranchPendingPrefersSamePathBinding（复审9轮 Note）同一分支绑
// 到多个 worktree 且其中一项与行内快照同路径：优先采信同路径恢复转 none，
// 不按切片顺序错判 ownership_mismatch。
func TestCleanupBranchPendingPrefersSamePathBinding(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-multi")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 650, env, "br-multi", now.Add(-time.Minute))
	e.svc.db.Model(run).Updates(map[string]any{"worktree_path": "/wt/ours"})
	run.WorktreePath = "/wt/ours"
	t3 := &fakeReviewT3{
		// 异路径项排在前——旧实现首个命中即 failed。
		worktrees: []T3Worktree{
			{Path: "/wt/foreign", RefNames: []string{"br-multi"}},
			{Path: "/wt/ours", RefNames: []string{"br-multi"}},
		},
		refNames: []string{"br-multi"},
	}
	review := e.withT3(t3, nil, nil)

	review.recheckBranchPending(context.Background(), run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeNone {
		t.Fatalf("存在同路径绑定应恢复转 none 走完整回收, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanupBranchPendingNoPathMultiBindingFails（复审10轮 bot）行内无路径
// 快照且分支被绑到多个不同路径 worktree：bound[0] 按路径排序不代表是本运
// 行的，盲信会删掉别人的工作树——归属不明记 failed 交人工。
func TestCleanupBranchPendingNoPathMultiBindingFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-npmb")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := seedBranchPendingRow(t, e, repo, 660, env, "br-many", now.Add(-time.Minute))
	t3 := &fakeReviewT3{
		worktrees: []T3Worktree{
			{Path: "/wt/a", RefNames: []string{"br-many"}},
			{Path: "/wt/b", RefNames: []string{"br-many"}},
		},
		refNames: []string{"br-many"},
	}
	review := e.withT3(t3, nil, nil)

	review.recheckBranchPending(context.Background(), run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonOwnershipMismatch {
		t.Fatalf("无快照多绑定应收 failed/ownership_mismatch, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
	if after.WorktreePath != "" {
		t.Fatalf("歧义场景不得写回路径, got %q", after.WorktreePath)
	}
}

// TestCleanupPendingWriteFailureKeepsMemoryState（复审12轮 P2）待确认原因的
// 写库失败不得改写内存态：内存先于库态清空会让 finish 读不到 prior、跳过旧
// 键聚合收缩，旧键清单永久滞留本运行。用 sqlite 触发器让 UPDATE 真失败。
func TestCleanupPendingWriteFailureKeepsMemoryState(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-wfail")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 670, env, model.ReviewRunCompleted, "/wt/f", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_reason": worktreeReasonRemoveUncertain, "worktree_error": "x"})
	run.WorktreeReason = worktreeReasonRemoveUncertain

	if err := e.svc.db.Exec(`CREATE TRIGGER block_reason BEFORE UPDATE ON review_runs
		WHEN NEW.worktree_reason <> OLD.worktree_reason
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer e.svc.db.Exec("DROP TRIGGER block_reason")

	// 清除路径：写失败 → 内存原因保持，不得先行清空。
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	review.recoverFromPending(run, now)
	if run.WorktreeReason != worktreeReasonRemoveUncertain {
		t.Fatalf("写失败内存态不得清空, got %q", run.WorktreeReason)
	}
	// 写入路径：写失败 → 内存原因保持旧值，不换成新原因。
	if review.writePendingReason(run, worktreeReasonRegistryIncomplete, "y", now) {
		t.Fatal("写失败应返回 false")
	}
	if run.WorktreeReason != worktreeReasonRemoveUncertain {
		t.Fatalf("写失败内存态不得改写, got %q", run.WorktreeReason)
	}
}

// TestCleanupRemoveBudgetPendingConverges（复审14轮 bot）remove_budget_pending
// 的连续计数不得被登记读成功复位：独立 remove-budget 键累积到
// reviewBudgetPendingMaxTries 收敛 failed(worktree_remove)。
func TestCleanupRemoveBudgetPendingConverges(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-rbc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	run := e.seedCleanableRun(t, repo, 680, env, model.ReviewRunCompleted, "/wt/rb", now)
	e.svc.db.Model(run).Updates(map[string]any{"worktree_branch": "br-rb"})
	run.WorktreeBranch = "br-rb"
	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/rb", Branch: "br-rb"},
		},
		worktrees: []T3Worktree{{Path: "/wt/rb", RefNames: []string{"br-rb"}}},
		// 登记读成功、删除侧预算持续耗尽——旧实现共享计数键，每 tick 被
		// recoverFromPending 复位，永远停在 1。
		removeErr: fmt.Errorf("%w: %s", errRegistryIncomplete, context.DeadlineExceeded),
	}
	review := e.withT3(t3, nil, nil)

	for i := 0; i < reviewBudgetPendingMaxTries; i++ {
		review.cleanupRunWorktree(run, now.Add(time.Duration(i)*6*time.Minute))
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonWorktreeRemove {
		t.Fatalf("删除侧连续预算耗尽应收敛 failed/worktree_remove, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
}

// TestCleanupBranchPendingMissingEnvConverges（复审16轮 Note）branch_pending
// 复查解析不出工作区根目录（环境绑定永久缺失：解绑或环境被删而行残留）：
// 不再无信号地无限续排——branch-env 键连续计数到上限收敛
// failed(env_binding)，「分支待清理」事实由原因与文案保留交人工。
func TestCleanupBranchPendingMissingEnvConverges(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	// 环境行在、但不建绑定：bindingFor 返回 nil，行内又无 workspace_root
	// 快照 → worktreeCwdFor 恒为空。
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unbound")

	run := e.seedCleanableRun(t, repo, 695, env, model.ReviewRunCompleted, "/wt/unb", now)
	e.svc.db.Model(run).Updates(map[string]any{
		"worktree_state":  string(model.ReviewWorktreeBranchPending),
		"worktree_reason": worktreeReasonBranchPending,
		"worktree_branch": "br-unb",
		"worktree_path":   "/wt/unb",
		"environment_id":  env.ID,
	})
	run.WorktreeState = model.ReviewWorktreeBranchPending
	run.WorktreeReason = worktreeReasonBranchPending
	run.WorktreeBranch = "br-unb"
	review := e.withT3(&fakeReviewT3{}, nil, nil)

	at := now
	for i := 0; i < reviewBranchPendingMaxTries; i++ {
		review.cleanupRunWorktree(run, at)
		at = at.Add(61 * time.Minute) // 越过 branch_pending 退避（封顶 1h）
		if i < reviewBranchPendingMaxTries-1 {
			var mid model.ReviewRun
			e.svc.db.First(&mid, "id = ?", run.ID)
			if mid.WorktreeState != model.ReviewWorktreeBranchPending {
				t.Fatalf("第 %d 次复查 = %q, want 保持 branch_pending", i+1, mid.WorktreeState)
			}
		}
	}
	if got := review.worktreeTries[run.ID+"|"+worktreeTryBranchEnv]; got != 0 {
		t.Fatalf("终态转移后 branch-env 计数应释放, got %d", got)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonEnvBinding {
		t.Fatalf("环境绑定长期缺失应收敛 failed/env_binding, got %q/%q",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "br-unb") {
		t.Fatalf("失败文案应保留分支名供人工清理, got %q", after.WorktreeError)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("收敛应交一条聚合通知")
	}
}

// TestRecoverFromPendingResetsCounterBeforeReasonCheck（复审16轮 Note）登记
// 读成功时 regIncomplete 计数必须先于原因判空复位：markPendingRetry 先计数
// 后写原因，写库失败会让 reason 留空——复位放在判空之后会把「连续失败」
// 退化成跨成功累计，叠满上限误收 failed。
func TestRecoverFromPendingResetsCounterBeforeReasonCheck(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(&fakeReviewT3{}, nil, nil)
	now := time.Now().UTC()

	// 写失败场景：计数已累积、行内原因留空。
	run := &model.ReviewRun{ID: "r-writeflap"}
	for i := 0; i < 5; i++ {
		review.bumpWorktreeTries(run.ID, worktreeTryRegIncomplete)
	}
	review.recoverFromPending(run, now)
	if got := review.worktreeTries[run.ID+"|"+worktreeTryRegIncomplete]; got != 0 {
		t.Fatalf("登记读成功必须复位 regIncomplete 计数（即使 reason 为空）, got %d", got)
	}

	// 删除侧待确认不被登记读消解：reason 与 remove-attempt 计数都保留，
	// 但 regIncomplete 计数照样复位（读成功确实证明登记侧通了）。
	run2 := &model.ReviewRun{ID: "r-removepend", WorktreeReason: worktreeReasonRemoveUnavailable}
	review.bumpWorktreeTries(run2.ID, worktreeTryRegIncomplete)
	review.bumpWorktreeTries(run2.ID, worktreeTryRemoveAttempt)
	review.recoverFromPending(run2, now)
	if got := review.worktreeTries[run2.ID+"|"+worktreeTryRegIncomplete]; got != 0 {
		t.Fatalf("regIncomplete 应复位, got %d", got)
	}
	if got := review.worktreeTries[run2.ID+"|"+worktreeTryRemoveAttempt]; got != 1 {
		t.Fatalf("remove-attempt 不应被登记读成功复位, got %d", got)
	}
	if run2.WorktreeReason != worktreeReasonRemoveUnavailable {
		t.Fatalf("remove_unavailable 不应被登记读清除, got %q", run2.WorktreeReason)
	}
}
