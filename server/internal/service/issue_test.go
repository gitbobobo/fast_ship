package service

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
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
	if issues[0].Title != "Crash on launch" || issues[0].GitHub == nil || issues[0].GitHub.Labels[0].Name != "bug" {
		t.Fatalf("unexpected issue payload: %+v", issues[0])
	}
	if issues[0].BodyHTML != "<p>App crashes on <strong>startup</strong></p>" {
		t.Fatalf("expected issue html body to be persisted, got %+v", issues[0])
	}

	comments, total, err := svc.issueService.ListComments(issues[0].ID, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 || comments[0].Author.Login != "bob" {
		t.Fatalf("unexpected comments payload: %+v", comments)
	}
	if comments[0].BodyHTML == "" {
		t.Fatalf("expected comment html body to be persisted, got %+v", comments[0])
	}

	events, total, err := svc.issueService.ListTimeline(issues[0].ID, project.UserID, 1, 20)
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

type fakeIssueGitHubClient struct {
	issues             []*ghclient.Issue
	repoLabels         []*gh.Label
	repoLabelsErr      error
	comments           map[int][]*ghclient.IssueComment
	timeline           map[int][]*ghclient.TimelineEvent
	createdComment     *ghclient.IssueComment
	updatedIssue       *ghclient.Issue
	createdIssue       *ghclient.Issue
	createIssueErr     error
	createIssueCalls   []fakeCreateIssueCall
	createCommentCalls []fakeCreateCommentCall
	updateIssueCalls   []fakeUpdateIssueCall
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

func (f *fakeIssueGitHubClient) ListIssueComments(_ context.Context, issueNumber, _, _ int) ([]*ghclient.IssueComment, *gh.Response, error) {
	return f.comments[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListIssueTimeline(_ context.Context, issueNumber, _, _ int) ([]*ghclient.TimelineEvent, *gh.Response, error) {
	return f.timeline[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) CreateIssueComment(_ context.Context, issueNumber int, body string) (*ghclient.IssueComment, error) {
	f.createCommentCalls = append(f.createCommentCalls, fakeCreateCommentCall{
		IssueNumber: issueNumber,
		Body:        body,
	})
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

func intPtr(v int) *int {
	return &v
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

	meta, err := svc.issueService.UpdateInternalMeta(issue.ID, project.UserID, model.IssueWorkflowStatusInProgress)
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

func TestIssueServiceUpdateInternalMeta_KeepsFirstCompletedAtAcrossReopen(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	firstDone, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusDone)
	if err != nil {
		t.Fatalf("mark issue done the first time: %v", err)
	}
	if firstDone == nil || firstDone.CompletedAt == nil {
		t.Fatalf("expected first completion timestamp, got %+v", firstDone)
	}
	firstCompletedAt := *firstDone.CompletedAt

	reopened, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusInProgress)
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

	doneAgain, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusDone)
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
			{Title: "定位问题", IsCompleted: true},
			{Title: "修复问题", IsCompleted: false},
		},
	})
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
		Body:           "## 检查项\n\n- 校验版本说明",
		WorkflowStatus: model.IssueWorkflowStatusTodo,
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
	if created.GitHub != nil {
		t.Fatalf("expected no github payload, got %+v", created.GitHub)
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
		Body:  fmt.Sprintf("创建时直接引用图片\n\n%s", draftAsset.Markdown),
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 attached issue asset, got %d", len(assets))
	}
	if assets[0].ID != draftAsset.ID {
		t.Fatalf("expected draft asset %q to be attached, got %q", draftAsset.ID, assets[0].ID)
	}
	if assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("expected asset status attached, got %q", assets[0].Status)
	}

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.ID})
	if err != nil {
		t.Fatalf("list draft assets: %v", err)
	}
	if len(draftAssets) != 0 {
		t.Fatalf("expected referenced draft asset to be removed after attach, got %d", len(draftAssets))
	}

	reader, mimeType, fileSize, err := svc.issueService.GetIssueAssetContent(draftAsset.ID, user.ID)
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
		ID:              draftAsset.ID,
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
		Body:  fmt.Sprintf("创建时直接引用图片\n\n%s", draftAsset.Markdown),
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

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.ID})
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
		Body:  "引用了不存在的图片 ![missing](/api/issues/assets/11111111-1111-1111-1111-111111111111/content)",
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
		Body:  "old body",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	title := "更新后的标题"
	body := "new body"
	state := model.IssueStateClosed
	updated, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
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
		Body:  "初始内容",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	bodyWithAsset := fmt.Sprintf("保留正文\n\n%s", asset.Markdown)
	if _, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithAsset,
	}); err != nil {
		t.Fatalf("attach asset markdown: %v", err)
	}

	newBody := "不再引用图片"
	if _, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Body: &newBody,
	}); err != nil {
		t.Fatalf("remove asset markdown: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("expected issue assets to be deleted, got %d", len(assets))
	}

	exists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.ID, asset.ID))
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
		Body:  "初始内容",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	bodyWithAsset := fmt.Sprintf("保留正文\n\n%s", asset.Markdown)
	if _, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Body: &bodyWithAsset,
	}); err != nil {
		t.Fatalf("attach asset markdown: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
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
		Body:  "初始内容",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	asset, err := svc.issueService.UploadInternalIssueAsset(
		created.ID,
		user.ID,
		"clip.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload issue asset: %v", err)
	}

	body := "保留正文但不再引用图片"
	if _, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Body: &body,
	}); err != nil {
		t.Fatalf("update internal issue: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 0 {
		t.Fatalf("expected pending issue asset to be deleted, got %d", len(assets))
	}

	exists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.ID, asset.ID))
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
		Body:  "初始内容",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	expiredPending, err := svc.issueService.UploadInternalIssueAsset(
		created.ID,
		user.ID,
		"expired.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload expired pending asset: %v", err)
	}
	attached, err := svc.issueService.UploadInternalIssueAsset(
		created.ID,
		user.ID,
		"keep.png",
		int64(len(testPNGBytes)),
		bytes.NewReader(testPNGBytes),
	)
	if err != nil {
		t.Fatalf("upload attached asset: %v", err)
	}
	if err := svc.db.Model(&model.IssueAsset{}).
		Where("id = ?", attached.ID).
		Update("status", model.IssueAssetStatusAttached).
		Error; err != nil {
		t.Fatalf("mark asset attached: %v", err)
	}

	expiredAt := time.Now().UTC().Add(-issueAssetPendingTTL - time.Hour)
	if err := svc.db.Model(&model.IssueAsset{}).
		Where("id = ?", expiredPending.ID).
		Update("created_at", expiredAt).
		Error; err != nil {
		t.Fatalf("age pending asset: %v", err)
	}

	if err := svc.issueService.CleanupExpiredPendingIssueAssets(); err != nil {
		t.Fatalf("cleanup expired pending issue assets: %v", err)
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 remaining issue asset, got %d", len(assets))
	}
	if assets[0].ID != attached.ID {
		t.Fatalf("expected attached asset %q to remain, got %q", attached.ID, assets[0].ID)
	}
	if assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("expected remaining asset to stay attached, got %q", assets[0].Status)
	}

	expiredExists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.ID, expiredPending.ID))
	if err != nil {
		t.Fatalf("stat expired pending asset: %v", err)
	}
	if expiredExists {
		t.Fatalf("expected expired pending asset file to be deleted")
	}

	attachedExists, err := svc.storage.Exists(fmt.Sprintf("%s/issues/%s/assets/%s.png", project.ID, created.ID, attached.ID))
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
		Body:  "issue body",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	comment, err := svc.issueService.CreateInternalComment(created.ID, user.ID, CreateInternalIssueCommentRequest{
		Body: "第一条内部评论",
	})
	if err != nil {
		t.Fatalf("create internal comment: %v", err)
	}

	if comment.Source != model.IssueSourceInternal || comment.Author.Login != user.Username {
		t.Fatalf("unexpected created comment: %+v", comment)
	}

	comments, total, err := svc.issueService.ListComments(created.ID, user.ID, 1, 20)
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
	reason := "completed"
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
	if assets[0].ID != draftAsset.ID || assets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("unexpected issue asset after attach: %+v", assets[0])
	}
	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.ID})
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
		Labels: &labels,
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
	if updated.GitHub == nil || len(updated.GitHub.Labels) != 2 {
		t.Fatalf("expected two github labels on updated issue, got %+v", updated.GitHub)
	}
	if updated.GitHub.Labels[0].Name != "bug" || updated.GitHub.Labels[1].Name != "ios" {
		t.Fatalf("unexpected github labels in response: %+v", updated.GitHub.Labels)
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
		Labels: &labels,
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
	})
	if err != nil {
		t.Fatalf("create github comment: %v", err)
	}

	if len(fake.createCommentCalls) != 1 {
		t.Fatalf("expected one github comment call, got %d", len(fake.createCommentCalls))
	}
	if fake.createCommentCalls[0].IssueNumber != 42 || fake.createCommentCalls[0].Body != "已在 GitHub 回复" {
		t.Fatalf("unexpected github comment call: %+v", fake.createCommentCalls[0])
	}
	if comment.Source != model.IssueSourceGitHub || comment.GitHubCommentID != 501 || comment.BodyHTML != "<p>已在 <strong>GitHub</strong> 回复</p>" {
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
		Body:  "测试标签功能",
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
	updated, err := svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Labels: &labels,
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
		Body:  "测试标签功能",
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
	_, err = svc.issueService.UpdateInternalIssue(created.ID, user.ID, UpdateInternalIssueRequest{
		Labels: &labels,
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
		Body:  "有 bug 标签",
	})
	if err != nil {
		t.Fatalf("create issue 1: %v", err)
	}

	_, err = svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "问题 2",
		Body:  "没有标签",
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
	_, err = svc.issueService.UpdateInternalIssue(issue1.ID, user.ID, UpdateInternalIssueRequest{
		Labels: &labels,
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
		Body:  "测试标签",
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
	_, err = svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		Labels: &labels,
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
		Body:  "GitHub 问题描述",
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
	if created.GitHub == nil || created.GitHub.Number != 88 {
		t.Fatalf("expected github meta with number 88, got %+v", created.GitHub)
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
		Body:  bodyWithAsset,
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

	issueAssets, err := svc.issueAssetRepo.ListByIssueID(created.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(issueAssets) != 1 {
		t.Fatalf("expected one attached issue asset, got %d", len(issueAssets))
	}
	if issueAssets[0].ID != draftAsset.ID || issueAssets[0].Status != model.IssueAssetStatusAttached {
		t.Fatalf("unexpected attached issue asset: %+v", issueAssets[0])
	}

	draftAssets, err := svc.issueDraftAssetRepo.ListByProjectIDAndIDs(project.ID, []string{draftAsset.ID})
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
		Body:  bodyWithMissingAsset,
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
		Body:  "desc",
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
		Body:  "desc",
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
		Body:  "desc",
	})
	if err == nil {
		t.Fatal("expected error when github api fails, got nil")
	}
}
