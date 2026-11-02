package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
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

func TestIssueServiceBatchCloseDoneIssues_GitHubClosesConcurrentlyFasterThanSerial(t *testing.T) {
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
	maxConcurrent := atomic.LoadInt32(&fake.updateIssueMax)
	if maxConcurrent < 3 {
		t.Fatalf("expected concurrent UpdateIssue, max=%d", maxConcurrent)
	}
	// serialMs 只计 GitHub 等待；本地 upsert 串行，墙钟会更高，但仍应低于
	// 200 次串行往返。-race 下 upsert 更慢，所以延迟取 20ms 让网络等待占主导。
	serialMs := n * delay.Milliseconds()
	if resp.ElapsedMs >= serialMs {
		t.Fatalf("elapsed %dms is not faster than serial %dms (maxConcurrent=%d)", resp.ElapsedMs, serialMs, maxConcurrent)
	}
}

func TestIssueServiceBatchCloseDoneIssues_CancelStopsLaterGitHubUpdates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 20
	const delay = 40 * time.Millisecond
	fake := &fakeIssueGitHubClient{updateIssueDelay: delay}
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

	deadline := time.Now().Add(2 * time.Second)
	for len(fake.recordedUpdateIssueCalls()) < batchCloseGitHubConcurrency {
		if time.Now().After(deadline) {
			t.Fatal("UpdateIssue never reached concurrency")
		}
		time.Sleep(time.Millisecond)
	}
	// 等第一波 delay 结束并完成 upsert，再取消，避免「一个都没写下就取消」的假绿。
	time.Sleep(delay + 20*time.Millisecond)
	cancel()

	got := <-done
	if got.err != nil {
		t.Fatalf("batch close: %v", got.err)
	}
	if got.resp.Succeeded < 1 {
		t.Fatalf("expected at least one persisted close before cancel, got %+v", got.resp)
	}
	if got.resp.Failed == 0 {
		t.Fatalf("expected cancelled items in failures, got %+v", got.resp)
	}
	calls := len(fake.recordedUpdateIssueCalls())
	if calls >= n {
		t.Fatalf("expected later github updates to be skipped, got %d calls", calls)
	}
	if calls > 2*batchCloseGitHubConcurrency {
		t.Fatalf("expected calls bounded by a couple of worker waves, got %d", calls)
	}

	var stillOpen int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateOpen).Count(&stillOpen).Error; err != nil {
		t.Fatalf("count open: %v", err)
	}
	if stillOpen == 0 {
		t.Fatal("expected remaining github issues to stay open after cancel")
	}
	var closed int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateClosed).Count(&closed).Error; err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if closed < 1 {
		t.Fatal("expected a github issue closed locally before cancel")
	}
	if got.resp.Succeeded+got.resp.Failed != n {
		t.Fatalf("succeeded+failed should cover total, got %+v", got.resp)
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
