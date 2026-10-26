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

// TestCleanupRemoveNotAttemptedRetriesThenFails（F7）删除请求未送达（环境/
// 连接不可用）：远端必然没执行——有限重试、不复查登记，到限 failed +
// 聚合，文案写明未送达而非「被远端明确拒绝」。
func TestCleanupRemoveNotAttemptedRetriesThenFails(t *testing.T) {
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

	for i := 0; i < reviewWorktreeMaxTries-1; i++ {
		review.cleanupRunWorktree(run, now)
		var mid model.ReviewRun
		e.svc.db.First(&mid, "id = ?", run.ID)
		if mid.WorktreeState != model.ReviewWorktreeNone {
			t.Fatalf("第 %d tick = %q, want none（未送达有限重试）", i+1, mid.WorktreeState)
		}
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		after.WorktreeReason != worktreeReasonWorktreeRemove {
		t.Fatalf("worktree = %q/%q, want failed/worktree_remove（到限）",
			after.WorktreeState, after.WorktreeReason)
	}
	if !strings.Contains(after.WorktreeError, "未能送达") {
		t.Fatalf("worktree_error = %q, 应写明未送达而非远端拒绝", after.WorktreeError)
	}
	if t3.listCalls != reviewWorktreeMaxTries {
		t.Fatalf("listCalls = %d, want %d（未送达分支不复查登记）", t3.listCalls, reviewWorktreeMaxTries)
	}
	if len(t3.removed) != reviewWorktreeMaxTries {
		t.Fatalf("removed = %v, want 每轮各试一次删除", t3.removed)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("到限应发一条聚合通知")
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
