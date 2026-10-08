package service

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
)

var testPNGBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
}

func TestIssueServiceSyncProjectIssues_ImportsIssuesCommentsAndTimeline(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{
			{
				Issue: gh.Issue{
					ID:                int64Ptr(101),
					NodeID:            stringPtr("I_kw123"),
					Number:            intPtr(42),
					State:             stringPtr("open"),
					Title:             stringPtr("Crash on launch"),
					Body:              stringPtr("App crashes on startup"),
					HTMLURL:           stringPtr("https://github.com/owner/repo/issues/42"),
					User:              &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
					AuthorAssociation: stringPtr("MEMBER"),
					Labels:            []*gh.Label{{Name: stringPtr("bug"), Color: stringPtr("d73a4a")}},
					Comments:          intPtr(1),
					CreatedAt:         &gh.Timestamp{Time: now.Add(-2 * time.Hour)},
					UpdatedAt:         &gh.Timestamp{Time: now.Add(-1 * time.Hour)},
					Reactions:         &gh.Reactions{TotalCount: intPtr(1), PlusOne: intPtr(1)},
				},
				BodyHTML: stringPtr("<p>App crashes on <strong>startup</strong></p>"),
			},
		},
		comments: map[int][]*ghclient.IssueComment{
			42: {
				{
					IssueComment: gh.IssueComment{
						ID:                int64Ptr(501),
						NodeID:            stringPtr("IC_kw123"),
						Body:              stringPtr("I can reproduce this"),
						HTMLURL:           stringPtr("https://github.com/owner/repo/issues/42#issuecomment-501"),
						User:              &gh.User{Login: stringPtr("bob"), AvatarURL: stringPtr("https://example.com/bob.png")},
						AuthorAssociation: stringPtr("CONTRIBUTOR"),
						CreatedAt:         &gh.Timestamp{Time: now.Add(-30 * time.Minute)},
						UpdatedAt:         &gh.Timestamp{Time: now.Add(-30 * time.Minute)},
					},
					BodyHTML: stringPtr("<p>I can reproduce this</p><p><img src=\"https://example.com/repro.png\" alt=\"repro\"></p>"),
				},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {
				{
					Timeline: gh.Timeline{
						ID:        int64Ptr(701),
						Event:     stringPtr("labeled"),
						Actor:     &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
						Label:     &gh.Label{Name: stringPtr("bug")},
						CreatedAt: &gh.Timestamp{Time: now.Add(-20 * time.Minute)},
					},
				},
			},
		},
	}

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err != nil {
		t.Fatalf("sync project issues: %v", err)
	}

	if result.SyncedIssueCount != 1 || result.SyncedCommentCount != 1 || result.SyncedTimelineCount != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	issues, total, err := svc.issueService.List(project.ID, project.UserID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if total != 1 || len(issues) != 1 {
		t.Fatalf("expected 1 synced issue, got total=%d len=%d", total, len(issues))
	}
	if issues[0].Title != "Crash on launch" || issues[0].Github == nil || issues[0].Github.Labels[0].Name != "bug" {
		t.Fatalf("unexpected issue payload: %+v", issues[0])
	}
	if issues[0].BodyHtml != "<p>App crashes on <strong>startup</strong></p>" {
		t.Fatalf("expected issue html body to be persisted, got %+v", issues[0])
	}

	comments, total, err := svc.issueService.ListComments(issues[0].Id, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 || comments[0].Author.Login != "bob" {
		t.Fatalf("unexpected comments payload: %+v", comments)
	}
	if comments[0].BodyHtml == "" {
		t.Fatalf("expected comment html body to be persisted, got %+v", comments[0])
	}

	events, total, err := svc.issueService.ListTimeline(issues[0].Id, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list timeline: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("unexpected events payload: %+v", events)
	}
	if events[0].Summary != "添加了标签 bug" {
		t.Fatalf("unexpected summary: %+v", events[0])
	}
}

func TestIssueServiceGetRepositoryLabels_ReturnsSortedLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("Something isn't working")},
			{Name: stringPtr("IOS"), Color: stringPtr("0969da"), Description: stringPtr("iOS platform")},
			{Name: stringPtr("Bug"), Color: stringPtr("f00"), Description: stringPtr("duplicate should be ignored")},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	labels, err := svc.issueService.GetRepositoryLabels(project.ID, user.ID)
	if err != nil {
		t.Fatalf("get repository labels: %v", err)
	}

	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	if labels[0].Name != "bug" || labels[1].Name != "IOS" {
		t.Fatalf("unexpected sorted labels: %+v", labels)
	}
	if labels[0].Color != "d73a4a" || labels[1].Color != "0969da" {
		t.Fatalf("unexpected label colors: %+v", labels)
	}
}

func TestIssueServiceGetRepositoryLabels_ReturnsGitHubError(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	fake := &fakeIssueGitHubClient{
		repoLabelsErr: fmt.Errorf("github unavailable"),
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	if _, err := svc.issueService.GetRepositoryLabels(project.ID, user.ID); err == nil {
		t.Fatalf("expected github api error, got nil")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}
}

func TestIssueServiceGetRepositoryLabels_RejectsNotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-nogit")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = nil
	})

	_, err := svc.issueService.GetRepositoryLabels(project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok {
		t.Fatalf("expected *errs.AppError, got %T", err)
	}
	if appErr.Code != errs.ErrProjectGitHubNotConfigured.Code {
		t.Errorf("expected code %d, got %d", errs.ErrProjectGitHubNotConfigured.Code, appErr.Code)
	}
}

type fakeIssueGitHubClient struct {
	issues        []*ghclient.Issue
	repoLabels    []*gh.Label
	repoLabelsErr error
	comments      map[int][]*ghclient.IssueComment
	timeline      map[int][]*ghclient.TimelineEvent
	// 分页模式：commentsPages/timelinePages 按页返回并推导 NextPage；
	// commentErrs/timelineErrs 按 (issueNumber, page) 注入拉取失败。
	commentsPages      map[int][][]*ghclient.IssueComment
	commentErrs        map[int]map[int]error
	timelinePages      map[int][][]*ghclient.TimelineEvent
	timelineErrs       map[int]map[int]error
	createdComment     *ghclient.IssueComment
	createCommentErr   error
	updatedIssue       *ghclient.Issue
	createdIssue       *ghclient.Issue
	createIssueErr     error
	createIssueCalls   []fakeCreateIssueCall
	createCommentCalls []fakeCreateCommentCall
	updateIssueCalls   []fakeUpdateIssueCall
	pullRequests       map[int]*gh.PullRequest
	pullRequestErr     error
	onGetPullRequest   func(number int)
}

type fakeCreateIssueCall struct {
	Title string
	Body  string
}

type fakeCreateCommentCall struct {
	IssueNumber int
	Body        string
}

type fakeUpdateIssueCall struct {
	IssueNumber int
	Title       string
	Body        string
	State       string
	StateReason string
	Labels      []string
}

func (f *fakeIssueGitHubClient) ValidateRepository(context.Context) error {
	return nil
}

func (f *fakeIssueGitHubClient) ListIssues(context.Context, string, *time.Time, int, int) ([]*ghclient.Issue, *gh.Response, error) {
	return f.issues, &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListRepositoryLabels(context.Context, int, int) ([]*gh.Label, *gh.Response, error) {
	return f.repoLabels, &gh.Response{NextPage: 0}, f.repoLabelsErr
}

func (f *fakeIssueGitHubClient) ListIssueComments(_ context.Context, issueNumber, page, _ int) ([]*ghclient.IssueComment, *gh.Response, error) {
	if err := f.commentErrs[issueNumber][page]; err != nil {
		return nil, nil, err
	}
	if pages, ok := f.commentsPages[issueNumber]; ok {
		var items []*ghclient.IssueComment
		if page >= 1 && page <= len(pages) {
			items = pages[page-1]
		}
		next := 0
		if page < len(pages) {
			next = page + 1
		}
		return items, &gh.Response{NextPage: next}, nil
	}
	return f.comments[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListIssueTimeline(_ context.Context, issueNumber, page, _ int) ([]*ghclient.TimelineEvent, *gh.Response, error) {
	if err := f.timelineErrs[issueNumber][page]; err != nil {
		return nil, nil, err
	}
	if pages, ok := f.timelinePages[issueNumber]; ok {
		var items []*ghclient.TimelineEvent
		if page >= 1 && page <= len(pages) {
			items = pages[page-1]
		}
		next := 0
		if page < len(pages) {
			next = page + 1
		}
		return items, &gh.Response{NextPage: next}, nil
	}
	return f.timeline[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) CreateIssueComment(_ context.Context, issueNumber int, body string) (*ghclient.IssueComment, error) {
	f.createCommentCalls = append(f.createCommentCalls, fakeCreateCommentCall{
		IssueNumber: issueNumber,
		Body:        body,
	})
	if f.createCommentErr != nil {
		return nil, f.createCommentErr
	}
	return f.createdComment, nil
}

func (f *fakeIssueGitHubClient) UpdateIssue(_ context.Context, issueNumber int, req ghclient.UpdateIssueRequest) (*ghclient.Issue, error) {
	call := fakeUpdateIssueCall{IssueNumber: issueNumber}
	if req.Title != nil {
		call.Title = *req.Title
	}
	if req.Body != nil {
		call.Body = *req.Body
	}
	if req.State != nil {
		call.State = *req.State
	}
	if req.StateReason != nil {
		call.StateReason = *req.StateReason
	}
	if req.Labels != nil {
		call.Labels = append([]string(nil), (*req.Labels)...)
	}
	f.updateIssueCalls = append(f.updateIssueCalls, call)
	return f.updatedIssue, nil
}

func (f *fakeIssueGitHubClient) CreateIssue(_ context.Context, title, body string) (*ghclient.Issue, error) {
	f.createIssueCalls = append(f.createIssueCalls, fakeCreateIssueCall{
		Title: title,
		Body:  body,
	})
	return f.createdIssue, f.createIssueErr
}

func (f *fakeIssueGitHubClient) GetPullRequest(_ context.Context, number int) (*gh.PullRequest, error) {
	if f.onGetPullRequest != nil {
		f.onGetPullRequest(number)
	}
	if f.pullRequestErr != nil {
		return nil, f.pullRequestErr
	}
	pr, ok := f.pullRequests[number]
	if !ok {
		return nil, fmt.Errorf("pull request %d not found", number)
	}
	return pr, nil
}

func intPtr(v int) *int {
	return &v
}

func testGitHubSyncIssue(id int64, number int, at time.Time) *ghclient.Issue {
	return &ghclient.Issue{
		Issue: gh.Issue{
			ID:        int64Ptr(id),
			NodeID:    stringPtr(fmt.Sprintf("I_kw%d", id)),
			Number:    intPtr(number),
			State:     stringPtr("open"),
			Title:     stringPtr(fmt.Sprintf("Issue %d", number)),
			Body:      stringPtr("body"),
			HTMLURL:   stringPtr(fmt.Sprintf("https://github.com/owner/repo/issues/%d", number)),
			User:      &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
			CreatedAt: &gh.Timestamp{Time: at},
			UpdatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func testGitHubSyncComment(id int64, body string, at time.Time) *ghclient.IssueComment {
	return &ghclient.IssueComment{
		IssueComment: gh.IssueComment{
			ID:        int64Ptr(id),
			NodeID:    stringPtr(fmt.Sprintf("IC_kw%d", id)),
			Body:      stringPtr(body),
			HTMLURL:   stringPtr(fmt.Sprintf("https://github.com/owner/repo/issues/42#issuecomment-%d", id)),
			User:      &gh.User{Login: stringPtr("bob"), AvatarURL: stringPtr("https://example.com/bob.png")},
			CreatedAt: &gh.Timestamp{Time: at},
			UpdatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func testGitHubSyncTimelineEvent(id int64, event string, at time.Time) *ghclient.TimelineEvent {
	return &ghclient.TimelineEvent{
		Timeline: gh.Timeline{
			ID:        int64Ptr(id),
			Event:     stringPtr(event),
			Actor:     &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
			CreatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func syncedGitHubIssueID(t *testing.T, svc *testServices, projectID string) string {
	t.Helper()
	var issue model.Issue
	if err := svc.db.First(&issue, "project_id = ?", projectID).Error; err != nil {
		t.Fatalf("load synced issue: %v", err)
	}
	return issue.ID
}

func storedCommentsByGitHubID(t *testing.T, svc *testServices, issueID string) map[int64]model.IssueComment {
	t.Helper()
	comments, err := svc.commentRepo.ListAllByIssueID(issueID)
	if err != nil {
		t.Fatalf("list stored comments: %v", err)
	}
	result := make(map[int64]model.IssueComment, len(comments))
	for _, c := range comments {
		result[c.GitHubCommentID] = c
	}
	return result
}

func storedTimelineByKey(t *testing.T, svc *testServices, issueID string) map[string]model.IssueTimelineEvent {
	t.Helper()
	var events []model.IssueTimelineEvent
	if err := svc.db.Where("issue_id = ?", issueID).Find(&events).Error; err != nil {
		t.Fatalf("list stored timeline events: %v", err)
	}
	result := make(map[string]model.IssueTimelineEvent, len(events))
	for _, e := range events {
		result[e.EventKey] = e
	}
	return result
}

func TestIssueSyncStateRepositoryGetOrCreate_IsAtomic(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")

	const workers = 12
	start := make(chan struct{})
	errCh := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.syncStateRepo.GetOrCreate(project.ID)
			errCh <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("get or create sync state: %v", err)
		}
	}

	var count int64
	if err := svc.db.Model(&model.IssueSyncState{}).Where("project_id = ?", project.ID).Count(&count).Error; err != nil {
		t.Fatalf("count sync state rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one sync state row, got %d", count)
	}
}

func TestIssueServiceUpdateInternalMeta_ReflectsInGetAndList(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	issue := createTestIssue(t, svc.db, project.ID)

	meta, err := svc.issueService.UpdateInternalMeta(issue.ID, project.UserID, model.IssueWorkflowStatusInProgress, "test")
	if err != nil {
		t.Fatalf("update internal meta: %v", err)
	}
	if meta == nil || meta.WorkflowStatus != model.IssueWorkflowStatusInProgress {
		t.Fatalf("unexpected internal meta response: %+v", meta)
	}
	if meta.StartedAt == nil {
		t.Fatalf("expected started_at to be set")
	}

	got, err := svc.issueService.Get(issue.ID, project.UserID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.InternalMeta == nil || got.InternalMeta.WorkflowStatus != model.IssueWorkflowStatusInProgress {
		t.Fatalf("expected internal meta on get, got %+v", got.InternalMeta)
	}

	items, total, err := svc.issueService.List(project.ID, project.UserID, IssueListFilters{Workflow: "in_progress"}, 1, 20)
	if err != nil {
		t.Fatalf("list issues by workflow: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one filtered issue, got total=%d len=%d", total, len(items))
	}

	items, total, err = svc.issueService.List(project.ID, project.UserID, IssueListFilters{Workflow: "unset"}, 1, 20)
	if err != nil {
		t.Fatalf("list issues by unset workflow: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected no unset issues, got total=%d len=%d", total, len(items))
	}
}

func TestIssueServiceUpdateInternalMeta_UpdatesIssueTimestamp(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	issue := createTestIssue(t, svc.db, project.ID)

	resp, err := svc.issueService.UpdateInternalMeta(issue.ID, project.UserID, model.IssueWorkflowStatusInProgress, "test")
	if err != nil {
		t.Fatalf("update internal meta: %v", err)
	}

	updated, err := svc.issueRepo.FindByID(issue.ID)
	if err != nil {
		t.Fatalf("reload issue: %v", err)
	}

	// (a) 主表 updated_at 推进。
	if !updated.UpdatedAt.After(issue.UpdatedAt) {
		t.Fatalf("expected issue updated_at to advance past %v, got %v", issue.UpdatedAt, updated.UpdatedAt)
	}

	// (b) 主表业务字段未被定向 UPDATE 改动。
	if updated.Title != issue.Title {
		t.Fatalf("expected title unchanged %q, got %q", issue.Title, updated.Title)
	}
	if updated.Body != issue.Body {
		t.Fatalf("expected body unchanged %q, got %q", issue.Body, updated.Body)
	}
	if updated.State != issue.State {
		t.Fatalf("expected state unchanged %q, got %q", issue.State, updated.State)
	}
	if updated.Source != issue.Source {
		t.Fatalf("expected source unchanged %q, got %q", issue.Source, updated.Source)
	}
	if updated.AuthorLogin != issue.AuthorLogin {
		t.Fatalf("expected author_login unchanged %q, got %q", issue.AuthorLogin, updated.AuthorLogin)
	}
	if updated.SequenceNumber != issue.SequenceNumber {
		t.Fatalf("expected sequence_number unchanged %d, got %d", issue.SequenceNumber, updated.SequenceNumber)
	}

	// (c) 主表 updated_at 与响应中的 meta updated_at 一致（均来自同一次 now）。
	if resp.UpdatedAt == nil {
		t.Fatalf("expected response updated_at to be set")
	}
	respUpdatedAt, err := time.Parse(time.RFC3339, *resp.UpdatedAt)
	if err != nil {
		t.Fatalf("parse response updated_at %q: %v", *resp.UpdatedAt, err)
	}
	if !respUpdatedAt.Equal(updated.UpdatedAt.Truncate(time.Second)) {
		t.Fatalf("expected response updated_at %v to equal issue updated_at %v", respUpdatedAt, updated.UpdatedAt)
	}
}

func TestIssueServiceUpdateInternalMeta_KeepsFirstCompletedAtAcrossReopen(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	firstDone, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusDone, "test")
	if err != nil {
		t.Fatalf("mark issue done the first time: %v", err)
	}
	if firstDone == nil || firstDone.CompletedAt == nil {
		t.Fatalf("expected first completion timestamp, got %+v", firstDone)
	}
	firstCompletedAt := *firstDone.CompletedAt

	reopened, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusInProgress, "test")
	if err != nil {
		t.Fatalf("reopen issue after done: %v", err)
	}
	if reopened == nil || reopened.CompletedAt == nil {
		t.Fatalf("expected reopening to preserve first completion timestamp, got %+v", reopened)
	}
	if got := *reopened.CompletedAt; got != firstCompletedAt {
		t.Fatalf("expected reopening to preserve first completion timestamp %q, got %q", firstCompletedAt, got)
	}

	time.Sleep(50 * time.Millisecond)

	doneAgain, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusDone, "test")
	if err != nil {
		t.Fatalf("mark issue done the second time: %v", err)
	}
	if doneAgain == nil || doneAgain.CompletedAt == nil {
		t.Fatalf("expected second done to keep completion timestamp, got %+v", doneAgain)
	}
	if got := *doneAgain.CompletedAt; got != firstCompletedAt {
		t.Fatalf("expected second done to keep original completion timestamp %q, got %q", firstCompletedAt, got)
	}

	var stored model.IssueInternalMeta
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored internal meta: %v", err)
	}
	if stored.CompletedAt == nil {
		t.Fatalf("expected stored completion timestamp to remain set")
	}
	if got := stored.CompletedAt.UTC().Format(time.RFC3339); got != firstCompletedAt {
		t.Fatalf("expected stored completion timestamp %q, got %q", firstCompletedAt, got)
	}
}

func TestIssueServiceReplaceChecklist_UpdatesProgressSnapshot(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	meta, err := svc.issueService.ReplaceChecklist(issue.ID, user.ID, ReplaceIssueChecklistRequest{
		Items: []IssueChecklistItemInput{
			{Title: "定位问题", IsCompleted: api.Ptr(true)},
			{Title: "修复问题", IsCompleted: api.Ptr(false)},
		},
	}, "test")
	if err != nil {
		t.Fatalf("replace checklist: %v", err)
	}
	if meta == nil || meta.ProgressPercent == nil || *meta.ProgressPercent != 50 {
		t.Fatalf("expected progress 50, got %+v", meta)
	}
	if meta.ChecklistTotal != 2 || meta.ChecklistDone != 1 {
		t.Fatalf("unexpected checklist counters: %+v", meta)
	}
	if meta.WorkflowStatus != "" {
		t.Fatalf("expected empty workflow status (checklist should not auto-set it), got %+v", meta)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.InternalMeta == nil || got.InternalMeta.ProgressPercent == nil || *got.InternalMeta.ProgressPercent != 50 {
		t.Fatalf("expected stored progress on get, got %+v", got.InternalMeta)
	}
	if len(got.InternalMeta.Checklist) != 2 {
		t.Fatalf("expected checklist items on get, got %+v", got.InternalMeta)
	}
}

func TestIssueServiceCreateInternalIssue_CreatesLocalIssue(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title:          "补充发布检查",
		Body:           api.Ptr("## 检查项\n\n- 校验版本说明"),
		WorkflowStatus: api.Ptr(model.IssueWorkflowStatusTodo),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	if created.Source != model.IssueSourceInternal {
		t.Fatalf("expected internal source, got %+v", created)
	}
	if created.Reference != "INT-1" {
		t.Fatalf("expected INT-1 reference, got %+v", created)
	}
	if created.Github != nil {
		t.Fatalf("expected no github payload, got %+v", created.Github)
	}
	if created.InternalMeta == nil || created.InternalMeta.WorkflowStatus != model.IssueWorkflowStatusTodo {
		t.Fatalf("expected todo workflow meta, got %+v", created.InternalMeta)
	}

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one issue after create, got total=%d len=%d", total, len(items))
	}
	if items[0].Author.Login != user.Username {
		t.Fatalf("expected author to use username, got %+v", items[0].Author)
	}
}

func TestIssueServiceCreateInternalIssue_AttachesReferencedDraftAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	draftAsset, err := svc.issueService.UploadDraftInternalIssueAsset(
		project.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload draft issue asset: %v", err)
	}

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr(fmt.Sprintf("创建时直接引用图片\n\n%s", draftAsset.Markdown)),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 attached issue asset, got %d", len(assets))
	}
	if assets[0].ID != draftAsset.Id {
		t.Fatalf("expected draft asset %q to be attached, got %q", draftAsset.Id, assets[0].ID)
	}
	if assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("expected asset status attached, got %q", assets[0].Status)
	}

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.Id})
	if err != nil {
		t.Fatalf("list draft assets: %v", err)
	}
	if len(draftAssets) != 0 {
		t.Fatalf("expected referenced draft asset to be removed after attach, got %d", len(draftAssets))
	}

	reader, mimeType, fileSize, err := svc.issueService.GetIssueAssetContent(draftAsset.Id, user.ID)
	if err != nil {
		t.Fatalf("get issue asset content: %v", err)
	}
	defer reader.Close()
	if mimeType != "image/png" {
		t.Fatalf("expected image/png mime type, got %q", mimeType)
	}
	if fileSize <= 0 {
		t.Fatalf("expected persisted file size, got %d", fileSize)
	}
}

func TestIssueServiceCreateInternalIssue_RollsBackWhenAttachingDraftAssetsFails(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	draftAsset, err := svc.issueService.UploadDraftInternalIssueAsset(
		project.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload draft issue asset: %v", err)
	}

	existingIssue := createTestIssue(t, svc.db, project.ID, func(issue *model.Issue) {
		issue.Source = model.IssueSourceInternal
		issue.SequenceNumber = 1
		issue.AuthorUserID = user.ID
		issue.AuthorLogin = user.Username
	})
	if err := svc.db.Create(&model.IssueAsset{
		ID:              draftAsset.Id,
		IssueID:         existingIssue.ID,
		FileName:        "existing.png",
		FilePath:        "existing/path.png",
		MimeType:        "image/png",
		FileSize:        int64(len(testPNGBytes)),
		Status:          model.IssueAssetStatusAttached,
		CreatedByUserID: user.ID,
		CreatedAt:       time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed conflicting issue asset: %v", err)
	}

	_, err = svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr(fmt.Sprintf("创建时直接引用图片\n\n%s", draftAsset.Markdown)),
	})
	if err != errs.ErrInternal {
		t.Fatalf("expected internal error, got %v", err)
	}

	var issueCount int64
	if err := svc.db.Model(&model.Issue{}).
		Where("project_id = ? AND title = ?", project.ID, "补充发布检查").
		Count(&issueCount).Error; err != nil {
		t.Fatalf("count rolled back issues: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("expected create to roll back inserted issue, got %d rows", issueCount)
	}

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.Id})
	if err != nil {
		t.Fatalf("list draft assets: %v", err)
	}
	if len(draftAssets) != 1 {
		t.Fatalf("expected draft asset to remain after rollback, got %d", len(draftAssets))
	}
}

func TestIssueServiceCreateInternalIssue_RejectsMissingReferencedDraftAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	_, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("引用了不存在的图片 ![missing](/api/issues/assets/11111111-1111-1111-1111-111111111111/content)"),
	})
	if err != errs.ErrInvalidParams {
		t.Fatalf("expected invalid params, got %v", err)
	}

	var issueCount int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ?", project.ID).Count(&issueCount).Error; err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("expected no issues to be created, got %d", issueCount)
	}
}

func TestIssueServiceUpdateInternalIssue_UpdatesBodyAndState(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("old body"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	title := "更新后的标题"
	body := "new body"
	state := model.IssueStateClosed
	updated, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Title: &title,
		Body:  &body,
		State: &state,
	})
	if err != nil {
		t.Fatalf("update internal issue: %v", err)
	}

	if updated.Title != title || updated.Body != body || updated.State != model.IssueStateClosed {
		t.Fatalf("unexpected updated issue: %+v", updated)
	}
	if updated.ClosedAt == nil {
		t.Fatalf("expected closed_at to be set")
	}
}

func TestIssueServiceUpdateInternalIssue_RemovesDetachedAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("初始内容"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.Id,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
		"test",
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	bodyWithAsset := fmt.Sprintf("保留正文\n\n%s", asset.Markdown)
	if _, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithAsset,
	}); err != nil {
		t.Fatalf("attach asset markdown: %v", err)
	}

	newBody := "不再引用图片"
	if _, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Body: &newBody,
	}); err != nil {
		t.Fatalf("remove asset markdown: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("expected issue assets to be deleted, got %d", len(assets))
	}

	exists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.Id, asset.Id))
	if err != nil {
		t.Fatalf("stat uploaded issue asset: %v", err)
	}
	if exists {
		t.Fatalf("expected uploaded issue asset file to be deleted")
	}
}

func TestIssueServiceUpdateInternalIssue_AttachesReferencedPendingAsset(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("初始内容"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.Id,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
		"test",
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	bodyWithAsset := fmt.Sprintf("保留正文\n\n%s", asset.Markdown)
	if _, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithAsset,
	}); err != nil {
		t.Fatalf("attach asset markdown: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 issue asset, got %d", len(assets))
	}
	if assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("expected asset status attached, got %q", assets[0].Status)
	}
}

func TestIssueServiceUpdateInternalIssue_RemovesUnreferencedPendingAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("初始内容"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.Id,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
		"test",
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	body := "保留正文但不再引用图片"
	if _, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Body: &body,
	}); err != nil {
		t.Fatalf("update internal issue: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("expected pending issue asset to be deleted, got %d", len(assets))
	}

	exists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.Id, asset.Id))
	if err != nil {
		t.Fatalf("stat uploaded issue asset: %v", err)
	}
	if exists {
		t.Fatalf("expected pending issue asset file to be deleted")
	}
}

func TestIssueServiceCleanupExpiredPendingIssueAssets_RemovesOnlyExpiredPending(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("初始内容"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	expiredPending, err := svc.issueService.UploadInternalIssueAsset(
		created.Id,
		user.ID,
		"expired.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
		"test",
	)
	if err != nil {
		t.Fatalf("upload expired pending asset: %v", err)
	}
	attached, err := svc.issueService.UploadInternalIssueAsset(
		created.Id,
		user.ID,
		"keep.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
		"test",
	)
	if err != nil {
		t.Fatalf("upload attached asset: %v", err)
	}
	if err := svc.db.Model(&model.IssueAsset{}).
		Where("id = ?", attached.Id).
		Update("status", model.IssueAssetStatusAttached).
		Error; err != nil {
		t.Fatalf("mark asset attached: %v", err)
	}

	expiredAt := time.Now().UTC().Add(-issueAssetPendingTTL - time.Hour)
	if err := svc.db.Model(&model.IssueAsset{}).
		Where("id = ?", expiredPending.Id).
		Update("created_at", expiredAt).
		Error; err != nil {
		t.Fatalf("age pending asset: %v", err)
	}

	if err := svc.issueService.CleanupExpiredPendingIssueAssets(); err != nil {
		t.Fatalf("cleanup expired pending issue assets: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 remaining issue asset, got %d", len(assets))
	}
	if assets[0].ID != attached.Id {
		t.Fatalf("expected attached asset %q to remain, got %q", attached.Id, assets[0].ID)
	}
	if assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("expected remaining asset to stay attached, got %q", assets[0].Status)
	}

	expiredExists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.Id, expiredPending.Id))
	if err != nil {
		t.Fatalf("stat expired pending asset: %v", err)
	}
	if expiredExists {
		t.Fatalf("expected expired pending asset file to be deleted")
	}

	attachedExists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.Id, attached.Id))
	if err != nil {
		t.Fatalf("stat attached asset: %v", err)
	}
	if !attachedExists {
		t.Fatalf("expected attached asset file to remain")
	}
}

func TestIssueServiceCreateInternalComment_AddsCommentToInternalIssue(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "补充发布检查",
		Body:  api.Ptr("issue body"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	comment, err := svc.issueService.CreateInternalComment(created.Id, user.ID, CreateInternalIssueCommentRequest{
		Body: "第一条内部评论",
	}, "test")
	if err != nil {
		t.Fatalf("create internal comment: %v", err)
	}

	if comment.Source != model.IssueSourceInternal || comment.Author.Login != user.Username {
		t.Fatalf("unexpected created comment: %+v", comment)
	}

	comments, total, err := svc.issueService.ListComments(created.Id, user.ID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 {
		t.Fatalf("expected one internal comment, got total=%d len=%d", total, len(comments))
	}
	if comments[0].Body != "第一条内部评论" {
		t.Fatalf("unexpected internal comment payload: %+v", comments[0])
	}
}

func TestIssueServiceCreateInternalCommentIdempotent_DoesNotDuplicateOnRetry(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-idempotent")
	project := createTestProject(t, svc.db, user.ID)
	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{Title: "hook", Body: api.Ptr("body")})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	req := CreateInternalIssueCommentRequest{Body: "release comment"}
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, callErr := svc.issueService.CreateInternalCommentIdempotent(issue.Id, user.ID, req, "ship-hook", "ship-hook-comment:attempt-1")
			results <- callErr
		}()
	}
	for i := 0; i < 8; i++ {
		if callErr := <-results; callErr != nil {
			t.Fatalf("concurrent idempotent comment: %v", callErr)
		}
	}
	_, total, err := svc.issueService.ListComments(issue.Id, user.ID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected idempotent retry to keep one comment, got %d", total)
	}
}

func TestIssueCommentUpsertPersistsIdempotencyKeyForExistingGitHubComment(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-comment-upsert")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	now := time.Now().UTC()
	comment := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issue.ID,
		Source:          model.IssueSourceGitHub,
		GitHubCommentID: 42,
		Body:            "remote marker",
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}
	if err := svc.commentRepo.Upsert(comment); err != nil {
		t.Fatalf("seed github comment: %v", err)
	}
	comment.IdempotencyKey = "ship-hook-comment:attempt-1"
	if err := svc.commentRepo.Upsert(comment); err != nil {
		t.Fatalf("update github comment marker: %v", err)
	}
	stored, err := svc.commentRepo.FindByIdempotencyKey(issue.ID, comment.IdempotencyKey)
	if err != nil {
		t.Fatalf("find idempotency key: %v", err)
	}
	if stored.GitHubCommentID != comment.GitHubCommentID {
		t.Fatalf("unexpected stored comment: %+v", stored)
	}
}

func TestIssueServiceUpdateInternalIssue_WritesGitHubStateBack(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	closedAt := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	fake := &fakeIssueGitHubClient{
		updatedIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:          int64Ptr(1001),
				NodeID:      stringPtr("I_kw_test"),
				Number:      intPtr(42),
				State:       stringPtr("closed"),
				StateReason: stringPtr("completed"),
				Title:       stringPtr("Crash on launch"),
				Body:        stringPtr("App crashes"),
				HTMLURL:     stringPtr("https://github.com/owner/repo/issues/42"),
				User: &gh.User{
					Login:     stringPtr("alice"),
					AvatarURL: stringPtr("https://avatars.example/alice.png"),
				},
				CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt: &gh.Timestamp{Time: closedAt},
				ClosedAt:  &gh.Timestamp{Time: closedAt},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {
				{
					Timeline: gh.Timeline{
						ID:        int64Ptr(701),
						Event:     stringPtr("closed"),
						Actor:     &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://avatars.example/alice.png")},
						CreatedAt: &gh.Timestamp{Time: closedAt},
					},
				},
			},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	state := model.IssueStateClosed
	reason := api.UpdateIssueRequestStateReasonCompleted
	updated, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		State:       &state,
		StateReason: &reason,
	})
	if err != nil {
		t.Fatalf("update github issue: %v", err)
	}

	if len(fake.updateIssueCalls) != 1 {
		t.Fatalf("expected one github update call, got %d", len(fake.updateIssueCalls))
	}
	if fake.updateIssueCalls[0].IssueNumber != 42 || fake.updateIssueCalls[0].State != "closed" || fake.updateIssueCalls[0].StateReason != "completed" {
		t.Fatalf("unexpected update call: %+v", fake.updateIssueCalls[0])
	}
	if updated.State != model.IssueStateClosed || updated.StateReason != "completed" || updated.ClosedAt == nil {
		t.Fatalf("unexpected updated issue payload: %+v", updated)
	}

	events, total, err := svc.issueService.ListTimeline(issue.ID, user.ID, 1, 20)
	if err != nil {
		t.Fatalf("list timeline after github state update: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("expected one synced timeline event, got total=%d len=%d", total, len(events))
	}
	if events[0].EventType != "closed" || events[0].Summary != "关闭了问题" {
		t.Fatalf("unexpected timeline event payload: %+v", events[0])
	}
}

func TestIssueServiceUpdateInternalIssue_WritesGitHubTitleBack(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	updatedAt := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	fake := &fakeIssueGitHubClient{
		updatedIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:      int64Ptr(1001),
				NodeID:  stringPtr("I_kw_test"),
				Number:  intPtr(42),
				State:   stringPtr("open"),
				Title:   stringPtr("修复崩溃并补充测试"),
				Body:    stringPtr("App crashes"),
				HTMLURL: stringPtr("https://github.com/owner/repo/issues/42"),
				User: &gh.User{
					Login:     stringPtr("alice"),
					AvatarURL: stringPtr("https://avatars.example/alice.png"),
				},
				CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt: &gh.Timestamp{Time: updatedAt},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	title := "修复崩溃并补充测试"
	updated, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Title: &title,
	})
	if err != nil {
		t.Fatalf("update github issue title: %v", err)
	}

	if len(fake.updateIssueCalls) != 1 {
		t.Fatalf("expected one github update call, got %d", len(fake.updateIssueCalls))
	}
	call := fake.updateIssueCalls[0]
	if call.IssueNumber != 42 || call.Title != "修复崩溃并补充测试" {
		t.Fatalf("unexpected update call: %+v", call)
	}
	if updated.Title != "修复崩溃并补充测试" {
		t.Fatalf("unexpected updated issue title: %q", updated.Title)
	}
}

func TestIssueServiceUpdateInternalIssue_RejectsEmptyTitle(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	for _, title := range []string{"", "   "} {
		t.Run(fmt.Sprintf("title=%q", title), func(t *testing.T) {
			_, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
				Title: &title,
			})
			if err == nil {
				t.Fatal("expected error for empty title, got nil")
			}
			if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
				t.Fatalf("expected ErrInvalidParams, got %v", err)
			}
		})
	}
}

func TestIssueServiceUpdateInternalIssue_WritesGitHubBodyBack(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	updatedAt := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	fake := &fakeIssueGitHubClient{
		updatedIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:      int64Ptr(1001),
				NodeID:  stringPtr("I_kw_test"),
				Number:  intPtr(42),
				State:   stringPtr("open"),
				Title:   stringPtr("Test Issue"),
				Body:    stringPtr("new body content"),
				HTMLURL: stringPtr("https://github.com/owner/repo/issues/42"),
				User: &gh.User{
					Login:     stringPtr("alice"),
					AvatarURL: stringPtr("https://avatars.example/alice.png"),
				},
				CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt: &gh.Timestamp{Time: updatedAt},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	body := "new body content"
	updated, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Body: &body,
	})
	if err != nil {
		t.Fatalf("update github issue body: %v", err)
	}

	if len(fake.updateIssueCalls) != 1 {
		t.Fatalf("expected one github update call, got %d", len(fake.updateIssueCalls))
	}
	call := fake.updateIssueCalls[0]
	if call.IssueNumber != 42 || call.Body != "new body content" {
		t.Fatalf("unexpected update call: %+v", call)
	}
	if updated.Body != "new body content" {
		t.Fatalf("unexpected updated issue body: %q", updated.Body)
	}
}

func TestIssueServiceUpdateInternalIssue_GitHubBodyReconcilesLocalAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	draftAsset, err := svc.issueService.UploadDraftInternalIssueAsset(
		project.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload draft issue asset: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		updatedIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:        int64Ptr(1001),
				NodeID:    stringPtr("I_kw_test"),
				Number:    intPtr(42),
				State:     stringPtr("open"),
				Title:     stringPtr("Test Issue"),
				Body:      stringPtr("body with asset"),
				HTMLURL:   stringPtr("https://github.com/owner/repo/issues/42"),
				User:      &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://avatars.example/alice.png")},
				CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt: &gh.Timestamp{Time: time.Now().UTC()},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{42: {}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	bodyWithAsset := fmt.Sprintf("正文引用图片\n\n%s", draftAsset.Markdown)
	if _, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithAsset,
	}); err != nil {
		t.Fatalf("update github issue body with asset: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(issue.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected one attached issue asset, got %d", len(assets))
	}
	if assets[0].ID != draftAsset.Id || assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("unexpected issue asset after attach: %+v", assets[0])
	}
	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.Id})
	if err != nil {
		t.Fatalf("list draft assets: %v", err)
	}
	if len(draftAssets) != 0 {
		t.Fatalf("expected draft asset to be promoted after github body update, got %d", len(draftAssets))
	}
	if got := fake.updateIssueCalls[len(fake.updateIssueCalls)-1].Body; got != bodyWithAsset {
		t.Fatalf("expected github update body to keep local asset reference, got %q", got)
	}

	bodyWithoutAsset := "正文不再引用图片"
	fake.updatedIssue = &ghclient.Issue{
		Issue: gh.Issue{
			ID:        int64Ptr(1001),
			NodeID:    stringPtr("I_kw_test"),
			Number:    intPtr(42),
			State:     stringPtr("open"),
			Title:     stringPtr("Test Issue"),
			Body:      stringPtr(bodyWithoutAsset),
			HTMLURL:   stringPtr("https://github.com/owner/repo/issues/42"),
			User:      &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://avatars.example/alice.png")},
			CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
			UpdatedAt: &gh.Timestamp{Time: time.Now().UTC()},
		},
	}
	if _, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithoutAsset,
	}); err != nil {
		t.Fatalf("update github issue body without asset: %v", err)
	}

	assets, err = svc.issueAssetRepo.ListByIssueID(issue.ID)
	if err != nil {
		t.Fatalf("list issue assets after removal: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("expected issue assets to be reconciled away, got %d", len(assets))
	}
}

func TestIssueServiceUpdateInternalIssue_WritesGitHubLabelsBack(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	updatedAt := time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC)
	fake := &fakeIssueGitHubClient{
		updatedIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:        int64Ptr(1001),
				NodeID:    stringPtr("I_kw_test"),
				Number:    intPtr(42),
				State:     stringPtr("open"),
				Title:     stringPtr("Crash on launch"),
				Body:      stringPtr("App crashes"),
				HTMLURL:   stringPtr("https://github.com/owner/repo/issues/42"),
				User:      &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://avatars.example/alice.png")},
				Labels:    []*gh.Label{{Name: stringPtr("bug"), Color: stringPtr("d73a4a")}, {Name: stringPtr("ios"), Color: stringPtr("0969da")}},
				CreatedAt: &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt: &gh.Timestamp{Time: updatedAt},
				Reactions: &gh.Reactions{},
			},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	labels := []string{"bug", "ios"}
	updated, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("update github issue labels: %v", err)
	}

	if len(fake.updateIssueCalls) != 1 {
		t.Fatalf("expected one github update call, got %d", len(fake.updateIssueCalls))
	}
	call := fake.updateIssueCalls[0]
	if call.IssueNumber != 42 {
		t.Fatalf("unexpected github issue number: %+v", call)
	}
	if len(call.Labels) != 2 || call.Labels[0] != "bug" || call.Labels[1] != "ios" {
		t.Fatalf("unexpected github labels call: %+v", call)
	}
	if updated.Github == nil || len(updated.Github.Labels) != 2 {
		t.Fatalf("expected two github labels on updated issue, got %+v", updated.Github)
	}
	if updated.Github.Labels[0].Name != "bug" || updated.Github.Labels[1].Name != "ios" {
		t.Fatalf("unexpected github labels in response: %+v", updated.Github.Labels)
	}
}

func TestIssueServiceUpdateInternalIssue_RejectsInvalidGitHubState(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	state := model.IssueState("archived")
	if _, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		State: &state,
	}); err == nil {
		t.Fatalf("expected invalid params error, got nil")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params error, got %v", err)
	}
	if len(fake.updateIssueCalls) != 0 {
		t.Fatalf("expected github update to be skipped, got %d calls", len(fake.updateIssueCalls))
	}
}

func TestIssueServiceUpdateInternalIssue_RejectsInvalidGitHubLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	labels := []string{"bug", " "}
	if _, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	}); err == nil {
		t.Fatalf("expected invalid params error, got nil")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params error, got %v", err)
	}
	if len(fake.updateIssueCalls) != 0 {
		t.Fatalf("expected github update to be skipped, got %d calls", len(fake.updateIssueCalls))
	}
}

func TestIssueServiceCreateInternalComment_WritesGitHubCommentBack(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	commentTime := time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC)
	fake := &fakeIssueGitHubClient{
		createdComment: &ghclient.IssueComment{
			IssueComment: gh.IssueComment{
				ID:      int64Ptr(501),
				NodeID:  stringPtr("IC_kw_test"),
				Body:    stringPtr("已在 GitHub 回复"),
				HTMLURL: stringPtr("https://github.com/owner/repo/issues/42#issuecomment-501"),
				User: &gh.User{
					Login:     stringPtr("alice"),
					AvatarURL: stringPtr("https://avatars.example/alice.png"),
				},
				CreatedAt: &gh.Timestamp{Time: commentTime},
				UpdatedAt: &gh.Timestamp{Time: commentTime},
			},
			BodyHTML: stringPtr("<p>已在 <strong>GitHub</strong> 回复</p>"),
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" || owner != "owner" || repo != "repo" {
			t.Fatalf("unexpected github client args: token=%q owner=%q repo=%q", token, owner, repo)
		}
		return fake
	}

	comment, err := svc.issueService.CreateInternalComment(issue.ID, user.ID, CreateInternalIssueCommentRequest{
		Body: "已在 GitHub 回复",
	}, "test")
	if err != nil {
		t.Fatalf("create github comment: %v", err)
	}

	if len(fake.createCommentCalls) != 1 {
		t.Fatalf("expected one github comment call, got %d", len(fake.createCommentCalls))
	}
	if fake.createCommentCalls[0].IssueNumber != 42 || fake.createCommentCalls[0].Body != "已在 GitHub 回复" {
		t.Fatalf("unexpected github comment call: %+v", fake.createCommentCalls[0])
	}
	if comment.Source != model.IssueSourceGitHub || comment.GithubCommentId != 501 || comment.BodyHtml != "<p>已在 <strong>GitHub</strong> 回复</p>" {
		t.Fatalf("unexpected created comment: %+v", comment)
	}

	comments, total, err := svc.issueService.ListComments(issue.ID, user.ID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 {
		t.Fatalf("expected one github comment, got total=%d len=%d", total, len(comments))
	}
}

func TestResolveInternalLabels_ResolvesValidLabels(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "ios", Color: "0969da", Description: "iOS platform"},
		{Name: "enhancement", Color: "a2eeef", Description: "New feature"},
	}

	result, appErr := resolveInternalLabels([]string{"bug", "ios"}, repoLabels)
	if appErr != nil {
		t.Fatalf("unexpected error: %v", appErr)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(result))
	}
	if result[0] != "bug" {
		t.Fatalf("unexpected first label: %q", result[0])
	}
	if result[1] != "ios" {
		t.Fatalf("unexpected second label: %q", result[1])
	}
}

func TestResolveInternalLabels_DeduplicatesCaseInsensitive(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "Bug", Color: "d73a4a", Description: "Something isn't working"},
	}

	result, appErr := resolveInternalLabels([]string{"Bug", "bug", "BUG"}, repoLabels)
	if appErr != nil {
		t.Fatalf("unexpected error: %v", appErr)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 deduplicated label, got %d", len(result))
	}
	if result[0] != "Bug" {
		t.Fatalf("expected original casing, got %q", result[0])
	}
}

func TestResolveInternalLabels_RejectsEmptyNames(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "bug", Color: "d73a4a", Description: ""},
	}

	if _, appErr := resolveInternalLabels([]string{"", "bug"}, repoLabels); appErr == nil {
		t.Fatalf("expected error for empty label name, got nil")
	}
}

func TestResolveInternalLabels_RejectsUnknownLabels(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "bug", Color: "d73a4a", Description: ""},
	}

	_, appErr := resolveInternalLabels([]string{"bug", "nonexistent"}, repoLabels)
	if appErr == nil {
		t.Fatalf("expected error for unknown label, got nil")
	}
	if appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected ErrInvalidParams, got %v", appErr)
	}
}

func TestResolveInternalLabels_AcceptsEmptyInput(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "bug", Color: "d73a4a", Description: ""},
	}

	result, appErr := resolveInternalLabels([]string{}, repoLabels)
	if appErr != nil {
		t.Fatalf("unexpected error: %v", appErr)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 labels, got %d", len(result))
	}
}

func TestResolveInternalLabels_TrimsWhitespace(t *testing.T) {
	repoLabels := []IssueLabelResponse{
		{Name: "bug", Color: "d73a4a", Description: ""},
	}

	result, appErr := resolveInternalLabels([]string{"  bug  "}, repoLabels)
	if appErr != nil {
		t.Fatalf("unexpected error: %v", appErr)
	}
	if len(result) != 1 || result[0] != "bug" {
		t.Fatalf("expected trimmed label, got %+v", result)
	}
}

func TestIssueServiceUpdateInternalIssue_SetsInternalLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "内部问题",
		Body:  api.Ptr("测试标签功能"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("Something isn't working")},
			{Name: stringPtr("ios"), Color: stringPtr("0969da"), Description: stringPtr("iOS platform")},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	labels := []string{"bug", "ios"}
	updated, err := svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("update internal issue labels: %v", err)
	}

	if updated.InternalMeta == nil || len(updated.InternalMeta.Labels) != 2 {
		t.Fatalf("expected 2 internal labels, got %+v", updated.InternalMeta)
	}
	if updated.InternalMeta.Labels[0].Name != "bug" || updated.InternalMeta.Labels[0].Color != "d73a4a" {
		t.Fatalf("unexpected first label: %+v", updated.InternalMeta.Labels[0])
	}
	if updated.InternalMeta.Labels[1].Name != "ios" || updated.InternalMeta.Labels[1].Color != "0969da" {
		t.Fatalf("unexpected second label: %+v", updated.InternalMeta.Labels[1])
	}
}

func TestIssueServiceUpdateInternalIssue_RejectsUnknownInternalLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	created, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "内部问题",
		Body:  api.Ptr("测试标签功能"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("")},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	labels := []string{"bug", "nonexistent"}
	_, err = svc.issueService.UpdateInternalIssue(created.Id, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	})
	if err == nil {
		t.Fatalf("expected error for unknown label, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestIssueServiceList_FiltersByInternalLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	issue1, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "问题 1",
		Body:  api.Ptr("有 bug 标签"),
	})
	if err != nil {
		t.Fatalf("create issue 1: %v", err)
	}

	_, err = svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "问题 2",
		Body:  api.Ptr("没有标签"),
	})
	if err != nil {
		t.Fatalf("create issue 2: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("")},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	labels := []string{"bug"}
	_, err = svc.issueService.UpdateInternalIssue(issue1.Id, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("update issue 1 labels: %v", err)
	}

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{Label: "bug"}, 1, 20)
	if err != nil {
		t.Fatalf("list issues by label: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 filtered issue, got total=%d len=%d", total, len(items))
	}
	if items[0].Title != "问题 1" {
		t.Fatalf("expected issue 1, got %q", items[0].Title)
	}
}

func TestIssueServiceGetFilterOptions_IncludesInternalLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "内部问题",
		Body:  api.Ptr("测试标签"),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("")},
			{Name: stringPtr("enhancement"), Color: stringPtr("a2eeef"), Description: stringPtr("")},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	labels := []string{"bug"}
	_, err = svc.issueService.UpdateInternalIssue(issue.Id, user.ID, UpdateInternalIssueRequest{
		Labels: labels,
	})
	if err != nil {
		t.Fatalf("update internal issue labels: %v", err)
	}

	opts, err := svc.issueService.GetFilterOptions(project.ID, user.ID)
	if err != nil {
		t.Fatalf("get filter options: %v", err)
	}

	found := false
	for _, l := range opts.Labels {
		if l == "bug" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'bug' in filter options labels, got %+v", opts.Labels)
	}
}

func TestIssueServiceCreateGitHubIssue_CreatesGitHubIssueAndSyncsToLocal(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		createdIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:                int64Ptr(201),
				NodeID:            stringPtr("I_kw456"),
				Number:            intPtr(88),
				State:             stringPtr("open"),
				Title:             stringPtr("GitHub 新建问题"),
				Body:              stringPtr("GitHub 问题描述"),
				HTMLURL:           stringPtr("https://github.com/owner/repo/issues/88"),
				User:              &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
				AuthorAssociation: stringPtr("MEMBER"),
				Labels:            []*gh.Label{},
				Comments:          intPtr(0),
				CreatedAt:         &gh.Timestamp{Time: now},
				UpdatedAt:         &gh.Timestamp{Time: now},
			},
			BodyHTML: stringPtr("<p>GitHub 问题描述</p>"),
		},
	}

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	created, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "GitHub 新建问题",
		Body:  api.Ptr("GitHub 问题描述"),
	})
	if err != nil {
		t.Fatalf("create github issue: %v", err)
	}

	if created.Source != model.IssueSourceGitHub {
		t.Fatalf("expected github source, got %+v", created.Source)
	}
	if created.Title != "GitHub 新建问题" {
		t.Fatalf("expected title 'GitHub 新建问题', got %+v", created.Title)
	}
	if created.Github == nil || created.Github.Number != 88 {
		t.Fatalf("expected github meta with number 88, got %+v", created.Github)
	}
	if len(fake.createIssueCalls) != 1 {
		t.Fatalf("expected one create issue call, got %d", len(fake.createIssueCalls))
	}
	if fake.createIssueCalls[0].Title != "GitHub 新建问题" || fake.createIssueCalls[0].Body != "GitHub 问题描述" {
		t.Fatalf("unexpected create issue call payload: %+v", fake.createIssueCalls[0])
	}
}

func TestIssueServiceCreateGitHubIssue_AttachesReferencedDraftAssets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	draftAsset, err := svc.issueService.UploadDraftInternalIssueAsset(
		project.ID,
		user.ID,
		"screenshot.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload draft issue asset: %v", err)
	}

	fake := &fakeIssueGitHubClient{
		createdIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:        gh.Int64(999),
				Number:    intPtr(88),
				Title:     gh.String("GitHub 带图片的问题"),
				Body:      gh.String("描述"),
				HTMLURL:   gh.String("https://github.com/owner/repo/issues/88"),
				State:     gh.String("open"),
				CreatedAt: &gh.Timestamp{Time: time.Now().UTC()},
				UpdatedAt: &gh.Timestamp{Time: time.Now().UTC()},
				User: &gh.User{
					Login:     gh.String("author"),
					AvatarURL: gh.String("https://avatars.githubusercontent.com/u/1"),
				},
				Reactions: &gh.Reactions{},
			},
			BodyHTML: stringPtr("<p>描述</p>"),
		},
	}

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	bodyWithAsset := fmt.Sprintf("问题描述\n\n%s", draftAsset.Markdown)
	created, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "GitHub 带图片的问题",
		Body:  api.Ptr(bodyWithAsset),
	})
	if err != nil {
		t.Fatalf("create github issue: %v", err)
	}

	if created.Title != "GitHub 带图片的问题" {
		t.Fatalf("expected title 'GitHub 带图片的问题', got %+v", created.Title)
	}
	if len(fake.createIssueCalls) != 1 {
		t.Fatalf("expected one create issue call, got %d", len(fake.createIssueCalls))
	}
	callBody := fake.createIssueCalls[0].Body
	if callBody != bodyWithAsset {
		t.Fatalf("expected create issue body to keep local asset reference, got %q", callBody)
	}

	issueAssets, err := svc.issueAssetRepo.ListByIssueID(created.Id)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(issueAssets) != 1 {
		t.Fatalf("expected one attached issue asset, got %d", len(issueAssets))
	}
	if issueAssets[0].ID != draftAsset.Id || issueAssets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("unexpected attached issue asset: %+v", issueAssets[0])
	}

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.Id})
	if err != nil {
		t.Fatalf("list draft assets: %v", err)
	}
	if len(draftAssets) != 0 {
		t.Fatalf("expected referenced draft asset to be removed after github issue creation, got %d", len(draftAssets))
	}
}

func TestIssueServiceCreateGitHubIssue_ReturnsErrorWhenReferencedDraftAssetNotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	fake := &fakeIssueGitHubClient{}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	// body 中引用了一个不存在的 draft asset（使用符合 UUID 格式的假 ID，否则正则无法提取）
	bodyWithMissingAsset := "问题描述\n\n![image](/api/issues/assets/00000000-0000-0000-0000-000000000000/content)"
	_, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "GitHub 带图片的问题",
		Body:  api.Ptr(bodyWithMissingAsset),
	})
	if err == nil {
		t.Fatal("expected error when referenced draft asset not found, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
	if len(fake.createIssueCalls) != 0 {
		t.Fatalf("expected no create issue call, got %d", len(fake.createIssueCalls))
	}
}

func TestIssueServiceCreateGitHubIssue_RejectsEmptyTitle(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	_, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "   ",
		Body:  api.Ptr("desc"),
	})
	if err == nil {
		t.Fatal("expected error for empty title, got nil")
	}
}

func TestIssueServiceCreateGitHubIssue_ProjectNotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")

	_, err := svc.issueService.CreateGitHubIssue("non-existent-project", user.ID, CreateInternalIssueRequest{
		Title: "title",
		Body:  api.Ptr("desc"),
	})
	if err == nil {
		t.Fatal("expected error for non-existent project, got nil")
	}
}

func TestIssueServiceCreateGitHubIssue_GitHubAPIFailure(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	fake := &fakeIssueGitHubClient{
		createIssueErr: fmt.Errorf("github api error"),
	}

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	_, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "title",
		Body:  api.Ptr("desc"),
	})
	if err == nil {
		t.Fatal("expected error when github api fails, got nil")
	}
}

func TestIssueServiceSyncProjectIssues_RejectsNotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-nogit")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = nil
	})

	_, err := svc.issueService.SyncProjectIssues(project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok {
		t.Fatalf("expected *errs.AppError, got %T", err)
	}
	if appErr.Code != errs.ErrProjectGitHubNotConfigured.Code {
		t.Errorf("expected code %d, got %d", errs.ErrProjectGitHubNotConfigured.Code, appErr.Code)
	}
}

// 评论与时间线各自跨页聚合后单事务提交；再同步一次靠唯一键幂等，不产生重复行。
func TestIssueServiceSyncProjectIssues_PagedCollectionsCommitAndResyncDedupes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		commentsPages: map[int][][]*ghclient.IssueComment{
			42: {
				{testGitHubSyncComment(501, "first", now), testGitHubSyncComment(502, "second", now)},
				{testGitHubSyncComment(503, "third", now)},
			},
		},
		timelinePages: map[int][][]*ghclient.TimelineEvent{
			42: {
				{testGitHubSyncTimelineEvent(701, "labeled", now), testGitHubSyncTimelineEvent(702, "closed", now)},
				{testGitHubSyncTimelineEvent(703, "reopened", now)},
			},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err != nil {
		t.Fatalf("sync project issues: %v", err)
	}
	if result.SyncedIssueCount != 1 || result.SyncedCommentCount != 3 || result.SyncedTimelineCount != 3 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	issueID := syncedGitHubIssueID(t, svc, project.ID)
	if got := storedCommentsByGitHubID(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected 3 stored comments, got %+v", got)
	}
	if got := storedTimelineByKey(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected 3 stored timeline events, got %+v", got)
	}

	// 重复同步：upsert 撞唯一键更新既有行，集合行数不变。
	result, err = svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err != nil {
		t.Fatalf("resync project issues: %v", err)
	}
	if result.SyncedCommentCount != 3 || result.SyncedTimelineCount != 3 {
		t.Fatalf("unexpected resync result: %+v", result)
	}
	if got := storedCommentsByGitHubID(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected comments to stay deduplicated after resync, got %+v", got)
	}
	if got := storedTimelineByKey(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected timeline events to stay deduplicated after resync, got %+v", got)
	}
}

// 评论第二页拉取失败：第一页已映射的增/改与远端缺失清理都不提交，集合保持同步前数据。
func TestIssueServiceSyncProjectIssues_CommentPageFailureKeepsCollection(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "old body", now), testGitHubSyncComment(599, "stale", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮：第一页改 500 并新增 501、丢弃 599，第二页失败。
	fake.commentsPages = map[int][][]*ghclient.IssueComment{
		42: {
			{testGitHubSyncComment(500, "new body", now), testGitHubSyncComment(501, "new", now)},
			{testGitHubSyncComment(502, "unreached", now)},
		},
	}
	fake.commentErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err == nil {
		t.Fatal("expected sync error on second comments page, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}

	stored := storedCommentsByGitHubID(t, svc, issueID)
	if len(stored) != 2 {
		t.Fatalf("expected comment collection untouched, got %+v", stored)
	}
	if stored[500].Body != "old body" {
		t.Fatalf("expected comment 500 update rolled back, got %q", stored[500].Body)
	}
	if _, ok := stored[501]; ok {
		t.Fatalf("expected comment 501 not inserted, got %+v", stored[501])
	}
	if _, ok := stored[599]; !ok {
		t.Fatalf("expected stale comment 599 kept after failed sync")
	}

	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusFailed {
		t.Fatalf("expected failed sync state, got %q", state.Status)
	}
}

// 时间线第二页拉取失败：事件集合保持同步前数据；评论是独立原子边界，本轮照常提交。
func TestIssueServiceSyncProjectIssues_TimelinePageFailureKeepsCollection(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now), testGitHubSyncTimelineEvent(799, "subscribed", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮：评论集合正常更新（独立原子边界，先提交）；时间线第一页把 700 改成
	// closed 并新增 701、丢弃 799，第二页失败。
	fake.comments = map[int][]*ghclient.IssueComment{
		42: {testGitHubSyncComment(500, "committed despite timeline failure", now)},
	}
	fake.timelinePages = map[int][][]*ghclient.TimelineEvent{
		42: {
			{testGitHubSyncTimelineEvent(700, "closed", now), testGitHubSyncTimelineEvent(701, "reopened", now)},
			{testGitHubSyncTimelineEvent(702, "unreached", now)},
		},
	}
	fake.timelineErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err == nil {
		t.Fatal("expected sync error on second timeline page, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}

	storedComments := storedCommentsByGitHubID(t, svc, issueID)
	if got := storedComments[500]; got.Body != "committed despite timeline failure" {
		t.Fatalf("expected comments committed independently of timeline failure, got %+v", storedComments)
	}

	stored := storedTimelineByKey(t, svc, issueID)
	if len(stored) != 2 {
		t.Fatalf("expected timeline collection untouched, got %+v", stored)
	}
	if stored["gh:700"].EventType != "labeled" {
		t.Fatalf("expected event 700 update rolled back, got %q", stored["gh:700"].EventType)
	}
	if _, ok := stored["gh:701"]; ok {
		t.Fatalf("expected event 701 not inserted")
	}
	if _, ok := stored["gh:799"]; !ok {
		t.Fatalf("expected stale event 799 kept after failed sync")
	}
}

// 远端返回空集合：GitHub 镜像行全部清掉；source='internal' 的本地评论保留。
func TestIssueServiceSyncProjectIssues_EmptyRemoteCleansMirrorKeepsInternal(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "mirror", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 本地来源评论（负数合成 ID）与 GitHub 镜像行共用同一张表。
	internal := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          model.IssueSourceInternal,
		GitHubCommentID: -1,
		Body:            "local note",
		AuthorLogin:     user.Username,
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}
	if err := svc.db.Create(internal).Error; err != nil {
		t.Fatalf("seed internal comment: %v", err)
	}

	fake.comments = nil
	fake.timeline = nil

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err != nil {
		t.Fatalf("resync with empty remote: %v", err)
	}
	if result.SyncedCommentCount != 0 || result.SyncedTimelineCount != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	stored := storedCommentsByGitHubID(t, svc, issueID)
	if len(stored) != 1 {
		t.Fatalf("expected only the internal comment to remain, got %+v", stored)
	}
	kept, ok := stored[-1]
	if !ok || kept.ID != internal.ID || kept.Source != model.IssueSourceInternal {
		t.Fatalf("expected internal comment preserved, got %+v", stored)
	}

	if got := storedTimelineByKey(t, svc, issueID); len(got) != 0 {
		t.Fatalf("expected timeline mirror cleaned, got %+v", got)
	}
}
