package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
)

func seedGitHubDoneIssuesForBatchClose(t *testing.T, svc *testServices, projectID string, n int, fake *fakeIssueGitHubClient) []model.IssueGitHubMeta {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	seeds := make([]listFixtureIssue, 0, n)
	for i := 1; i <= n; i++ {
		seeds = append(seeds, listFixtureIssue{
			seq: i, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: fmt.Sprintf("gh %d", i), author: "alice",
			updated: now.Add(time.Duration(i) * time.Second), created: now,
			gh:       &listFixtureGHMeta{number: 100 + i},
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone),
		})
	}
	seedListFixtureIssues(t, svc, projectID, seeds)

	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", projectID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load github metas: %v", err)
	}
	if len(metas) != n {
		t.Fatalf("expected %d github metas, got %d", n, len(metas))
	}
	closedAt := now.Add(time.Minute)
	fake.updatedIssuesByNumber = make(map[int]*ghclient.Issue, n)
	for _, m := range metas {
		item := testGitHubSyncIssue(m.GitHubIssueID, m.Number, closedAt)
		item.State = stringPtr("closed")
		item.StateReason = stringPtr("completed")
		item.ClosedAt = &gh.Timestamp{Time: closedAt}
		fake.updatedIssuesByNumber[m.Number] = item
	}
	return metas
}

func TestIssueServiceBatchCloseDoneIssues_InternalDeletesRecommendationsInOneShot(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	now := time.Now().UTC()
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done one", author: "alice",
			updated: now, created: now,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 2, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done two", author: "alice",
			updated: now.Add(time.Second), created: now,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
	})

	var issues []model.Issue
	if err := svc.db.Where("project_id = ?", project.ID).Order("sequence_number").Find(&issues).Error; err != nil {
		t.Fatalf("load issues: %v", err)
	}
	for _, issue := range issues {
		rec := &model.IssueRecommendation{
			IssueID:   issue.ID,
			ProjectID: project.ID,
			Reason:    "close me",
			Priority:  model.IssueRecommendationPriorityMedium,
			CreatedBy: user.ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := svc.db.Create(rec).Error; err != nil {
			t.Fatalf("seed recommendation: %v", err)
		}
		dep := &model.RecommendationDependency{
			IssueID:    issue.ID,
			DepIssueID: issue.ID,
			Position:   0,
			CreatedAt:  now,
		}
		if err := svc.db.Create(dep).Error; err != nil {
			t.Fatalf("seed recommendation dep: %v", err)
		}
	}

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceInternal))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Total != 2 || resp.Succeeded != 2 || resp.Failed != 0 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}

	var open int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateOpen).Count(&open).Error; err != nil {
		t.Fatalf("count open: %v", err)
	}
	if open != 0 {
		t.Fatalf("expected no open issues, got %d", open)
	}
	var recs int64
	if err := svc.db.Model(&model.IssueRecommendation{}).Where("project_id = ?", project.ID).Count(&recs).Error; err != nil {
		t.Fatalf("count recs: %v", err)
	}
	if recs != 0 {
		t.Fatalf("expected recommendations deleted, got %d", recs)
	}
	var deps int64
	if err := svc.db.Model(&model.RecommendationDependency{}).Count(&deps).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if deps != 0 {
		t.Fatalf("expected recommendation deps deleted, got %d", deps)
	}
}

func TestIssueServiceBatchCloseDoneIssues_GitHubClosesConcurrently(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 200
	const delay = 20 * time.Millisecond
	fake := &fakeIssueGitHubClient{updateIssueDelay: delay}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}
	seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, n, fake)

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Total != n || resp.Succeeded != n || resp.Failed != 0 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}
	if got := len(fake.recordedUpdateIssueCalls()); got != n {
		t.Fatalf("expected %d github updates, got %d", n, got)
	}
	// 只断言并发度（INT-185）：墙钟对比对负载敏感，改为看 UpdateIssue 瞬时
	// 并发峰值是否达到 worker 池的水平。
	if maxConcurrent := atomic.LoadInt32(&fake.updateIssueMax); maxConcurrent < batchCloseGitHubConcurrency {
		t.Fatalf("expected concurrent UpdateIssue up to %d, max=%d", batchCloseGitHubConcurrency, maxConcurrent)
	}
}

func TestIssueServiceBatchCloseDoneIssues_CancelStopsLaterGitHubUpdates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 20
	// 确定性同步（INT-185）：worker 阻塞在 onUpdateIssue 钩子时仍占用
	// semaphore 槽位，用 started/release 两个通道精确钉住「第一波全部进入
	// PATCH、尚未返回」的时刻，不依赖墙钟窗口。
	started := make(chan struct{}, n)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHooks := func() { releaseOnce.Do(func() { close(release) }) }
	fake := &fakeIssueGitHubClient{}
	fake.onUpdateIssue = func(_ context.Context, _ int) {
		started <- struct{}{}
		<-release
	}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}
	seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, n, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		resp *BatchCloseDoneIssuesResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := svc.issueService.BatchCloseDoneIssues(ctx, project.ID, user.ID, string(model.IssueSourceGitHub))
		done <- outcome{resp: resp, err: err}
	}()

	// 失败路径（如等 started 超时 t.Fatal）也要先取消批次再放行钩子里的
	// worker，让批次 goroutine 有界退出，不把阻塞在钩子上的 goroutine 拖到
	// 测试结束之后继续碰共享的测试 DB。
	batchExited := false
	t.Cleanup(func() {
		cancel()
		releaseHooks()
		if batchExited {
			return
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	// 第一波全部进入 PATCH（占满全部槽位）后取消：在途请求经 WithoutCancel +
	// 独立超时不受整批取消影响，必须继续走完并落库；未开始的项不再发请求。
	for i := 0; i < batchCloseGitHubConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("UpdateIssue never reached concurrency")
		}
	}
	cancel()
	releaseHooks()

	got := <-done
	batchExited = true
	if got.err != nil {
		t.Fatalf("batch close: %v", got.err)
	}

	if got.resp.Succeeded != batchCloseGitHubConcurrency {
		t.Fatalf("expected in-flight %d closes to persist, got Succeeded=%d", batchCloseGitHubConcurrency, got.resp.Succeeded)
	}
	if got.resp.Failed != n-batchCloseGitHubConcurrency || len(got.resp.Failures) != n-batchCloseGitHubConcurrency {
		t.Fatalf("expected %d never-started failures, got %+v", n-batchCloseGitHubConcurrency, got.resp)
	}
	for _, f := range got.resp.Failures {
		// 未开始的项必须是「未开始关闭」语义，不能是「结果未确认」。
		if f.Error != "请求已取消或超时，未继续关闭" {
			t.Fatalf("expected never-started copy for %s, got %q", f.Id, f.Error)
		}
	}

	calls := len(fake.recordedUpdateIssueCalls())
	if calls != batchCloseGitHubConcurrency {
		t.Fatalf("expected calls to stop at the first wave, got %d", calls)
	}

	var stillOpen int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateOpen).Count(&stillOpen).Error; err != nil {
		t.Fatalf("count open: %v", err)
	}
	if stillOpen != n-batchCloseGitHubConcurrency {
		t.Fatalf("expected exactly %d github issues to stay open after cancel, got %d", n-batchCloseGitHubConcurrency, stillOpen)
	}
	var closed int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateClosed).Count(&closed).Error; err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if closed != batchCloseGitHubConcurrency {
		t.Fatalf("expected %d in-flight closes to land in db, got %d", batchCloseGitHubConcurrency, closed)
	}
	if got.resp.Succeeded+got.resp.Failed != n {
		t.Fatalf("succeeded+failed should cover total, got %+v", got.resp)
	}
}

// INT-185：Acquire 成功不等于 worker 已调度——批次取消后、PATCH 开始前的
// worker 必须直接放弃，不再发新请求（WithoutCancel 只豁免已在途的请求）。
func TestCloseOneGitHubIssue_SkipsPatchWhenBatchCanceled(t *testing.T) {
	fake := &fakeIssueGitHubClient{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 批次已取消

	results := make(chan githubCloseNetResult, 1)
	issue := model.Issue{Source: model.IssueSourceGitHub, GitHubMeta: &model.IssueGitHubMeta{Number: 7}}
	closeOneGitHubIssue(ctx, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil, nil)

	res := <-results
	if res.err == nil || !errors.Is(res.err, context.Canceled) {
		t.Fatalf("expected canceled err, got %+v", res.err)
	}
	if got := batchCloseItemError(res.err); got != "请求已取消或超时，未继续关闭" {
		t.Fatalf("expected never-started copy, got %q", got)
	}
	if calls := len(fake.recordedUpdateIssueCalls()); calls != 0 {
		t.Fatalf("canceled batch must not dispatch PATCH, got %d calls", calls)
	}
}

// INT-185（复审 R2）：命中首个限流即熔断——剩余项不再派发，逐条记「未尝试」。
// 钩子门控与 CancelStopsLater 同构：4 个 worker 全部进入 PATCH 后统一放行，
// 每个被放行 worker 都必经 trip（全部命中限流），槽位释放只发生在 trip 之后，
// 因此 calls==4 与文案分布是确定性事实。在途请求经 WithoutCancel 落库的语义
// 由 CancelStopsLater 覆盖；限流 trip 与相邻 worker 槽位释放之间没有跨
// goroutine 的 happen-before，熔断按「尽力同时」实现，重叠窗口上限为并发度。
func TestIssueServiceBatchCloseDoneIssues_RateLimitStopsRemainingDispatch(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 20
	started := make(chan struct{}, n)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHooks := func() { releaseOnce.Do(func() { close(release) }) }
	fake := &fakeIssueGitHubClient{}
	fake.onUpdateIssue = func(_ context.Context, _ int) {
		started <- struct{}{}
		<-release
	}
	metas := seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, n, fake)
	errs := make(map[int]error, n)
	for _, m := range metas {
		errs[m.Number] = &gh.RateLimitError{Message: "API rate limit exceeded for 1.2.3.4."}
	}
	fake.updateIssueErrs = errs
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		resp *BatchCloseDoneIssuesResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := svc.issueService.BatchCloseDoneIssues(ctx, project.ID, user.ID, string(model.IssueSourceGitHub))
		done <- outcome{resp: resp, err: err}
	}()
	batchExited := false
	t.Cleanup(func() {
		cancel()
		releaseHooks()
		if batchExited {
			return
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	for i := 0; i < batchCloseGitHubConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("UpdateIssue never reached concurrency")
		}
	}
	releaseHooks()

	got := <-done
	batchExited = true
	if got.err != nil {
		t.Fatalf("batch close: %v", got.err)
	}
	resp := got.resp
	if calls := len(fake.recordedUpdateIssueCalls()); calls != batchCloseGitHubConcurrency {
		t.Fatalf("expected dispatch to stop at first wave, got %d calls", calls)
	}
	if resp.Succeeded != 0 || resp.Failed != n {
		t.Fatalf("unexpected result: %+v", resp)
	}
	var limited, remaining int
	for _, f := range resp.Failures {
		switch f.Error {
		case batchCloseGitHubRateLimitText:
			limited++
		case batchCloseGitHubRateLimitRemainingText:
			remaining++
		default:
			t.Fatalf("unexpected failure copy for %s: %q", f.Id, f.Error)
		}
	}
	if limited != batchCloseGitHubConcurrency || remaining != n-batchCloseGitHubConcurrency {
		t.Fatalf("expected %d limited + %d remaining, got %d + %d", batchCloseGitHubConcurrency, n-batchCloseGitHubConcurrency, limited, remaining)
	}
	var closed int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateClosed).Count(&closed).Error; err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if closed != 0 {
		t.Fatalf("rate-limited closes must not persist, got %d", closed)
	}
}

// INT-185（复审 R3 审计）：GitHub 错误文案规范化——5xx 与传输错误按「结果
// 未确认」，4xx 按「<状态码> <Message>」输出且不含 URL/Issue 号；同类错误
// 逐字相同，前端按文案分组计数才有效。
func TestIssueServiceBatchCloseDoneIssues_GitHubErrorCopyNormalization(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	fake := &fakeIssueGitHubClient{}
	metas := seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, 3, fake)
	ghResp := func(status int) *http.Response { return &http.Response{StatusCode: status} }
	fake.updateIssueErrs = map[int]error{
		metas[0].Number: &gh.ErrorResponse{Message: "Internal Server Error", Response: ghResp(http.StatusBadGateway)},
		metas[1].Number: &gh.ErrorResponse{Message: "Not Found", Response: ghResp(http.StatusNotFound)},
		metas[2].Number: &gh.ErrorResponse{Message: "Not Found", Response: ghResp(http.StatusNotFound)},
	}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Succeeded != 0 || resp.Failed != 3 || len(resp.Failures) != 3 {
		t.Fatalf("unexpected result: %+v", resp)
	}
	copies := map[string]int{}
	for _, f := range resp.Failures {
		copies[f.Error]++
	}
	if got := copies["关闭结果未确认：GitHub 侧可能已关闭，请刷新查看"]; got != 1 {
		t.Fatalf("502 must be classified as unconfirmed once, got %d in %v", got, copies)
	}
	notFound := "更新 GitHub Issue 失败：404 Not Found"
	if got := copies[notFound]; got != 2 {
		t.Fatalf("identical 404 copies must dedupe to one group ×2, got %d in %v", got, copies)
	}
	if strings.Contains(notFound, "repos/") || strings.Contains(notFound, "https://") {
		t.Fatalf("normalized copy must not carry URL, got %q", notFound)
	}
}

// INT-185（复审 R3 审计）：传输错误（非 ErrorResponse）结果未知，按「结果
// 未确认」处理。
func TestClassifyGitHubCloseError(t *testing.T) {
	ghResp := func(status int) *http.Response { return &http.Response{StatusCode: status} }
	var unconfirmed *batchClosePatchUnconfirmedError
	if e := classifyGitHubCloseError(&gh.ErrorResponse{Message: "oops", Response: ghResp(http.StatusBadGateway)}); !errors.As(e, &unconfirmed) {
		t.Fatalf("5xx must be unconfirmed, got %v", e)
	}
	if e := classifyGitHubCloseError(errors.New("connection reset by peer")); !errors.As(e, &unconfirmed) {
		t.Fatalf("transport error must be unconfirmed, got %v", e)
	}
	if e := classifyGitHubCloseError(&gh.ErrorResponse{Response: ghResp(http.StatusNotFound)}); batchCloseItemError(e) != "更新 GitHub Issue 失败：404" {
		t.Fatalf("empty message must not leave trailing space, got %q", batchCloseItemError(e))
	}
}

// INT-185（复审 R4）：明细上限不能吞掉后续类别——先占满名额的类别之后，
// 新类别的样例仍要进明细（每类至少出现一次），同类第二条不再追加。
func TestAppendBatchCloseFailure_ReservesFirstOfEachCategoryPastCap(t *testing.T) {
	resp := &BatchCloseDoneIssuesResponse{Failures: make([]BatchCloseDoneIssueFailure, 0)}
	for i := 0; i < batchCloseDoneMaxFailures; i++ {
		appendBatchCloseFailure(resp, model.Issue{ID: fmt.Sprintf("id-%d", i)}, errBatchCloseSkippedNotOpen)
	}
	appendBatchCloseFailure(resp, model.Issue{ID: "more-skip"}, errBatchCloseSkippedNotOpen)
	if len(resp.Failures) != batchCloseDoneMaxFailures {
		t.Fatalf("same-category past cap must not append, got %d entries", len(resp.Failures))
	}
	limitErr := errs.New(errs.ErrGitHubAPI.Code, batchCloseGitHubRateLimitText)
	appendBatchCloseFailure(resp, model.Issue{ID: "rate-1"}, limitErr)
	appendBatchCloseFailure(resp, model.Issue{ID: "rate-2"}, limitErr)
	if len(resp.Failures) != batchCloseDoneMaxFailures+1 {
		t.Fatalf("new-category sample must append past cap exactly once, got %d", len(resp.Failures))
	}
	last := resp.Failures[len(resp.Failures)-1]
	if last.Id != "rate-1" || last.Error != batchCloseGitHubRateLimitText {
		t.Fatalf("expected rate-1 sample kept, got %+v", last)
	}
}

// INT-185（复审 R5）：派发阶段的 ctx err 按当时的熔断状态当场定死归因，
// drain 阶段不再重读全局标志——同一批次的失败原因不随 goroutine 时序漂移。
func TestBatchCloseCancelErr_AttributedAtProductionTime(t *testing.T) {
	var flag atomic.Bool
	// 未熔断：保留原始 ctx err（batchCloseItemError 走「未开始」文案）。
	got := batchCloseCancelErr(context.DeadlineExceeded, &flag)
	if got != context.DeadlineExceeded {
		t.Fatalf("untripped flag must keep raw ctx err, got %v", got)
	}
	if batchCloseItemError(got) != "请求已取消或超时，未继续关闭" {
		t.Fatalf("unexpected copy: %q", batchCloseItemError(got))
	}
	// 熔断已触发：当场归因为限流未尝试。
	flag.Store(true)
	if batchCloseItemError(batchCloseCancelErr(context.DeadlineExceeded, &flag)) != batchCloseGitHubRateLimitRemainingText {
		t.Fatalf("tripped flag must attribute to rate limit remaining")
	}
	// nil 标志等价于未熔断。
	if got := batchCloseCancelErr(context.Canceled, nil); got != context.Canceled {
		t.Fatalf("nil flag must keep raw ctx err, got %v", got)
	}
}

// INT-185（复审 R7）：发 PATCH 前按 id 复核本地行仍 open+done——被拖回开
// 发中的行不发 PATCH、按「已跳过关闭」回报；复核出错也不发 PATCH。
func TestCloseOneGitHubIssue_GuardSkipsBeforePatch(t *testing.T) {
	issue := model.Issue{Source: model.IssueSourceGitHub, GitHubMeta: &model.IssueGitHubMeta{Number: 9}}

	fake := &fakeIssueGitHubClient{}
	results := make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(context.Background(), fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil,
		func(ctx context.Context, id string) (bool, error) { return false, nil })
	res := <-results
	if res.err != errBatchCloseSkippedNotOpen {
		t.Fatalf("expected skipped-not-open, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("guard must stop the PATCH")
	}

	results = make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(context.Background(), fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil,
		func(ctx context.Context, id string) (bool, error) { return false, errors.New("db down") })
	res = <-results
	if res.err == nil || batchCloseItemError(res.err) != "复核本地状态失败，未关闭该项" {
		t.Fatalf("expected guard-failure copy, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("guard error must stop the PATCH")
	}

	// 守卫通过时照常发 PATCH。
	results = make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(context.Background(), fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil,
		func(ctx context.Context, id string) (bool, error) { return true, nil })
	res = <-results
	if res.err != nil {
		t.Fatalf("guard-passed item must PATCH, got err=%v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 1 {
		t.Fatalf("expected exactly one PATCH, got %d", len(fake.recordedUpdateIssueCalls()))
	}

	// 守卫通过但批次已被取消：守卫后再复核（R8），按「未开始」计数不发 PATCH。
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	results = make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(cancelledCtx, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil,
		func(ctx context.Context, id string) (bool, error) { return true, nil })
	res = <-results
	if res.err == nil {
		t.Fatal("cancelled batch must not PATCH")
	}
	if len(fake.recordedUpdateIssueCalls()) != 1 {
		t.Fatalf("cancelled batch must not issue another PATCH, got %d", len(fake.recordedUpdateIssueCalls()))
	}
}

// INT-185（复审 R9）：守卫在途时批次被取消/熔断——守卫因带 batchCtx 返回
// ctx err，要按「未开始/限流」归因，不能误报为「复核本地状态失败」。
func TestCloseOneGitHubIssue_GuardCtxCancelAttribution(t *testing.T) {
	issue := model.Issue{Source: model.IssueSourceGitHub, GitHubMeta: &model.IssueGitHubMeta{Number: 11}}
	fake := &fakeIssueGitHubClient{}

	// 未熔断：守卫返回 ctx.Canceled（批次被用户取消/超时），按取消归因。
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var rateLimited atomic.Bool
	results := make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(cancelledCtx, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, &rateLimited,
		func(ctx context.Context, id string) (bool, error) { return false, ctx.Err() })
	res := <-results
	if res.err == nil || !errors.Is(res.err, context.Canceled) {
		t.Fatalf("guard ctx-cancel must attribute to cancellation (ctx err passthrough), got %v", res.err)
	}

	// 熔断中：守卫返回 DeadlineExceeded（限流熔断或整批截止），归限流文案。
	rateLimited.Store(true)
	timedOut, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()
	results = make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(timedOut, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, &rateLimited,
		func(ctx context.Context, id string) (bool, error) { return false, ctx.Err() })
	res = <-results
	if res.err == nil || batchCloseItemError(res.err) != batchCloseGitHubRateLimitRemainingText {
		t.Fatalf("guard ctx-deadline under fuse must attribute to rate-limit, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("ctx-cancelled guard must never PATCH")
	}
}

func TestIssueServiceBatchCloseDoneIssues_MixedSourcesGitHubFailureKeepsInternalClosed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	now := time.Now().UTC()
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "internal done", author: "alice",
			updated: now, created: now,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 2, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "github done", author: "alice",
			updated: now.Add(time.Second), created: now,
			gh:       &listFixtureGHMeta{number: 42},
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
	})

	fake := &fakeIssueGitHubClient{
		updateIssueErrs: map[int]error{42: errors.New("github down")},
	}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, "")
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Total != 2 || resp.Succeeded != 1 || resp.Failed != 1 || len(resp.Failures) != 1 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}

	var internal model.Issue
	if err := svc.db.Where("project_id = ? AND source = ?", project.ID, model.IssueSourceInternal).First(&internal).Error; err != nil {
		t.Fatalf("load internal: %v", err)
	}
	if internal.State != model.IssueStateClosed || internal.StateReason != "completed" {
		t.Fatalf("internal should be closed, got %+v", internal)
	}
	var github model.Issue
	if err := svc.db.Where("project_id = ? AND source = ?", project.ID, model.IssueSourceGitHub).First(&github).Error; err != nil {
		t.Fatalf("load github: %v", err)
	}
	if github.State != model.IssueStateOpen {
		t.Fatalf("failed github issue should stay open, got %+v", github)
	}
}

// INT-185：行在批量关闭查询之后、事务之前被并发方关闭（已非 open）时，
// 不计入成功、不误删推荐，按失败逐条回报。
func TestIssueServiceBatchCloseDoneIssues_InternalSkipsAlreadyClosed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	now := time.Now().UTC()
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done one", author: "alice",
			updated: now, created: now,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
	})

	var issues []model.Issue
	if err := svc.db.Where("project_id = ?", project.ID).Find(&issues).Error; err != nil {
		t.Fatalf("load issues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	issue := issues[0]

	rec := &model.IssueRecommendation{
		IssueID: issue.ID, ProjectID: project.ID, Reason: "keep me",
		Priority: model.IssueRecommendationPriorityMedium, CreatedBy: user.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(rec).Error; err != nil {
		t.Fatalf("seed recommendation: %v", err)
	}
	dep := &model.RecommendationDependency{IssueID: issue.ID, DepIssueID: issue.ID, Position: 0, CreatedAt: now}
	if err := svc.db.Create(dep).Error; err != nil {
		t.Fatalf("seed recommendation dep: %v", err)
	}

	// 模拟并发方提前关闭：行已非 open，事务内的 UPDATE 不应命中。
	closedAt := now.Add(time.Minute)
	if _, err := svc.issueRepo.BatchCloseInternalTx(svc.db, []string{issue.ID}, closedAt); err != nil {
		t.Fatalf("pre-close issue: %v", err)
	}

	resp := &BatchCloseDoneIssuesResponse{Total: 1, Failures: make([]BatchCloseDoneIssueFailure, 0)}
	svc.issueService.closeInternalIssues(context.Background(), issues, resp)
	if resp.Succeeded != 0 || resp.Failed != 1 {
		t.Fatalf("expected 0 succeeded / 1 failed, got %+v", resp)
	}
	if len(resp.Failures) != 1 || resp.Failures[0].Id != issue.ID {
		t.Fatalf("expected failure on the already-closed issue, got %+v", resp.Failures)
	}
	if resp.Failures[0].Error != errBatchCloseSkippedNotOpen.Error() {
		t.Fatalf("unexpected failure copy: %q", resp.Failures[0].Error)
	}

	var got model.Issue
	if err := svc.db.Where("id = ?", issue.ID).First(&got).Error; err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if got.State != model.IssueStateClosed || got.StateReason != "completed" {
		t.Fatalf("expected the concurrent close to stand, got %+v", got)
	}
	var recs, deps int64
	if err := svc.db.Model(&model.IssueRecommendation{}).Where("issue_id = ?", issue.ID).Count(&recs).Error; err != nil {
		t.Fatalf("count recs: %v", err)
	}
	if err := svc.db.Model(&model.RecommendationDependency{}).Where("issue_id = ?", issue.ID).Count(&deps).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if recs != 1 || deps != 1 {
		t.Fatalf("expected recommendation to stay, got recs=%d deps=%d", recs, deps)
	}
}

// INT-185：取消文案分两类——未开始的取消与已发出 PATCH 的未确认结果。
func TestBatchCloseItemError_CancelCategories(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := batchCloseItemError(err); got != "请求已取消或超时，未继续关闭" {
			t.Fatalf("never-started cancel copy, got %q", got)
		}
	}
	unconfirmed := &batchClosePatchUnconfirmedError{err: context.DeadlineExceeded}
	if got := batchCloseItemError(unconfirmed); got != "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" {
		t.Fatalf("unconfirmed copy, got %q", got)
	}
	if !errors.Is(unconfirmed, context.DeadlineExceeded) {
		t.Fatal("unconfirmed error must unwrap to the underlying ctx err")
	}
}

// INT-185：批量关闭识别 GitHub 限流，各类型命中与不命中各测一条。
func TestIsGitHubRateLimitError(t *testing.T) {
	resp := func(status int) *http.Response { return &http.Response{StatusCode: status} }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"RateLimitError", &gh.RateLimitError{Message: "API rate limit exceeded for 1.2.3.4."}, true},
		{"AbuseRateLimitError", &gh.AbuseRateLimitError{
			Message:  "You have exceeded a secondary rate limit",
			Response: resp(http.StatusForbidden),
		}, true},
		{"ErrorResponse403RateLimitBody", &gh.ErrorResponse{
			Message:  "You have exceeded a secondary rate limit",
			Response: resp(http.StatusForbidden),
		}, true},
		{"ErrorResponse429RateLimitBody", &gh.ErrorResponse{
			Message:  "API rate limit exceeded",
			Response: resp(http.StatusTooManyRequests),
		}, true},
		{"ErrorResponse403RateLimitRawBody", &gh.ErrorResponse{
			Message: "",
			Response: func() *http.Response {
				r := resp(http.StatusForbidden)
				r.Body = io.NopCloser(strings.NewReader(`{"message":"abuse detection mechanism triggered"}`))
				return r
			}(),
		}, true},
		{"ErrorResponse403OtherBody", &gh.ErrorResponse{
			Message:  "Repository request blocked",
			Response: resp(http.StatusForbidden),
		}, false},
		{"ErrorResponse500RateLimitBody", &gh.ErrorResponse{
			Message:  "rate limit",
			Response: resp(http.StatusInternalServerError),
		}, false},
		{"ErrorResponseNilHTTPResponse", &gh.ErrorResponse{Message: "rate limit"}, false},
		{"PlainError", errors.New("rate limit exceeded"), false},
	}
	for _, tc := range cases {
		if got := isGitHubRateLimitError(tc.err); got != tc.want {
			t.Errorf("%s: isGitHubRateLimitError=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// INT-185：UpdateIssue 命中限流时 failures 的文案必须指向限流重试。
func TestIssueServiceBatchCloseDoneIssues_GitHubRateLimitFailureMessage(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	fake := &fakeIssueGitHubClient{}
	metas := seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, 1, fake)
	fake.updateIssueErrs = map[int]error{
		metas[0].Number: &gh.RateLimitError{Message: "API rate limit exceeded for 1.2.3.4."},
	}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Succeeded != 0 || resp.Failed != 1 || len(resp.Failures) != 1 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}
	if resp.Failures[0].Error != batchCloseGitHubRateLimitText {
		t.Fatalf("expected rate limit copy, got %q", resp.Failures[0].Error)
	}
}

// INT-185：upsertGitHubIssue 的入口守卫——nil 或缺 GitHub ID 的响应返回
// 错误而不是 panic，且不写任何行。
func TestIssueMirrorUpsertGitHubIssue_RejectsEmptyPayload(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	if _, err := svc.issueService.mirror.upsertGitHubIssue(project.ID, nil); err == nil {
		t.Fatal("nil payload must return an error, not panic")
	}
	missing := &ghclient.Issue{}
	if _, err := svc.issueService.mirror.upsertGitHubIssue(project.ID, missing); err == nil {
		t.Fatal("payload without GitHub ID must return an error, not panic")
	}
	var issues int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ?", project.ID).Count(&issues).Error; err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issues != 0 {
		t.Fatalf("guard must not write rows, got %d issues", issues)
	}
}
