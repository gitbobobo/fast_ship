package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

// fakeReviewT3 线程/工作区执行面的内存桩：记录调用、可注入错误。
// ThreadWorktree 按 threadID 查 threadWorktreeErrs（单线程错误优先）、
// threadWorktrees（未命中回 threadWTDefault）；全局 threadWTErrs 按调用次序
// 出队，取完后回 threadWTErr。createFn 非空时代替默认桩。准备失败判定
// （INT-147）按 threadID 查 prepFailed，缺 key 回 (nil, nil)。
type fakeReviewT3 struct {
	threadWorktrees    map[string]T3ThreadWorktree
	threadWorktreeErrs map[string]error
	threadWTDefault    T3ThreadWorktree
	threadWTErrs       []error
	threadWTErr        error
	threadWTCalls      []string
	worktrees          []T3Worktree
	listErr            error
	listCalls          int
	listCwds           []string
	stopped            []string
	removed            []string
	removedCwd         []string
	removeErr          error
	archived           []string
	archiveErr         error
	prepFailed         map[string]*T3FailedPreparation
	prepErr            error
	prepCalls          []string // threadID:messageID
	// prepFn 非空时代替 prepFailed/prepErr：在投影读里执行（测试用假时钟注入
	// 「慢投影读」的耗时，确定性验证阶段预算与轮转）。
	prepFn      func(threadID string) (*T3FailedPreparation, error)
	retried     []string // threadID
	retryRunIDs []string
	retryCmdIDs []string
	retryErr    error
	// retryFn 非空时代替 retryErr：在重试 RPC 里执行（注入「RPC 期间运行被
	// 并发终态化」「RPC 自己的上下文是否还有预算」等场景），返回值作为 RPC 结果。
	retryFn  func(ctx context.Context, threadID string) error
	createFn func(context.Context, string, string, *T3CreateThreadRequest) (*T3ThreadLaunchResult, bool, error)
	created  []*T3CreateThreadRequest
}

func (f *fakeReviewT3) CreateThreadForAgent(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (*T3ThreadLaunchResult, bool, error) {
	f.created = append(f.created, req)
	if f.createFn != nil {
		return f.createFn(ctx, userID, envID, req)
	}
	return nil, false, errors.New("not implemented in test")
}

func (f *fakeReviewT3) StopThread(_ context.Context, _, _, threadID string) (*T3ThreadStopResult, error) {
	f.stopped = append(f.stopped, threadID)
	return &T3ThreadStopResult{}, nil
}

func (f *fakeReviewT3) ThreadWorktree(_ context.Context, _, _, threadID string) (*T3ThreadWorktree, error) {
	f.threadWTCalls = append(f.threadWTCalls, threadID)
	if err, ok := f.threadWorktreeErrs[threadID]; ok {
		return nil, err
	}
	if len(f.threadWTErrs) > 0 {
		err := f.threadWTErrs[0]
		f.threadWTErrs = f.threadWTErrs[1:]
		return nil, err
	}
	if f.threadWTErr != nil {
		return nil, f.threadWTErr
	}
	if wt, ok := f.threadWorktrees[threadID]; ok {
		out := wt
		return &out, nil
	}
	out := f.threadWTDefault
	return &out, nil
}

func (f *fakeReviewT3) ListWorktrees(_ context.Context, _, _, cwd string) ([]T3Worktree, error) {
	f.listCalls++
	f.listCwds = append(f.listCwds, cwd)
	return f.worktrees, f.listErr
}

func (f *fakeReviewT3) RemoveWorktree(_ context.Context, _, _, cwd, path string, _ bool) error {
	f.removed = append(f.removed, path)
	f.removedCwd = append(f.removedCwd, cwd)
	return f.removeErr
}

func (f *fakeReviewT3) ArchiveThread(_ context.Context, _, _, threadID string) error {
	f.archived = append(f.archived, threadID)
	return f.archiveErr
}

func (f *fakeReviewT3) FailedPreparation(_ context.Context, _, _, threadID, messageID string) (*T3FailedPreparation, error) {
	f.prepCalls = append(f.prepCalls, threadID+":"+messageID)
	if f.prepFn != nil {
		return f.prepFn(threadID)
	}
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	if out, ok := f.prepFailed[threadID]; ok {
		return out, nil
	}
	return nil, nil
}

func (f *fakeReviewT3) RetryPreparation(ctx context.Context, _, _, threadID, t3RunID, commandID string) error {
	f.retried = append(f.retried, threadID)
	f.retryRunIDs = append(f.retryRunIDs, t3RunID)
	f.retryCmdIDs = append(f.retryCmdIDs, commandID)
	if f.retryFn != nil {
		return f.retryFn(ctx, threadID)
	}
	return f.retryErr
}

// withT3 在共享测试库上组装一个带 T3/媒体桩的 ReviewService。media 传 nil
// 时用环境的默认成功桩（没有截图引用的 PR 不受影响）。
func (e *reviewEngineEnv) withT3(t3 reviewT3, media reviewMedia,
	bearer func(context.Context, int64) (string, error)) *ReviewService {
	if media == nil {
		media = e.media
	}
	return NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: e.fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		t3, media, bearer, e.svc.cfg, zap.NewNop())
}

// seedDispatchedRun 落一行已确认派发的运行（dispatched_at 非空），可指定
// worktree_path/archived_at/kind。
func (e *reviewEngineEnv) seedDispatchedRun(t *testing.T, repo string, pr int,
	kind model.ReviewKind, status model.ReviewRunStatus, worktreePath string, archived bool, now time.Time) *model.ReviewRun {
	t.Helper()
	run := e.seedRun(t, repo, pr, "sha-c", status, now)
	run.Kind = kind
	run.WorktreePath = worktreePath
	run.DispatchStartedAt = &now
	run.DispatchedAt = &now
	if archived {
		run.ArchivedAt = &now
	}
	if status.Terminal() {
		run.FinishedAt = &now
	}
	if err := e.svc.db.Save(run).Error; err != nil {
		t.Fatalf("save run: %v", err)
	}
	return run
}

func (e *reviewEngineEnv) cleanupFailedCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	e.svc.db.Model(&model.Notification{}).
		Where("kind = ? AND user_id = ?", model.NotificationReviewCleanupFailed, e.user.ID).Count(&n)
	return n
}

// seedCleanableRun 落一条已归档终态代码运行：环境换成真实 env 并带
// t3_project_id，worktree_path 可空（正常流转下行里就是空的，回收时按
// 线程投影真值写回）。
func (e *reviewEngineEnv) seedCleanableRun(t *testing.T, repo string, pr int,
	env *model.T3Environment, status model.ReviewRunStatus, worktreePath string, now time.Time) *model.ReviewRun {
	t.Helper()
	run := e.seedDispatchedRun(t, repo, pr, model.ReviewKindCode, status, worktreePath, true, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"environment_id": env.ID, "t3_project_id": "t3p",
	})
	run.EnvironmentID, run.T3ProjectID = env.ID, "t3p"
	return run
}

// TestReviewCleanupOwnership F1：工作区真值只认线程投影；删除前核验归属。
func TestReviewCleanupOwnership(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	// 绑定需要真实存在的环境（FK）。
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-c")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// 线程投影没有工作区路径：有限重试到上限后按「无工作区可清」removed——
	// 不通知、不列举、不删任何路径。
	r1 := e.seedDispatchedRun(t, repo, 1, model.ReviewKindCode, model.ReviewRunFailed, "", true, now)
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(r1, now)
	}
	var after1 model.ReviewRun
	e.svc.db.First(&after1, "id = ?", r1.ID)
	if after1.WorktreeState != model.ReviewWorktreeRemoved {
		t.Fatalf("empty-path worktree = %q, want removed", after1.WorktreeState)
	}
	if len(t3.removed) != 0 || t3.listCalls != 0 {
		t.Fatal("线程无工作区不得做登记列举与删除")
	}
	if got := len(t3.threadWTCalls); got != reviewWorktreeMaxTries {
		t.Fatalf("thread reads = %d, want %d（空路径有限重试）", got, reviewWorktreeMaxTries)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("线程确认无工作区不应发失败通知")
	}

	// 线程真值正常：路径写回行 + listRefs 核验通过 → 删除 → removed。
	r2 := e.seedCleanableRun(t, repo, 1, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		r2.ThreadID: {Path: "/wt/owned", Branch: "br-owned"},
	}
	t3.worktrees = []T3Worktree{{Path: "/wt/owned", RefNames: []string{"br-owned"}}}
	review.cleanupRunWorktree(r2, now)
	var after2 model.ReviewRun
	e.svc.db.First(&after2, "id = ?", r2.ID)
	if after2.WorktreeState != model.ReviewWorktreeRemoved || len(t3.removed) != 1 || t3.removed[0] != "/wt/owned" {
		t.Fatalf("owned worktree = %q removed=%v", after2.WorktreeState, t3.removed)
	}
	if after2.WorktreePath != "/wt/owned" {
		t.Fatalf("worktree_path = %q，应写回线程真值 /wt/owned", after2.WorktreePath)
	}

	// 路径被其他运行认领：failed + 不删。
	other := e.seedRun(t, repo, 1, "sha-c", model.ReviewRunRunning, now)
	e.svc.db.Model(other).Update("worktree_path", "/wt/shared")
	r3 := e.seedCleanableRun(t, repo, 1, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees[r3.ThreadID] = T3ThreadWorktree{Path: "/wt/shared", Branch: "br-shared"}
	t3.worktrees = append(t3.worktrees, T3Worktree{Path: "/wt/shared", RefNames: []string{"br-shared"}})
	removedBefore := len(t3.removed)
	review.cleanupRunWorktree(r3, now)
	var after3 model.ReviewRun
	e.svc.db.First(&after3, "id = ?", r3.ID)
	if after3.WorktreeState != model.ReviewWorktreeFailed || len(t3.removed) != removedBefore {
		t.Fatalf("shared worktree = %q removed=%v", after3.WorktreeState, t3.removed)
	}

	// 归档未完成：跳过（等 archiveRuns），不动状态不调删除。
	r4 := e.seedDispatchedRun(t, repo, 1, model.ReviewKindCode, model.ReviewRunCompleted, "/wt/pending", false, now)
	review.cleanupRunWorktree(r4, now)
	var after4 model.ReviewRun
	e.svc.db.First(&after4, "id = ?", r4.ID)
	if after4.WorktreeState != model.ReviewWorktreeNone || len(t3.removed) != removedBefore {
		t.Fatalf("unarchived worktree = %q removed=%v", after4.WorktreeState, t3.removed)
	}
}

// TestCleanupConcurrentRunsAllRemoved INT-142 回归：多组合连发的代码运行
// 回收——行里 worktree_path 全空，各线程投影给出不同的 {Path,Branch}，
// listRefs 里三条都以同名 ref 登记；三条全部 removed 且路径/线程一一对应。
func TestCleanupConcurrentRunsAllRemoved(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-burst")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	threadWT := map[string]T3ThreadWorktree{}
	var refs []T3Worktree
	runs := make([]*model.ReviewRun, 3)
	for i := range runs {
		runs[i] = e.seedCleanableRun(t, repo, 40+i, env, model.ReviewRunCompleted, "", now)
		path := fmt.Sprintf("/wt/burst-%d", i)
		branch := fmt.Sprintf("br-burst-%d", i)
		threadWT[runs[i].ThreadID] = T3ThreadWorktree{Path: path, Branch: branch}
		refs = append(refs, T3Worktree{Path: path, RefNames: []string{branch}})
	}
	t3 := &fakeReviewT3{threadWorktrees: threadWT, worktrees: refs}
	review := e.withT3(t3, nil, nil)

	review.cleanupWorktrees(now)

	if len(t3.removed) != len(runs) {
		t.Fatalf("removed = %v, want %d 条", t3.removed, len(runs))
	}
	wantPaths := map[string]bool{}
	for i := range runs {
		wantPaths[fmt.Sprintf("/wt/burst-%d", i)] = true
	}
	for _, p := range t3.removed {
		delete(wantPaths, p)
	}
	if len(wantPaths) != 0 {
		t.Fatalf("删除路径与线程不一一对应，漏删: %v", wantPaths)
	}
	for i, run := range runs {
		var after model.ReviewRun
		e.svc.db.First(&after, "id = ?", run.ID)
		if after.WorktreeState != model.ReviewWorktreeRemoved {
			t.Fatalf("run %d worktree_state = %q, want removed", i, after.WorktreeState)
		}
		if want := fmt.Sprintf("/wt/burst-%d", i); after.WorktreePath != want {
			t.Fatalf("run %d worktree_path = %q, want %q（线程真值写回）", i, after.WorktreePath, want)
		}
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("多组合连发不应产生回收失败通知")
	}
}

// TestCleanupThreadNotFoundRemoves 线程 404：可能来自 T3 重启/代理抖动，同样
// 走有限重试——上限内不动状态，连续到上限才认定线程确已不存在：removed，
// 不通知、不删。
func TestCleanupThreadNotFoundRemoves(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-gone")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{threadWTErr: errT3ThreadNotFound}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 44, env, model.ReviewRunCompleted, "", now)
	for i := 0; i < reviewWorktreeMaxTries-1; i++ {
		review.cleanupRunWorktree(run, now)
		var mid model.ReviewRun
		e.svc.db.First(&mid, "id = ?", run.ID)
		if mid.WorktreeState != model.ReviewWorktreeNone {
			t.Fatalf("404 重试中 worktree_state = %q, want none", mid.WorktreeState)
		}
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved {
		t.Fatalf("worktree_state = %q, want removed", after.WorktreeState)
	}
	if len(t3.removed) != 0 || t3.listCalls != 0 {
		t.Fatal("线程不存在不得列举/删除")
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("线程不存在按无工作区处理，不应通知")
	}
}

// TestCleanupThreadReadErrorRetries 读线程工作区失败：上限内只重试不动状态，
// 到上限 failed + 通知 + 不删。
func TestCleanupThreadReadErrorRetries(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-flaky")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{threadWTErr: errors.New("upstream boom")}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 45, env, model.ReviewRunCompleted, "", now)
	for i := 0; i < reviewWorktreeMaxTries-1; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", run.ID)
	if mid.WorktreeState != model.ReviewWorktreeNone {
		t.Fatalf("重试中 worktree_state = %q, want none（不动状态）", mid.WorktreeState)
	}
	review.cleanupRunWorktree(run, now)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		!strings.Contains(after.WorktreeError, "读取线程工作区失败") {
		t.Fatalf("worktree = %q/%q, want failed+读失败原因", after.WorktreeState, after.WorktreeError)
	}
	if len(t3.removed) != 0 {
		t.Fatal("读失败不得删除")
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("到上限应发一次失败通知")
	}
}

// TestCleanupUnregisteredWorktreeSkipsDelete 线程记录的路径已不在项目登记中：
// 视为无工作区可清，removed——不通知、不删。
func TestCleanupUnregisteredWorktreeSkipsDelete(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-unreg")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{
		worktrees: []T3Worktree{{Path: "/wt/someone-else", RefNames: []string{"br-x"}}},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 46, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		run.ThreadID: {Path: "/wt/gone", Branch: "br-gone"},
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved {
		t.Fatalf("worktree_state = %q, want removed", after.WorktreeState)
	}
	if len(t3.removed) != 0 {
		t.Fatalf("未登记路径不得删除, removed=%v", t3.removed)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("未登记按无工作区处理，不应通知")
	}
}

// TestCleanupRefNameMismatchFails 登记 ref 名与线程分支不一致：failed + 通知，
// 不删。
func TestCleanupRefNameMismatchFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-refmis")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{
		worktrees: []T3Worktree{{Path: "/wt/x", RefNames: []string{"br-other"}}},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 47, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		run.ThreadID: {Path: "/wt/x", Branch: "br-thread"},
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		!strings.Contains(after.WorktreeError, "分支与线程记录不一致") {
		t.Fatalf("worktree = %q/%q, want failed+分支不一致", after.WorktreeState, after.WorktreeError)
	}
	if len(t3.removed) != 0 {
		t.Fatal("分支不一致不得删除")
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("分支不一致应发失败通知")
	}
}

// TestCleanupRowPathMismatchFails 行内旧快照路径与线程真值不一致：failed +
// 通知，不删。
func TestCleanupRowPathMismatchFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-rowmis")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{
		worktrees: []T3Worktree{{Path: "/wt/new", RefNames: []string{"br-n"}}},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 48, env, model.ReviewRunCompleted, "/wt/old", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		run.ThreadID: {Path: "/wt/new", Branch: "br-n"},
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		!strings.Contains(after.WorktreeError, "工作区与线程记录不一致") {
		t.Fatalf("worktree = %q/%q, want failed+路径不一致", after.WorktreeState, after.WorktreeError)
	}
	if len(t3.removed) != 0 {
		t.Fatal("行与线程不一致不得删除")
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("路径不一致应发失败通知")
	}
}

// TestCleanupListWorktreesErrorRetries 列举项目工作区登记失败：有限重试到
// 上限后 failed + 通知 + 不删。
func TestCleanupListWorktreesErrorRetries(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-listerr")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{listErr: errors.New("listRefs down")}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 49, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		run.ThreadID: {Path: "/wt/x", Branch: "br-x"},
	}
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		!strings.Contains(after.WorktreeError, "读取项目工作区登记失败") {
		t.Fatalf("worktree = %q/%q, want failed+登记读取失败", after.WorktreeState, after.WorktreeError)
	}
	if len(t3.removed) != 0 {
		t.Fatal("登记读取失败不得删除")
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("到上限应发一次失败通知")
	}
}

// TestReviewArchiveStage F11：终态且 dispatched 的运行归档；失败有限重试后
// 置 archived_at 防无限重试 + 通知 + 代码运行 worktree failed。
func TestReviewArchiveStage(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{archiveErr: errors.New("archive rpc down")}
	review := e.withT3(t3, nil, nil)

	visual := e.seedDispatchedRun(t, repo, 2, model.ReviewKindVisual, model.ReviewRunCompleted, "", false, now)
	code := e.seedDispatchedRun(t, repo, 2, model.ReviewKindCode, model.ReviewRunCompleted, "/wt/c", false, now)

	// 有界重试：前两次仅重试，第三次超限。
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.archiveRuns(now)
	}
	var v, c model.ReviewRun
	e.svc.db.First(&v, "id = ?", visual.ID)
	e.svc.db.First(&c, "id = ?", code.ID)
	if v.ArchivedAt == nil || c.ArchivedAt == nil {
		t.Fatal("超限后 archived_at 应写入防无限重试")
	}
	if c.WorktreeState != model.ReviewWorktreeFailed {
		t.Fatalf("code worktree = %q, want failed", c.WorktreeState)
	}
	if len(t3.archived) != reviewWorktreeMaxTries*2 {
		t.Fatalf("archive calls = %d, want %d", len(t3.archived), reviewWorktreeMaxTries*2)
	}
	if e.cleanupFailedCount(t) != 2 {
		t.Fatal("归档超限应对每条运行各发一次通知")
	}

	// 成功后写 archived_at。
	t3.archiveErr = nil
	ok := e.seedDispatchedRun(t, repo, 2, model.ReviewKindCode, model.ReviewRunCompleted, "/wt/ok", false, now)
	review.archiveRuns(now)
	var a model.ReviewRun
	e.svc.db.First(&a, "id = ?", ok.ID)
	if a.ArchivedAt == nil {
		t.Fatal("归档成功应写 archived_at")
	}
}

// TestReviewResultRequiredKeys F4：必填顶层键缺省/为 null → 40004。
func TestReviewResultRequiredKeys(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 31, &githubapi.PullRequest{
		RepoFullName: repo, Number: 31, HeadSHA: "sha-k",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	run := e.seedRun(t, repo, 31, "sha-k", model.ReviewRunRunning, now)

	// 只有 summary → 缺 findings/prior/ui_changed*。
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run.ID, []byte(`{"summary":"ok"}`)), 40004)
	// 显式 null 同样拒收。
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run.ID,
		[]byte(`{"summary":"ok","findings":[],"prior":null,"ui_changed":false,"ui_changed_since_last_visual":false}`)), 40004)
	// prior 条目缺 body → 40004。
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run.ID,
		[]byte(`{"summary":"ok","findings":[],"prior":[{"status":"open"}],"ui_changed":false,"ui_changed_since_last_visual":false}`)), 40004)
	// 全键齐 → 通过。
	good := `{"summary":"ok","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
}

// TestReviewStatusResetOnNewHead F5：webhook 新 SHA 与开轮推进都清空
// code/visual_status，新 SHA 重新发 pending。
func TestReviewStatusResetOnNewHead(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	e.fake.SetPR(repo, 41, &githubapi.PullRequest{
		RepoFullName: repo, Number: 41, HeadSHA: "sha-2",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	// 总账带旧 head 的已发布结论。
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 41, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-1", RoundNumber: 1,
		CodeStatus: "success", VisualStatus: "failure", UpdatedAt: now,
	})
	e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 41, HeadSHA: "sha-1", AcceptedAt: now,
	})

	if err := e.review.HandleGitHubWebhook(context.Background(), "pull_request", "d-new",
		prPayload("synchronize", repo, 41, "sha-2", false, false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 41", repo)
	if ledger.HeadSHA != "sha-2" {
		t.Fatalf("ledger head = %q, want sha-2（webhook 立刻推进 head）", ledger.HeadSHA)
	}

	// 防抖消费开轮 + 同步：新 head 应重新发 pending。
	e.svc.db.Model(&model.ReviewDebounce{}).Where("repo_full_name = ?", repo).
		Update("fire_at", now.Add(-time.Second))
	e.review.processDueDebounces(now.Add(time.Second))
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 41", repo)
	if ledger.HeadSHA != "sha-2" {
		t.Fatalf("ledger head = %q, want sha-2", ledger.HeadSHA)
	}
	if ledger.CodeStatus != "" || ledger.VisualStatus != "" {
		t.Fatalf("statuses after open round = %q/%q, want cleared", ledger.CodeStatus, ledger.VisualStatus)
	}
	e.review.syncPendingPRs(now.Add(2 * time.Second))
	statuses := e.fake.StatusesForContext("代码审查")
	found := false
	for _, s := range statuses {
		if s.SHA == "sha-2" {
			found = true
		}
	}
	if !found {
		t.Fatal("新 head 应重新发 pending commit status")
	}
}

// TestReviewResultAfterDeadline F7：deadline 后的提交 → 40920 + timed_out +
// 令牌消费 + sync_pending。
func TestReviewResultAfterDeadline(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 51, &githubapi.PullRequest{
		RepoFullName: repo, Number: 51, HeadSHA: "sha-d",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	run := e.seedRun(t, repo, 51, "sha-d", model.ReviewRunRunning, now)
	e.svc.db.Model(run).Update("deadline_at", now.Add(-time.Minute))
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-late", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 51, ProjectID: e.project.ID, UserID: e.user.ID,
		HeadSHA: "sha-d", RoundNumber: 1, UpdatedAt: now,
	})

	good := `{"summary":"s","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good)), 40920)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunTimedOut || after.ErrorCode != "timeout" {
		t.Fatalf("late submit run = %q/%q, want timed_out/timeout", after.Status, after.ErrorCode)
	}
	var token model.ReviewRunToken
	e.svc.db.First(&token, "run_id = ?", run.ID)
	if token.ConsumedAt == nil {
		t.Fatal("late submit should consume token")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 51", repo)
	if !ledger.SyncPending {
		t.Fatal("late submit should mark sync_pending")
	}
}

// TestReviewWebhookUncheckedAction F8：未勾选动作不开轮，但 seen/取消/删防
// 抖/置 pending 照常推进。
func TestReviewWebhookUncheckedAction(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t, func(s *model.ProjectReviewSettings) { s.TriggerSynchronize = false })
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	now := time.Now().UTC()

	// 先制造一条 pending 的旧账本：未勾选的新 head 必须把它清掉。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: strings.ToLower(repo), PRNumber: 61,
		UserID: e.user.ID, HeadSHA: "sha-old", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 3, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	old := e.seedRun(t, repo, 61, "sha-old", model.ReviewRunRunning, now)
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-u1",
		prPayload("synchronize", repo, 61, "sha-new", false, false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}

	var run model.ReviewRun
	e.svc.db.First(&run, "id = ?", old.ID)
	if run.Status != model.ReviewRunCancelled {
		t.Fatalf("old run = %q, want cancelled（未勾选也要收敛旧 SHA）", run.Status)
	}
	var seen int64
	e.svc.db.Model(&model.ReviewSeenSHA{}).Where("head_sha = 'sha-new'").Count(&seen)
	if seen != 1 {
		t.Fatal("未勾选也要登记 seen SHA")
	}
	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 61", repo).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-new" {
		t.Fatalf("ledger head = %q, want sha-new（未勾选也要推进 head）", ledger.HeadSHA)
	}
	if ledger.SyncPending {
		t.Fatal("未勾选不应置 sync_pending（不应发布旧轮结论）")
	}
	// 旧轮残留的 pending 必须被清掉，否则调度会拿新 head 按旧轮发布。
	var epoch int
	e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND pr_number = 61", e.project.ID).
		Select("sync_epoch").Scan(&epoch)
	if epoch == 0 {
		t.Fatal("前进 head 时 sync_epoch 应递增（中止在途同步）")
	}
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("debounce rows = %d, want 0（未勾选不开轮）", got)
	}

	// 同一 SHA 上随后勾选的动作仍应正常开轮（head 已推进，不会被当成回旧 SHA）。
	// 把已受理 head 的 pr_updated_at 改旧，模拟真实的「后续事件时间更晚」。
	e.svc.db.Model(&model.ReviewSeenSHA{}).
		Where("head_sha = 'sha-new'").
		Update("pr_updated_at", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	e.svc.db.Model(&model.ProjectReviewSettings{}).Where("project_id = ?", e.project.ID).
		Update("trigger_ready_for_review", true)
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-u2",
		prPayload("ready_for_review", repo, 61, "sha-new", false, false)); err != nil {
		t.Fatalf("webhook 2: %v", err)
	}
	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("debounce rows = %d, want 1（同一 SHA 的已勾选动作应开轮）", got)
	}
}

// fakeReviewMedia 截图下载桩：匿名/Bearer 两路分开计数与注入错误。failURLs
// 按 URL 注入失败（其余成功），覆盖「部分截图可用」的场景；blockURLs 的
// URL 阻塞到 ctx.Done 再失败，模拟靠前地址握手挂起的黑洞（F32）。
type fakeReviewMedia struct {
	cached                 *githubmedia.CachedMedia
	failURLs               map[string]error
	blockURLs              map[string]bool
	anonErr, bearerErr     error
	anonCalls, bearerCalls int
	lastBearer             string
}

// fakeScreenshotMedia 恒成功的截图下载桩：临时目录里落一个最小 PNG 当缓存
// 产物（CachedImage 由 githubmedia 提供，cacheEntry 未导出、外部填不了）。
func fakeScreenshotMedia(t *testing.T) *fakeReviewMedia {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatalf("write fake png: %v", err)
	}
	return &fakeReviewMedia{cached: githubmedia.CachedImage(path, "image/png", 15)}
}

func (f *fakeReviewMedia) FetchAndCache(ctx context.Context, url string) (*githubmedia.CachedMedia, error) {
	f.anonCalls++
	if f.blockURLs[url] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err, bad := f.failURLs[url]; bad {
		return nil, err
	}
	if f.anonErr != nil {
		return nil, f.anonErr
	}
	return f.cached, nil
}

func (f *fakeReviewMedia) FetchAndCacheWithBearer(_ context.Context, _, bearer, _ string) (*githubmedia.CachedMedia, error) {
	f.bearerCalls++
	f.lastBearer = bearer
	if f.bearerErr != nil {
		return nil, f.bearerErr
	}
	return f.cached, nil
}

// TestReviewAssetsBearerRetry F10：匿名 404（私有附件对匿名请求的回法）→
// 带 App 令牌重试；失败资产行下次 ensure 重试并更新原行。
func TestReviewAssetsBearerRetry(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	cached := &githubmedia.CachedMedia{Path: "/cache/shot.png"}
	media := &fakeReviewMedia{cached: cached, anonErr: &githubmedia.UpstreamStatusError{StatusCode: 404}}
	review := e.withT3(nil, media, func(context.Context, int64) (string, error) {
		return "installation-token-1", nil
	})
	body := "intro ![a](https://github.com/user-attachments/assets/xyz) tail"

	// 匿名失败 → Bearer 重试成功 → 行带 storage_path。
	_, assets, err := review.ensureAssets(ctx, repo, 71, "sha-a", body, 42)
	if err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if media.anonCalls != 1 || media.bearerCalls != 1 || media.lastBearer != "installation-token-1" {
		t.Fatalf("calls = anon:%d bearer:%d(%q)", media.anonCalls, media.bearerCalls, media.lastBearer)
	}
	if len(assets) != 1 || assets[0].StoragePath != "/cache/shot.png" {
		t.Fatalf("assets = %+v", assets)
	}

	// 制造失败行：下一次 ensure 重试并更新原行（不插新行）。
	e.svc.db.Model(&model.ReviewAsset{}).Where("source_url LIKE ?", "%xyz%").
		Updates(map[string]any{"storage_path": "", "error": "prev failure"})
	media.anonErr = nil
	_, assets, err = review.ensureAssets(ctx, repo, 71, "sha-a", body, 42)
	if err != nil {
		t.Fatalf("ensureAssets retry: %v", err)
	}
	if media.anonCalls != 2 {
		t.Fatalf("retry anon calls = %d, want 2", media.anonCalls)
	}
	var count int64
	e.svc.db.Model(&model.ReviewAsset{}).Where("source_url LIKE ?", "%xyz%").Count(&count)
	if count != 1 {
		t.Fatalf("asset rows = %d, want 1（失败行原地更新）", count)
	}
	if len(assets) != 1 || assets[0].StoragePath != "/cache/shot.png" {
		t.Fatalf("retried assets = %+v", assets)
	}
}

// TestVisualHistoryLatestVisualRound F12：取最近一轮有视觉运行的历史
// （round1 有、round2 无、round3 重跑时用 round1）。
func TestVisualHistoryLatestVisualRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	mkVisual := func(round int, status model.ReviewRunStatus, result string) {
		run := e.seedRun(t, repo, 81, fmt.Sprintf("sha-%d", round), status, now)
		run.Kind = model.ReviewKindVisual
		run.RoundNumber = round
		if err := e.svc.db.Save(run).Error; err != nil {
			t.Fatalf("save visual run: %v", err)
		}
		if result != "" {
			e.svc.db.Model(run).Update("result_json", result)
		}
	}
	mkVisual(1, model.ReviewRunCompleted,
		`{"summary":"v1","findings":[{"severity":"blocker","screenshot":"首页","dimension":"spacing","body":"间距错乱","fix":"改 padding"}],"prior":[],"unmeasurable":[]}`)
	// round2 只有代码运行（无视觉）。
	e.seedRun(t, repo, 81, "sha-2", model.ReviewRunCompleted, now)
	cur := e.seedRun(t, repo, 81, "sha-3", model.ReviewRunDispatched, now)
	e.svc.db.Model(cur).Updates(map[string]any{"kind": model.ReviewKindVisual, "round_number": 3})
	cur.Kind = model.ReviewKindVisual
	cur.RoundNumber = 3

	content, err := e.review.visualHistory(context.Background(), cur)
	if err != nil {
		t.Fatalf("visualHistory: %v", err)
	}
	if !strings.Contains(content, "轮次 1") || !strings.Contains(content, "间距错乱") {
		t.Fatalf("history should use round-1 visual runs: %s", content)
	}
}

// TestUpdateSettingsTriggerFalseSticks F9：首次显式保存 false 的触发位不被
// 列默认（default:true 时代的 GORM Create 零值替换）写成 true。
func TestUpdateSettingsTriggerFalseSticks(t *testing.T) {
	svc := setupTestServices(t)
	review := newReviewTestService(t, svc, nil, nil, nil)
	user := createTestUser(t, svc.db, "setter")
	project := createTestProject(t, svc.db, user.ID)

	// enabled=false：跳过前置校验、允许零组合，但触发位必须原样落库。
	if _, err := review.UpdateSettings(context.Background(), user.ID, project.ID,
		&api.ReviewSettingsInput{
			Enabled:               false,
			TriggerOpened:         false,
			TriggerSynchronize:    false,
			TriggerReadyForReview: true,
			TriggerReopened:       false,
		}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	out, err := review.GetSettings(context.Background(), user.ID, project.ID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if out.TriggerOpened || out.TriggerSynchronize {
		t.Fatalf("triggers = opened:%v sync:%v, want false/false", out.TriggerOpened, out.TriggerSynchronize)
	}
	if !out.TriggerReadyForReview {
		t.Fatal("trigger_ready_for_review should stay true")
	}
}

// TestParseDiffRightLinesTrailingNewline F14：diff 末尾空行不算右侧有效行。
func TestParseDiffRightLinesTrailingNewline(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n ctx\n+added\n"
	valid := parseDiffRightLines(diff)
	got := valid["a.go"]
	if len(got) != 2 || !got[1] || !got[2] {
		t.Fatalf("valid lines = %v, want {1,2}", got)
	}
}

// TestCleanupEmptyBranchMatchesByPath F4 回归：线程投影的 branch 可空——空时
// 只凭「路径命中登记」认定归属，不再被 ref 名比较恒不等打成永久 failed。
func TestCleanupEmptyBranchMatchesByPath(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-nobranch")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{
		worktrees: []T3Worktree{{Path: "/wt/x", RefNames: []string{"whatever"}}},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 62, env, model.ReviewRunCompleted, "", now)
	t3.threadWorktrees = map[string]T3ThreadWorktree{
		run.ThreadID: {Path: "/wt/x"}, // branch 为空
	}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || len(t3.removed) != 1 {
		t.Fatalf("worktree = %q removed=%v, want removed 且删除（空分支按路径核验）",
			after.WorktreeState, t3.removed)
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("空分支不应发失败通知")
	}
}

// TestCleanupTerminalRowsNotClaimers F7 回归：removed/failed 行残留的
// worktree_path 不再算认领者，同路径的后续合法属主正常回收。
func TestCleanupTerminalRowsNotClaimers(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-staleclaim")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// 历史终态行：同路径、已 removed——只是残留快照，不得当认领者。
	e.seedDispatchedRun(t, repo, 63, model.ReviewKindCode, model.ReviewRunCompleted, "/wt/recycled", true, now)
	e.svc.db.Model(&model.ReviewRun{}).Where("worktree_path = '/wt/recycled'").
		Update("worktree_state", model.ReviewWorktreeRemoved)

	t3 := &fakeReviewT3{
		threadWorktrees: map[string]T3ThreadWorktree{
			// 当前运行复用同一路径（分支不同无妨，核验按 ref 名集合）。
			"th-current": {Path: "/wt/recycled", Branch: "br-new"},
		},
		worktrees: []T3Worktree{{Path: "/wt/recycled", RefNames: []string{"br-new"}}},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 64, env, model.ReviewRunCompleted, "", now)
	e.svc.db.Model(run).Update("thread_id", "th-current")
	run.ThreadID = "th-current"
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeRemoved || len(t3.removed) != 1 {
		t.Fatalf("worktree = %q removed=%v, want removed（终态行不算认领者）",
			after.WorktreeState, t3.removed)
	}
}

// TestCleanupTriesPerReason F8 回归：重试计数按原因独立——404 两次不占用
// 「读失败」的宽限；若仍是单一计数，第 3 个 tick 就会提前落终态。
func TestCleanupTriesPerReason(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-mixtry")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{
		threadWTErrs: []error{errT3ThreadNotFound, errT3ThreadNotFound},
		threadWTErr:  errors.New("upstream boom"),
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedCleanableRun(t, repo, 65, env, model.ReviewRunCompleted, "", now)
	// 两个 tick 404（notfound 计数）+ 一个 tick 读失败（thread 计数=1）：
	// 任一原因都还没到上限，状态必须原样不动。
	for i := 0; i < reviewWorktreeMaxTries; i++ {
		review.cleanupRunWorktree(run, now)
		var mid model.ReviewRun
		e.svc.db.First(&mid, "id = ?", run.ID)
		if mid.WorktreeState != model.ReviewWorktreeNone {
			t.Fatalf("tick %d worktree_state = %q, want none（原因混算会提前终态）", i+1, mid.WorktreeState)
		}
	}
	// 再两次读失败到该原因上限：failed + 通知。
	for i := 0; i < reviewWorktreeMaxTries-1; i++ {
		review.cleanupRunWorktree(run, now)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed ||
		!strings.Contains(after.WorktreeError, "读取线程工作区失败") {
		t.Fatalf("worktree = %q/%q, want failed+读失败原因", after.WorktreeState, after.WorktreeError)
	}
	if e.cleanupFailedCount(t) != 1 {
		t.Fatal("到上限应发一次失败通知")
	}
}

// TestCleanupNotFoundCounterResetsAfterSuccess 404 计数按「连续失败」语义：
// 中途成功读到线程投影就清零。否则「404 → 成功（但登记读失败）→ 404 → …」
// 的交错序列会让累计 404 提前命中上限，把已确认存在的工作区静默标 removed。
func TestCleanupNotFoundCounterResetsAfterSuccess(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-404reset")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	run := e.seedCleanableRun(t, repo, 66, env, model.ReviewRunCompleted, "", now)

	t3 := &fakeReviewT3{
		threadWTErrs: []error{errT3ThreadNotFound, errT3ThreadNotFound},
		threadWorktrees: map[string]T3ThreadWorktree{
			run.ThreadID: {Path: "/wt/404-reset", Branch: "br-404"},
		},
		worktrees: []T3Worktree{{Path: "/wt/404-reset", RefNames: []string{"br-404"}}},
		listErr:   errors.New("registry down"),
	}
	review := e.withT3(t3, nil, nil)

	// tick1/2：两个 404（累计 2，未到上限）。
	review.cleanupRunWorktree(run, now)
	review.cleanupRunWorktree(run, now)
	// tick3：成功读到线程投影（404 计数应清零），登记读取失败进入 registry 重试。
	review.cleanupRunWorktree(run, now)
	// tick4：再一个 404。若 404 计数没随成功清零，这里就是第 3 次 → removed。
	t3.threadWTErrs = []error{errT3ThreadNotFound}
	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeNone {
		t.Fatalf("worktree_state = %q, want none（成功读取后 404 计数必须清零）", after.WorktreeState)
	}
	if len(t3.removed) != 0 {
		t.Fatalf("不得删除，removed = %v", t3.removed)
	}
}
