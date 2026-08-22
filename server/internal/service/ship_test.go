package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
)

func TestShipServiceCheck_ReturnsDetailedMissingItems(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = []byte{}
	})
	version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	check, err := svc.shipService.Check(version.ID, project.UserID)
	if err != nil {
		t.Fatalf("ship check: %v", err)
	}

	if check.CanShip {
		t.Fatalf("expected ship check to fail")
	}
	if len(check.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(check.Items))
	}

	assertCheckItem := func(key string, ok bool) {
		t.Helper()
		for _, item := range check.Items {
			if item.Key == key {
				if item.OK != ok {
					t.Fatalf("expected %s ok=%v, got %v", key, ok, item.OK)
				}
				return
			}
		}
		t.Fatalf("missing check item %s", key)
	}

	assertCheckItem("release_notes", false)
	assertCheckItem("artifacts", false)
	assertCheckItem("target_commitish", false)
	assertCheckItem("github_config", false)
}

func TestShipServiceShip_SuccessUpdatesVersionAndUploadsAssets(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		release: &gh.RepositoryRelease{
			ID:      int64Ptr(42),
			HTMLURL: stringPtr("https://github.com/owner/repo/releases/tag/v1.0.0"),
		},
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		if owner != project.GithubOwner || repo != project.GithubRepo {
			t.Fatalf("unexpected repo: %s/%s", owner, repo)
		}
		return fake
	}

	if _, err := svc.shipService.Ship(version.ID, project.UserID); err != nil {
		t.Fatalf("ship version: %v", err)
	}

	updated, err := svc.versionRepo.FindByID(version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}

	if updated.Status != model.VersionStatusShipped {
		t.Fatalf("expected shipped status, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusCompleted {
		t.Fatalf("expected completed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageFinalize {
		t.Fatalf("expected finalize ship stage, got %q", updated.ShipStage)
	}
	if updated.ShippedAt == nil {
		t.Fatalf("expected shipped_at to be set")
	}
	if updated.GithubReleaseURL != "https://github.com/owner/repo/releases/tag/v1.0.0" {
		t.Fatalf("unexpected release url: %q", updated.GithubReleaseURL)
	}
	if updated.ErrorLog != "" {
		t.Fatalf("expected empty error log, got %q", updated.ErrorLog)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createTagCalls != 1 {
		t.Fatalf("expected create tag once, got %d", fake.createTagCalls)
	}
	if fake.createReleaseCalls != 1 {
		t.Fatalf("expected create release once, got %d", fake.createReleaseCalls)
	}
	if len(fake.uploadedAssets) != 1 || fake.uploadedAssets[0] != "app.apk" {
		t.Fatalf("unexpected uploaded assets: %#v", fake.uploadedAssets)
	}
}

func TestShipServiceShip_CreateReleaseFailureRecordsFailureState(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		createReleaseErr: errors.New("release api down"),
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}

	_, err := svc.shipService.Ship(version.ID, project.UserID)
	if err == nil {
		t.Fatalf("expected ship to fail")
	}
	if !strings.Contains(err.Error(), "创建 Release 失败") {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, reloadErr := svc.versionRepo.FindByID(version.ID)
	if reloadErr != nil {
		t.Fatalf("reload version: %v", reloadErr)
	}

	if updated.Status != model.VersionStatusPending {
		t.Fatalf("expected pending status after failure, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusFailed {
		t.Fatalf("expected failed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageCreateRelease {
		t.Fatalf("expected create release stage, got %q", updated.ShipStage)
	}
	if !strings.Contains(updated.ErrorLog, "release api down") {
		t.Fatalf("expected error log to contain release failure, got %q", updated.ErrorLog)
	}
	if updated.GithubReleaseURL != "" {
		t.Fatalf("expected empty release url after failure, got %q", updated.GithubReleaseURL)
	}
}

func TestShipServiceShip_CreateTagPermissionFailureUsesActionableMessage(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubOwner = "liuyincs"
		p.GithubRepo = "musiver"
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		createTagErr: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusForbidden},
			Message:  "Resource not accessible by personal access token",
		},
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}

	_, err := svc.shipService.Ship(version.ID, project.UserID)
	if err == nil {
		t.Fatalf("expected ship to fail")
	}
	if !strings.Contains(err.Error(), "GitHub Token 无权操作仓库 liuyincs/musiver") {
		t.Fatalf("expected actionable permission error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Contents: Read and write") {
		t.Fatalf("expected permission hint in error, got %v", err)
	}

	updated, reloadErr := svc.versionRepo.FindByID(version.ID)
	if reloadErr != nil {
		t.Fatalf("reload version: %v", reloadErr)
	}
	if !strings.Contains(updated.ErrorLog, "Resource owner 为 liuyincs") {
		t.Fatalf("expected stored error log to contain owner guidance, got %q", updated.ErrorLog)
	}
}

func TestShipServiceShip_UploadFailureRecordsFailureState(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		release:        &gh.RepositoryRelease{ID: int64Ptr(42)},
		uploadAssetErr: errors.New("asset upload failed"),
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}

	_, err := svc.shipService.Ship(version.ID, project.UserID)
	if err == nil {
		t.Fatalf("expected ship to fail")
	}
	if !strings.Contains(err.Error(), "上传安装包失败") {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, reloadErr := svc.versionRepo.FindByID(version.ID)
	if reloadErr != nil {
		t.Fatalf("reload version: %v", reloadErr)
	}

	if updated.Status != model.VersionStatusPending {
		t.Fatalf("expected pending status after failure, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusFailed {
		t.Fatalf("expected failed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageUploadAssets {
		t.Fatalf("expected upload assets stage, got %q", updated.ShipStage)
	}
	if !strings.Contains(updated.ErrorLog, "asset upload failed") {
		t.Fatalf("expected error log to contain upload failure, got %q", updated.ErrorLog)
	}
}

type fakeGitHubClient struct {
	mu sync.Mutex

	validateErr      error
	createTagErr     error
	createReleaseErr error
	uploadAssetErr   error
	release          *gh.RepositoryRelease

	createTagCalls     int
	createReleaseCalls int
	uploadedAssets     []string
}

func (f *fakeGitHubClient) ValidateRepository(context.Context) error {
	return f.validateErr
}

func (f *fakeGitHubClient) CreateTag(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createTagCalls++
	return f.createTagErr
}

func (f *fakeGitHubClient) CreateRelease(context.Context, string, string, string) (*gh.RepositoryRelease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReleaseCalls++
	if f.createReleaseErr != nil {
		return nil, f.createReleaseErr
	}
	return f.release, nil
}

func (f *fakeGitHubClient) UploadAsset(_ context.Context, _ int64, filename string, _ *os.File) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadedAssets = append(f.uploadedAssets, filename)
	return f.uploadAssetErr
}

func int64Ptr(v int64) *int64 {
	return &v
}

func stringPtr(v string) *string {
	return &v
}

func uploadShipTestArtifact(t *testing.T, svc *testServices, versionID, userID string) {
	t.Helper()
	if _, err := svc.artifactService.Upload(
		versionID,
		userID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
}

func stubSuccessfulShipClient(t *testing.T, svc *testServices, releaseURL string) *fakeGitHubClient {
	t.Helper()
	if releaseURL == "" {
		releaseURL = "https://github.com/owner/repo/releases/tag/v1.0.0"
	}
	fake := &fakeGitHubClient{
		release: &gh.RepositoryRelease{
			ID:      int64Ptr(42),
			HTMLURL: stringPtr(releaseURL),
		},
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}
	return fake
}

func TestShipServiceShip_ExecutesInternalIssueShipHook(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "Ship hook target",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}

	commentBody := "已随 {version} 发出。{release_url}"
	workflow := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody:    &commentBody,
		Close:          true,
		WorkflowStatus: &workflow,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	check, err := svc.shipService.Check(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship check: %v", err)
	}
	if len(check.PendingIssueHooks) != 1 {
		t.Fatalf("expected 1 pending hook before ship, got %+v", check.PendingIssueHooks)
	}
	if check.PendingIssueHooks[0].IssueID != issue.ID || !check.PendingIssueHooks[0].Comment || !check.PendingIssueHooks[0].Close {
		t.Fatalf("unexpected pending hook: %+v", check.PendingIssueHooks[0])
	}

	releaseURL := "https://github.com/owner/repo/releases/tag/v1.0.0"
	stubSuccessfulShipClient(t, svc, releaseURL)

	result, err := svc.shipService.Ship(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship version: %v", err)
	}
	if result.HookTotal != 1 || result.HookFailed != 0 {
		t.Fatalf("unexpected ship result: %+v", result)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.State != model.IssueStateClosed || got.StateReason != "completed" {
		t.Fatalf("expected closed completed issue, got state=%q reason=%q", got.State, got.StateReason)
	}
	if got.InternalMeta == nil || got.InternalMeta.WorkflowStatus != model.IssueWorkflowStatusDone {
		t.Fatalf("expected workflow done, got %+v", got.InternalMeta)
	}
	if got.ShipHook == nil || got.ShipHook.Status != string(model.IssueShipHookStatusFired) {
		t.Fatalf("expected fired ship hook, got %+v", got.ShipHook)
	}
	wantComment := "已随 v1.0.0 发出。" + releaseURL
	if got.ShipHook.CommentBody != wantComment {
		t.Fatalf("expected rendered comment %q, got %q", wantComment, got.ShipHook.CommentBody)
	}

	comments, _, err := svc.issueService.ListComments(issue.ID, user.ID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != wantComment {
		t.Fatalf("expected one internal comment with rendered body, got %+v", comments)
	}
}

func TestShipServiceShip_FailedShipDoesNotConsumeHook(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue := createTestIssue(t, svc.db, project.ID)
	workflow := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &workflow,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return &fakeGitHubClient{createReleaseErr: errors.New("release api down")}
	}

	if _, err := svc.shipService.Ship(version.ID, user.ID); err == nil {
		t.Fatalf("expected ship to fail")
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil || got.ShipHook.Status != string(model.IssueShipHookStatusPending) {
		t.Fatalf("expected pending hook after failed ship, got %+v", got.ShipHook)
	}
}

func TestShipServiceShip_SkipsCloseOnClosedIssueStillComments(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "Already closed",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	closed := model.IssueStateClosed
	reason := "completed"
	if _, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{
		State:       &closed,
		StateReason: &reason,
	}); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	commentBody := "closed issue comment"
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &commentBody,
		Close:       true,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	stubSuccessfulShipClient(t, svc, "")

	result, err := svc.shipService.Ship(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship version: %v", err)
	}
	if result.HookFailed != 0 {
		t.Fatalf("expected no hook failures, got %+v", result)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil || got.ShipHook.Results == nil || got.ShipHook.Results.Close == nil {
		t.Fatalf("expected close result, got %+v", got.ShipHook)
	}
	if !got.ShipHook.Results.Close.OK || !got.ShipHook.Results.Close.Skipped {
		t.Fatalf("expected close skipped with ok=true, got %+v", got.ShipHook.Results.Close)
	}
	if got.ShipHook.Results.Comment == nil || !got.ShipHook.Results.Comment.OK {
		t.Fatalf("expected comment ok, got %+v", got.ShipHook.Results.Comment)
	}
}

func TestShipServiceShip_SkipsWorkflowWhenAlreadyDone(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "Done issue",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	done := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, done, "test"); err != nil {
		t.Fatalf("set workflow done: %v", err)
	}
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &done,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	stubSuccessfulShipClient(t, svc, "")

	result, err := svc.shipService.Ship(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship version: %v", err)
	}
	if result.HookFailed != 0 {
		t.Fatalf("expected no hook failures, got %+v", result)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil || got.ShipHook.Results == nil || got.ShipHook.Results.WorkflowStatus == nil {
		t.Fatalf("expected workflow result, got %+v", got.ShipHook)
	}
	if !got.ShipHook.Results.WorkflowStatus.OK || !got.ShipHook.Results.WorkflowStatus.Skipped {
		t.Fatalf("expected workflow skipped with ok=true, got %+v", got.ShipHook.Results.WorkflowStatus)
	}
}

func TestShipServiceShip_GitHubCommentFailureStillShips(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue := createTestIssue(t, svc.db, project.ID)
	commentBody := "github comment"
	workflow := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody:    &commentBody,
		Close:          true,
		WorkflowStatus: &workflow,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	closedAt := time.Now().UTC()
	fakeIssueGH := &fakeIssueGitHubClient{
		createCommentErr: errors.New("comment api down"),
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
				User:        &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://avatars.example/alice.png")},
				CreatedAt:   &gh.Timestamp{Time: issue.CreatedAt},
				UpdatedAt:   &gh.Timestamp{Time: closedAt},
				ClosedAt:    &gh.Timestamp{Time: closedAt},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{42: {}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fakeIssueGH
	}
	stubSuccessfulShipClient(t, svc, "")

	result, err := svc.shipService.Ship(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship should succeed despite hook comment failure: %v", err)
	}
	if result.HookTotal != 1 || result.HookFailed != 1 {
		t.Fatalf("unexpected ship result: %+v", result)
	}

	updated, err := svc.versionRepo.FindByID(version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if updated.Status != model.VersionStatusShipped {
		t.Fatalf("expected shipped version, got %q", updated.Status)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil || got.ShipHook.Status != string(model.IssueShipHookStatusFired) {
		t.Fatalf("expected fired hook, got %+v", got.ShipHook)
	}
	if got.ShipHook.Results == nil || got.ShipHook.Results.Comment == nil || got.ShipHook.Results.Comment.OK {
		t.Fatalf("expected comment failure, got %+v", got.ShipHook.Results)
	}
	if got.ShipHook.Results.WorkflowStatus == nil || !got.ShipHook.Results.WorkflowStatus.OK {
		t.Fatalf("expected workflow to continue after comment failure, got %+v", got.ShipHook.Results)
	}
	if got.ShipHook.Results.Close == nil || !got.ShipHook.Results.Close.OK {
		t.Fatalf("expected close to continue after comment failure, got %+v", got.ShipHook.Results)
	}
	if len(fakeIssueGH.updateIssueCalls) == 0 {
		t.Fatalf("expected close to call GitHub update")
	}
}

func TestShipServiceShip_ConsumesOnlySameProjectHooks(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	projectA := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	projectB := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, projectA.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issueA1, err := svc.issueService.CreateInternalIssue(projectA.ID, user.ID, CreateInternalIssueRequest{Title: "A1"})
	if err != nil {
		t.Fatalf("create issue A1: %v", err)
	}
	issueA2, err := svc.issueService.CreateInternalIssue(projectA.ID, user.ID, CreateInternalIssueRequest{Title: "A2"})
	if err != nil {
		t.Fatalf("create issue A2: %v", err)
	}
	issueB := createTestIssue(t, svc.db, projectB.ID)

	workflow := model.IssueWorkflowStatusDone
	for _, issueID := range []string{issueA1.ID, issueA2.ID, issueB.ID} {
		if _, err := svc.issueService.UpsertShipHook(issueID, user.ID, UpsertShipHookRequest{
			WorkflowStatus: &workflow,
		}); err != nil {
			t.Fatalf("upsert hook for %s: %v", issueID, err)
		}
	}

	stubSuccessfulShipClient(t, svc, "")

	result, err := svc.shipService.Ship(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship version: %v", err)
	}
	if result.HookTotal != 2 {
		t.Fatalf("expected 2 consumed hooks, got %+v", result)
	}

	for _, issueID := range []string{issueA1.ID, issueA2.ID} {
		got, err := svc.issueService.Get(issueID, user.ID)
		if err != nil {
			t.Fatalf("get issue %s: %v", issueID, err)
		}
		if got.ShipHook == nil || got.ShipHook.Status != string(model.IssueShipHookStatusFired) {
			t.Fatalf("expected fired hook for %s, got %+v", issueID, got.ShipHook)
		}
	}

	gotB, err := svc.issueService.Get(issueB.ID, user.ID)
	if err != nil {
		t.Fatalf("get other project issue: %v", err)
	}
	if gotB.ShipHook == nil || gotB.ShipHook.Status != string(model.IssueShipHookStatusPending) {
		t.Fatalf("expected other project hook to remain pending, got %+v", gotB.ShipHook)
	}
}

func TestShipServiceCheck_ReturnsEmptyPendingIssueHooks(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	check, err := svc.shipService.Check(version.ID, user.ID)
	if err != nil {
		t.Fatalf("ship check: %v", err)
	}
	if check.PendingIssueHooks == nil {
		t.Fatalf("expected non-nil pending_issue_hooks slice")
	}
	if len(check.PendingIssueHooks) != 0 {
		t.Fatalf("expected empty pending hooks, got %+v", check.PendingIssueHooks)
	}
}

func TestShipServiceShip_PreservesUnknownPlaceholders(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	uploadShipTestArtifact(t, svc, version.ID, user.ID)

	issue, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "Placeholder issue",
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	commentBody := "ver={version} url={release_url} unknown={unknown}"
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &commentBody,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	releaseURL := "https://github.com/owner/repo/releases/tag/v1.0.0"
	stubSuccessfulShipClient(t, svc, releaseURL)

	if _, err := svc.shipService.Ship(version.ID, user.ID); err != nil {
		t.Fatalf("ship version: %v", err)
	}

	want := "ver=v1.0.0 url=" + releaseURL + " unknown={unknown}"
	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil || got.ShipHook.CommentBody != want {
		t.Fatalf("expected rendered comment %q, got %+v", want, got.ShipHook)
	}
}
