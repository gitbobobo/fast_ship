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
	"go.uber.org/zap"
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
	closeOneGitHubIssue(ctx, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil, nil, nil)

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
// 每个被放行 worker 都必经 trip（全部命中限流），且每个触发 worker 都在释放
// 槽位前幂等 cancel（INT-191 R1），槽位释放只发生在取消完成之后，因此
// calls==4 与文案分布是确定性事实。在途请求经 WithoutCancel 落库的语义
// 由 CancelStopsLater 覆盖。
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

// INT-191：认证失败（401）与无权限（非限流 403）纳入熔断——剩余项不再派发，
// 触发项与未派发项按类别区分文案。钩子门控与 RateLimitStopsRemainingDispatch
// 同构：4 个 worker 全部进入 PATCH 后统一放行，且每个触发 worker 都在释放槽
// 位前幂等 cancel（INT-191 R1），calls==并发度与文案分布是确定性事实。
// 文案在这里逐字断言，锁死稳定性（前端按文案分组去重）。
func TestIssueServiceBatchCloseDoneIssues_AuthFailureStopsRemainingDispatch(t *testing.T) {
	const n = 20
	cases := []struct {
		name      string
		patchErr  error
		itemText  string
		remaining string
	}{
		{
			name:      "401 trips with auth copy",
			patchErr:  &gh.ErrorResponse{Message: "Bad credentials", Response: &http.Response{StatusCode: http.StatusUnauthorized}},
			itemText:  "更新 GitHub Issue 失败：GitHub 认证失败，请检查凭证",
			remaining: "GitHub 认证失败，未尝试关闭",
		},
		{
			name:      "non-ratelimit 403 trips with forbidden copy",
			patchErr:  &gh.ErrorResponse{Message: "Repository request blocked", Response: &http.Response{StatusCode: http.StatusForbidden}},
			itemText:  "更新 GitHub Issue 失败：GitHub 仓库无权限，请检查授权",
			remaining: "GitHub 仓库无权限，未尝试关闭",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := setupTestServices(t)
			user := createTestUser(t, svc.db, "user-1")
			project := createTestProject(t, svc.db, user.ID)

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
			errsByNumber := make(map[int]error, n)
			for _, m := range metas {
				errsByNumber[m.Number] = tc.patchErr
			}
			fake.updateIssueErrs = errsByNumber
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
			var tripped, remaining int
			for _, f := range resp.Failures {
				switch f.Error {
				case tc.itemText:
					tripped++
				case tc.remaining:
					remaining++
				default:
					t.Fatalf("unexpected failure copy for %s: %q", f.Id, f.Error)
				}
			}
			if tripped != batchCloseGitHubConcurrency || remaining != n-batchCloseGitHubConcurrency {
				t.Fatalf("expected %d tripped + %d remaining, got %d + %d", batchCloseGitHubConcurrency, n-batchCloseGitHubConcurrency, tripped, remaining)
			}
			var closed int64
			if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateClosed).Count(&closed).Error; err != nil {
				t.Fatalf("count closed: %v", err)
			}
			if closed != 0 {
				t.Fatalf("aborted closes must not persist, got %d", closed)
			}
		})
	}
}

// INT-191：404 是单条 Issue 状态（已转移/删除），不熔断——整批逐条 PATCH，
// 逐条按规范化文案回报，不出现「未尝试」类熔断文案。
func TestIssueServiceBatchCloseDoneIssues_NotFoundDoesNotTripDispatch(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 10 // 多于并发度，超发波次证明熔断未截断派发
	fake := &fakeIssueGitHubClient{}
	metas := seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, n, fake)
	errsByNumber := make(map[int]error, n)
	for _, m := range metas {
		errsByNumber[m.Number] = &gh.ErrorResponse{Message: "Not Found", Response: &http.Response{StatusCode: http.StatusNotFound}}
	}
	fake.updateIssueErrs = errsByNumber
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if calls := len(fake.recordedUpdateIssueCalls()); calls != n {
		t.Fatalf("404 must not trip dispatch, expected %d PATCH calls, got %d", n, calls)
	}
	if resp.Succeeded != 0 || resp.Failed != n {
		t.Fatalf("unexpected result: %+v", resp)
	}
	for _, f := range resp.Failures {
		if f.Error != "更新 GitHub Issue 失败：404 Not Found" {
			t.Fatalf("unexpected 404 copy for %s: %q", f.Id, f.Error)
		}
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
// INT-191：归因按触发类别取「未尝试」文案，限流/认证/权限各不相同。
func TestBatchCloseCancelErr_AttributedAtProductionTime(t *testing.T) {
	var trip atomic.Int32
	// 未熔断：保留原始 ctx err（batchCloseItemError 走「未开始」文案）。
	got := batchCloseCancelErr(context.DeadlineExceeded, &trip)
	if got != context.DeadlineExceeded {
		t.Fatalf("untripped flag must keep raw ctx err, got %v", got)
	}
	if batchCloseItemError(got) != "请求已取消或超时，未继续关闭" {
		t.Fatalf("unexpected copy: %q", batchCloseItemError(got))
	}
	// 熔断已触发：按触发类别当场归因为「未尝试」。
	trip.Store(int32(abortRateLimit))
	if batchCloseItemError(batchCloseCancelErr(context.DeadlineExceeded, &trip)) != batchCloseGitHubRateLimitRemainingText {
		t.Fatalf("tripped flag must attribute to rate limit remaining")
	}
	trip.Store(int32(abortAuth))
	if batchCloseItemError(batchCloseCancelErr(context.DeadlineExceeded, &trip)) != abortAuth.remainingText() {
		t.Fatalf("tripped flag must attribute to auth remaining")
	}
	trip.Store(int32(abortForbidden))
	if batchCloseItemError(batchCloseCancelErr(context.DeadlineExceeded, &trip)) != abortForbidden.remainingText() {
		t.Fatalf("tripped flag must attribute to forbidden remaining")
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
		func(ctx context.Context, id string) (bool, error) { return false, nil }, nil)
	res := <-results
	if res.err != errBatchCloseSkippedNotOpen {
		t.Fatalf("expected skipped-not-open, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("guard must stop the PATCH")
	}

	results = make(chan githubCloseNetResult, 1)
	closeOneGitHubIssue(context.Background(), fake, issue, ghclient.UpdateIssueRequest{}, results, nil, nil,
		func(ctx context.Context, id string) (bool, error) { return false, errors.New("db down") }, nil)
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
		func(ctx context.Context, id string) (bool, error) { return true, nil }, nil)
	res = <-results
	if res.err != nil {
		t.Fatalf("guard-passed item must PATCH, got err=%v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 1 {
		t.Fatalf("expected exactly one PATCH, got %d", len(fake.recordedUpdateIssueCalls()))
	}

}

// INT-185（复审 R9/R10）：守卫真正执行时批次被取消/熔断——守卫因带
// batchCtx 返回 ctx err，要按「未开始/熔断类别」归因，不能误报为「复核本地
// 状态失败」。守卫阻塞到批次取消后才返回 ctx.Err()，保证进入守卫出错
// 分支而非顶部 batchCtx.Err() 提前返回（R10 Note：之前用例在调用前就
// cancel，guard 从未执行）。
func TestCloseOneGitHubIssue_GuardCtxCancelAttribution(t *testing.T) {
	issue := model.Issue{Source: model.IssueSourceGitHub, GitHubMeta: &model.IssueGitHubMeta{Number: 11}}
	fake := &fakeIssueGitHubClient{}

	// 守卫执行中批次被取消（未熔断）：守卫返回 ctx.Canceled，按取消归因。
	batchCtx, cancel := context.WithCancel(context.Background())
	guardStarted := make(chan struct{})
	var trip atomic.Int32
	results := make(chan githubCloseNetResult, 1)
	go func() {
		closeOneGitHubIssue(batchCtx, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, &trip,
			func(ctx context.Context, id string) (bool, error) {
				close(guardStarted)
				<-ctx.Done() // 阻塞到批次取消后返回 ctx err
				return false, ctx.Err()
			}, nil)
	}()
	<-guardStarted
	cancel()
	res := <-results
	if res.err == nil || !errors.Is(res.err, context.Canceled) {
		t.Fatalf("guard ctx-cancel must attribute to cancellation (ctx err passthrough), got %v", res.err)
	}

	// 守卫执行中批次被限流熔断：守卫返回 DeadlineExceeded，归限流文案。
	batchCtx2, cancel2 := context.WithCancel(context.Background())
	guardStarted2 := make(chan struct{})
	var trip2 atomic.Int32
	trip2.Store(int32(abortRateLimit))
	results = make(chan githubCloseNetResult, 1)
	go func() {
		closeOneGitHubIssue(batchCtx2, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, &trip2,
			func(ctx context.Context, id string) (bool, error) {
				close(guardStarted2)
				<-ctx.Done()
				return false, context.DeadlineExceeded
			}, nil)
	}()
	<-guardStarted2
	cancel2()
	res = <-results
	if res.err == nil || batchCloseItemError(res.err) != batchCloseGitHubRateLimitRemainingText {
		t.Fatalf("guard ctx-deadline under fuse must attribute to rate-limit, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("ctx-cancelled guard must never PATCH")
	}

	// 守卫通过但返回后批次才取消：走守卫后第二次 batchCtx.Err() 复核，
	// 按「未开始」计数不发 PATCH（覆盖 issue_list.go:248-252 复核分支）。
	batchCtx3, cancel3 := context.WithCancel(context.Background())
	guardReturned := make(chan struct{})
	var trip3 atomic.Int32
	results = make(chan githubCloseNetResult, 1)
	go func() {
		closeOneGitHubIssue(batchCtx3, fake, issue, ghclient.UpdateIssueRequest{}, results, nil, &trip3,
			func(ctx context.Context, id string) (bool, error) {
				close(guardReturned)
				<-ctx.Done() // 等批次取消后才返回 true，让第二次 ctx 复核命中
				return true, nil
			}, nil)
	}()
	<-guardReturned
	cancel3()
	res = <-results
	if res.err == nil || !errors.Is(res.err, context.Canceled) {
		t.Fatalf("post-guard cancellation must attribute to cancellation, got %v", res.err)
	}
	if len(fake.recordedUpdateIssueCalls()) != 0 {
		t.Fatal("post-guard-cancelled item must never PATCH")
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
		// INT-185（复审 R13）：裸 429（空 body/无文案、网关自定义文案）也是权威
		// 限流信号，无需 body 含 rate limit 字样，否则不熔断会继续派发 PATCH。
		{"ErrorResponse429EmptyBody", &gh.ErrorResponse{
			Message:  "",
			Response: resp(http.StatusTooManyRequests),
		}, true},
		{"ErrorResponse429CustomGatewayBody", &gh.ErrorResponse{
			Message:  "request rejected by edge",
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

// INT-191：整批性失败类别判定——限流（含限流文案 403、裸 429）归
// abortRateLimit，401 归 abortAuth，其余 403 归 abortForbidden；404、5xx、
// 传输错误是单条或结果未知，不熔断。
func TestGitHubBatchCloseAbort(t *testing.T) {
	resp := func(status int) *http.Response { return &http.Response{StatusCode: status} }
	cases := []struct {
		name       string
		err        error
		wantReason batchCloseGitHubAbortReason
		wantFatal  bool
	}{
		{"RateLimitError", &gh.RateLimitError{Message: "API rate limit exceeded for 1.2.3.4."}, abortRateLimit, true},
		{"AbuseRateLimitError", &gh.AbuseRateLimitError{Message: "You have exceeded a secondary rate limit", Response: resp(http.StatusForbidden)}, abortRateLimit, true},
		{"ErrorResponse403RateLimitBody", &gh.ErrorResponse{Message: "You have exceeded a secondary rate limit", Response: resp(http.StatusForbidden)}, abortRateLimit, true},
		{"ErrorResponse429CustomGatewayBody", &gh.ErrorResponse{Message: "request rejected by edge", Response: resp(http.StatusTooManyRequests)}, abortRateLimit, true},
		{"ErrorResponse401", &gh.ErrorResponse{Message: "Bad credentials", Response: resp(http.StatusUnauthorized)}, abortAuth, true},
		{"ErrorResponse403OtherBody", &gh.ErrorResponse{Message: "Repository request blocked", Response: resp(http.StatusForbidden)}, abortForbidden, true},
		{"ErrorResponse404", &gh.ErrorResponse{Message: "Not Found", Response: resp(http.StatusNotFound)}, abortNone, false},
		{"ErrorResponse410Gone", &gh.ErrorResponse{Message: "Gone", Response: resp(http.StatusGone)}, abortNone, false},
		{"ErrorResponse500", &gh.ErrorResponse{Message: "Internal Server Error", Response: resp(http.StatusInternalServerError)}, abortNone, false},
		{"ErrorResponseNilHTTPResponse", &gh.ErrorResponse{Message: "Bad credentials"}, abortNone, false},
		{"PlainError", errors.New("connection reset by peer"), abortNone, false},
	}
	for _, tc := range cases {
		reason, fatal := githubBatchCloseAbort(tc.err)
		if fatal != tc.wantFatal || reason != tc.wantReason {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, reason, fatal, tc.wantReason, tc.wantFatal)
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

// INT-185（复审 R11 Note）：PATCH 成功但本地落地失败（upsertGitHubIssue/
// markCollectionsStaleAfterPush 返回 error）必须归「结果未确认」——GitHub 已关、
// 本地可能仍 open，按普通内部错误会让用户以为没关成功而重复操作。注入一个
// 返回错误的窄 storer，断言计入 failed 且文案为刷新确认类。
func TestIssueServiceBatchCloseDoneIssues_GitHubLocalPersistFailureIsUnconfirmed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	fake := &fakeIssueGitHubClient{}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}
	metas := seedGitHubDoneIssuesForBatchClose(t, svc, project.ID, 2, fake)

	// 注入落地失败：upsert 返回 error。
	failing := &failingBatchCloseStorer{upsertErr: errors.New("disk full")}
	svc.issueService.batchCloseStorer = failing
	defer func() { svc.issueService.batchCloseStorer = nil }()

	resp, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Failed != 2 || resp.Succeeded != 0 {
		t.Fatalf("all github rows must fail on upsert, got %+v", resp)
	}
	for i, f := range resp.Failures {
		if f.Error != "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" {
			t.Fatalf("failure %d must be the refresh-confirm copy, got %q", i, f.Error)
		}
	}
	// PATCH 确实发出过（远端已关，正是要提示刷新的原因）。
	if got := len(fake.recordedUpdateIssueCalls()); got != 2 {
		t.Fatalf("expected 2 PATCH calls, got %d", got)
	}
	_ = metas

	// markStale 失败同样归未确认。
	failing2 := &failingBatchCloseStorer{staleErr: errors.New("stale flag failed")}
	svc.issueService.batchCloseStorer = failing2
	resp2, err := svc.issueService.BatchCloseDoneIssues(context.Background(), project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close 2: %v", err)
	}
	for i, f := range resp2.Failures {
		if f.Error != "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" {
			t.Fatalf("stale failure %d must be the refresh-confirm copy, got %q", i, f.Error)
		}
	}
}

// failingBatchCloseStorer 测试注入：PATCH 成功后本地落地的两个调用点按字段
// 返回错误（INT-185 R11）。upsertErr 命中时先返回 upsert 错误。
type failingBatchCloseStorer struct {
	upsertErr error
	staleErr  error
}

func (f *failingBatchCloseStorer) upsertGitHubIssue(projectID string, item *ghclient.Issue) (*model.Issue, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	return &model.Issue{ID: "stored"}, nil
}

func (f *failingBatchCloseStorer) markCollectionsStaleAfterPush(issueID string) error {
	return f.staleErr
}

// INT-185（复审 R12/R13）：事务失败归因——批次 ctx 已死（取消/超时）时逐条
// 归「请求已取消或超时」，即使驱动/GORM 把事务错误包装成非 ctx 类型；批次
// 仍存活才报内部错误。用 classifyInternalCloseErr 直接断言三条边界，避免
// 依赖共享内存库上脆弱的持锁阻塞（R13 Note：原先用例在调用前就 cancel，
// 顶部早退、从未进事务失败分支）。
func TestClassifyInternalCloseErr(t *testing.T) {
	dbErr := errors.New("SQLITE_BUSY: database is locked")

	// 批次 ctx 已死 + 非 ctx 包装错误：回读 ctx.Err() 归取消/超时。
	done, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if got := classifyInternalCloseErr(done, dbErr, nil); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("dead ctx must attribute to its own err, got %v", got)
	}
	dead, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if got := classifyInternalCloseErr(dead, dbErr, nil); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancelled ctx must attribute to its own err, got %v", got)
	}

	// 批次存活 + ctx err：透传原 err（仍是取消类文案）。
	live := context.Background()
	if got := classifyInternalCloseErr(live, context.Canceled, nil); !errors.Is(got, context.Canceled) {
		t.Fatalf("live ctx with ctx err must passthrough, got %v", got)
	}

	// 批次存活 + 非 ctx 错误：归 ErrInternal 并记 warn（用 logger 验证告警）。
	var logged bool
	warn := func(msg string, fields ...zap.Field) { logged = true }
	if got := classifyInternalCloseErr(live, dbErr, warn); got != errs.ErrInternal {
		t.Fatalf("live ctx with wrapped err must be ErrInternal, got %v", got)
	}
	if !logged {
		t.Fatal("internal failure must be logged")
	}
}

// INT-185（复审 R14 Note）：closeInternalIssues 调用点回归保护——注入返回
// 驱动包装错误（非 ctx 类型）的事务缝，批次 ctx 已死时按批次归因取消/超时
// 而非「服务器内部错误」。验证 issue_list.go 对 classifyInternalCloseErr 的
// 调用点没被回退。
func TestIssueServiceBatchCloseDoneIssues_InternalTxInjectedErrorAttribution(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	now := time.Now().UTC()
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done", author: "alice", updated: now, created: now,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
	})
	var issues []model.Issue
	if err := svc.db.Where("project_id = ?", project.ID).Find(&issues).Error; err != nil {
		t.Fatalf("load issues: %v", err)
	}

	// 事务返回驱动包装错误（非 ctx 类型，模拟 SQLITE_INTERRUPT/SQLITE_BUSY）。
	svc.issueService.runCloseInternalTx = func(ids []string, now time.Time) ([]string, error) {
		return nil, errors.New("SQLITE_INTERRUPT: interrupted")
	}
	defer func() { svc.issueService.runCloseInternalTx = nil }()

	// 批次 ctx 已死（取消）：回读 ctx.Err() 归「请求已取消或超时」。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := &BatchCloseDoneIssuesResponse{Total: 1, Failures: make([]BatchCloseDoneIssueFailure, 0)}
	svc.issueService.closeInternalIssues(ctx, issues, resp)
	if resp.Failed != 1 || len(resp.Failures) != 1 {
		t.Fatalf("expected 1 failed, got %+v", resp)
	}
	if resp.Failures[0].Error != "请求已取消或超时，未继续关闭" {
		t.Fatalf("dead ctx + wrapped err must attribute to cancellation, got %q", resp.Failures[0].Error)
	}

	// 批次 ctx 存活：同一包装错误应归「服务器内部错误」（ErrInternal）。
	resp2 := &BatchCloseDoneIssuesResponse{Total: 1, Failures: make([]BatchCloseDoneIssueFailure, 0)}
	svc.issueService.closeInternalIssues(context.Background(), issues, resp2)
	if resp2.Failed != 1 || len(resp2.Failures) != 1 {
		t.Fatalf("expected 1 failed, got %+v", resp2)
	}
	if resp2.Failures[0].Error != errs.ErrInternal.Message {
		t.Fatalf("live ctx + wrapped err must be ErrInternal, got %q", resp2.Failures[0].Error)
	}
}
