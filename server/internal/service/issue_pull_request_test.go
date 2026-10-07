package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func testPullRequest(repoFullName string, number int, state string, mergedAt *gh.Timestamp) *gh.PullRequest {
	pr := &gh.PullRequest{
		Number:  intPtr(number),
		State:   gh.String(state),
		Title:   gh.String(fmt.Sprintf("Fix crash #%d", number)),
		HTMLURL: gh.String(fmt.Sprintf("https://github.com/%s/pull/%d", repoFullName, number)),
		Draft:   gh.Bool(false),
		User:    &gh.User{Login: gh.String("bob")},
		Head:    &gh.PullRequestBranch{Ref: gh.String("fix-crash")},
		Base: &gh.PullRequestBranch{
			Ref:  gh.String("main"),
			Repo: &gh.Repository{FullName: gh.String(repoFullName)},
		},
	}
	if mergedAt != nil {
		pr.MergedAt = mergedAt
		pr.ClosedAt = mergedAt
	}
	return pr
}

func createInternalTestIssue(t *testing.T, db *gorm.DB, projectID string) *model.Issue {
	t.Helper()
	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: 9,
		State:          model.IssueStateOpen,
		Title:          "Internal issue",
		AuthorUserID:   "user-1",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	return issue
}

func withTestGitHubToken(t *testing.T, svc *testServices) func(*model.Project) {
	return func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	}
}

func stubPullRequestClient(t *testing.T, svc *testServices, fake *fakeIssueGitHubClient, wantToken string) {
	t.Helper()
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != wantToken {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}
}

func countPullRequestRows(t *testing.T, svc *testServices, issueID string) int64 {
	t.Helper()
	var count int64
	if err := svc.db.Model(&model.IssuePullRequest{}).Where("issue_id = ?", issueID).Count(&count).Error; err != nil {
		t.Fatalf("count pull request rows: %v", err)
	}
	return count
}

// internal 来源 Issue + 未配 GitHub 的项目：attach 以未认证客户端访问公共仓库，
// 行落 link_origin=manual 且填充服务端拉取的字段。
func TestIssueServiceAttachPullRequest_InternalIssueSucceeds(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = nil
	})
	issue := createInternalTestIssue(t, svc.db, project.ID)

	var gotOwner, gotRepo string
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "" {
			t.Fatalf("expected empty token for internal project, got %q", token)
		}
		gotOwner, gotRepo = owner, repo
		return fake
	}

	resp, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("attach pull request: %v", err)
	}

	if gotOwner != "owner" || gotRepo != "repo" {
		t.Fatalf("unexpected client repo: %s/%s", gotOwner, gotRepo)
	}
	if resp.State != model.IssuePullRequestStateOpen || resp.Title != "Fix crash #7" ||
		resp.RepoFullName != "owner/repo" || resp.Number != 7 ||
		resp.AuthorLogin != "bob" || resp.HeadRef != "fix-crash" || resp.BaseRef != "main" ||
		resp.LinkOrigin != model.IssuePullRequestLinkOriginManual || resp.Provider != "github" {
		t.Fatalf("unexpected attach payload: %+v", resp)
	}
	if resp.MergedAt != nil || resp.ClosedAt != nil {
		t.Fatalf("expected no merged_at/closed_at on open PR, got %+v", resp)
	}

	var stored model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored link: %v", err)
	}
	if stored.ID != resp.Id || stored.ProjectID != project.ID || stored.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("unexpected stored link: %+v", stored)
	}
	if stored.SyncedAt.IsZero() {
		t.Fatalf("expected synced_at to be set")
	}
}

// 重复 attach 同一 PR 幂等：不产生第二行，既有行状态顺带刷新。
func TestIssueServiceAttachPullRequest_Idempotent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	mergedAt := &gh.Timestamp{Time: time.Now().Add(-time.Hour).UTC()}
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	first, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}

	fake.pullRequests[7] = testPullRequest("owner/repo", 7, "closed", mergedAt)
	second, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}

	if second.Id != first.Id {
		t.Fatalf("expected same link row on re-attach, got %q vs %q", second.Id, first.Id)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 1 {
		t.Fatalf("expected exactly one link row, got %d", count)
	}
	if second.State != model.IssuePullRequestStateMerged || second.MergedAt == nil {
		t.Fatalf("expected refreshed merged state, got %+v", second)
	}
}

// attach 的 PR URL 可以是任意 GitHub 仓库，不要求等于项目配置的仓库。
func TestIssueServiceAttachPullRequest_CrossRepoURL(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	issue := createTestIssue(t, svc.db, project.ID)

	var gotOwner, gotRepo string
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{42: testPullRequest("other-org/other-repo", 42, "open", nil)},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("expected decrypted project token, got %q", token)
		}
		gotOwner, gotRepo = owner, repo
		return fake
	}

	resp, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/other-org/other-repo/pull/42",
	})
	if err != nil {
		t.Fatalf("attach cross-repo pull request: %v", err)
	}
	if gotOwner != "other-org" || gotRepo != "other-repo" {
		t.Fatalf("expected client for other-org/other-repo, got %s/%s", gotOwner, gotRepo)
	}
	if resp.RepoFullName != "other-org/other-repo" || resp.Number != 42 {
		t.Fatalf("unexpected stored repo: %+v", resp)
	}
}

func TestIssueServiceAttachPullRequest_RejectsBadURL(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		t.Fatalf("github client must not be built for unparseable URL")
		return nil
	}

	badURLs := []string{
		"",
		"not-a-url",
		"https://gitlab.com/owner/repo/pull/7",
		"https://github.com/owner/repo/issues/7",
		"https://github.com/owner/repo/pull/",
		"https://github.com/owner/repo/pull/abc",
		"https://github.com/owner/repo/pull/0",
		"ftp://github.com/owner/repo/pull/7",
		// 点段会被 HTTP 客户端路径归一化，导致请求落到非预期路径
		"https://github.com/./repo/pull/7",
		"https://github.com/../repo/pull/7",
		"https://github.com/owner/../pull/7",
		"https://github.com/owner/./pull/7",
		"https://github.com/.owner/repo/pull/7",
		"https://github.com/owner./repo/pull/7",
		"https://github.com/owner/repo./pull/7",
	}
	for _, raw := range badURLs {
		_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{Url: raw})
		if err == nil {
			t.Fatalf("expected error for url %q, got nil", raw)
		}
		appErr, ok := err.(*errs.AppError)
		if !ok || appErr.Code != errs.ErrInvalidParams.Code {
			t.Fatalf("expected 40001 for url %q, got %v", raw, err)
		}
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}
}

// 尾部 /files、query、fragment 与 http、www 前缀仍应识别为同一 PR。
func TestIssueServiceAttachPullRequest_AcceptsURLVariants(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	for _, raw := range []string{
		"https://github.com/owner/repo/pull/7",
		"https://github.com/owner/repo/pull/7/files",
		"https://github.com/owner/repo/pull/7?diff=split",
		"http://www.github.com/owner/repo/pull/7#discussion_r1",
		" https://github.com/owner/repo/pull/7/ ",
	} {
		if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{Url: raw}); err != nil {
			t.Fatalf("expected url %q to attach, got %v", raw, err)
		}
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 1 {
		t.Fatalf("expected variants to hit the same unique row, got %d rows", count)
	}
}

func TestIssueServiceAttachPullRequest_GitHubErrorLeavesNoRow(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{pullRequestErr: errors.New("github down")}
	stubPullRequestClient(t, svc, fake, "gh-token")

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err == nil {
		t.Fatal("expected github api error, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected 50200, got %v", err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows after failed fetch, got %d", count)
	}
}

func TestIssueServiceDetachPullRequest(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	resp, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := svc.issueService.DetachIssuePullRequest(issue.ID, user.ID, resp.Id); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected link removed, got %d rows", count)
	}

	// 再删一次：关联已不存在
	if err := svc.issueService.DetachIssuePullRequest(issue.ID, user.ID, resp.Id); err == nil {
		t.Fatal("expected not found on second detach")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrPullRequestNotFound.Code {
		t.Fatalf("expected 40412, got %v", err)
	}

	// 别的 issue 名下的关联也不可见
	otherIssue := createInternalTestIssue(t, svc.db, project.ID)
	if err := svc.db.Create(&model.IssuePullRequest{
		ID:           "link-other",
		IssueID:      otherIssue.ID,
		ProjectID:    project.ID,
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo",
		Number:       9,
		State:        model.IssuePullRequestStateOpen,
		LinkOrigin:   model.IssuePullRequestLinkOriginManual,
		SyncedAt:     time.Now().UTC(),
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed other link: %v", err)
	}
	if err := svc.issueService.DetachIssuePullRequest(issue.ID, user.ID, "link-other"); err == nil {
		t.Fatal("expected not found for cross-issue link")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrPullRequestNotFound.Code {
		t.Fatalf("expected 40412 for cross-issue link, got %v", err)
	}
	if count := countPullRequestRows(t, svc, otherIssue.ID); count != 1 {
		t.Fatalf("expected other issue link untouched, got %d rows", count)
	}
}

// sync 把 open 刷成 merged：GitHub closed + merged_at 非空 → state=merged 且 merged_at 落库。
func TestIssueServiceSyncPullRequests_RefreshesToMerged(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	firstSyncedAt := time.Now().UTC()

	mergedAt := &gh.Timestamp{Time: time.Now().Add(-time.Hour).UTC()}
	fake.pullRequests[7] = testPullRequest("owner/repo", 7, "closed", mergedAt)

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Items) != 1 || len(result.Failures) != 0 {
		t.Fatalf("expected one refreshed link and no failures, got %+v", result)
	}
	if result.Items[0].State != model.IssuePullRequestStateMerged {
		t.Fatalf("expected merged state, got %+v", result.Items[0])
	}
	if result.Items[0].MergedAt == nil || result.Items[0].ClosedAt == nil {
		t.Fatalf("expected merged_at/closed_at set, got %+v", result.Items[0])
	}

	var stored model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload link: %v", err)
	}
	if stored.State != model.IssuePullRequestStateMerged || stored.MergedAt == nil {
		t.Fatalf("expected merged persisted, got %+v", stored)
	}
	if !stored.SyncedAt.After(firstSyncedAt) {
		t.Fatalf("expected synced_at to advance, got %v vs %v", stored.SyncedAt, firstSyncedAt)
	}
}

// 单行失败是独立失败域：记入 failures 不中断，失败行旧数据保留，整体仍返回结果。
func TestIssueServiceSyncPullRequests_PartialFailureRecorded(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	staleSyncedAt := now.Add(-time.Hour)
	for _, link := range []*model.IssuePullRequest{
		{ID: "link-ok", IssueID: issue.ID, ProjectID: project.ID, Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo", Number: 7, Title: "ok pr", State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: staleSyncedAt, CreatedAt: staleSyncedAt, UpdatedAt: staleSyncedAt},
		{ID: "link-gone", IssueID: issue.ID, ProjectID: project.ID, Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "gone/repo", Number: 9, Title: "stale pr", State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: staleSyncedAt, CreatedAt: staleSyncedAt, UpdatedAt: staleSyncedAt},
	} {
		if err := svc.db.Create(link).Error; err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	clients := map[string]*fakeIssueGitHubClient{
		"owner/repo": {pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)}},
		"gone/repo":  {pullRequestErr: errors.New("404 not found")},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		fake, ok := clients[owner+"/"+repo]
		if !ok {
			t.Fatalf("unexpected client repo: %s/%s", owner, repo)
		}
		return fake
	}

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync should not return error on per-row failure, got %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Id != "link-ok" {
		t.Fatalf("expected link-ok in items, got %+v", result.Items)
	}
	if len(result.Failures) != 1 || result.Failures[0].Id != "link-gone" {
		t.Fatalf("expected link-gone in failures, got %+v", result.Failures)
	}
	if !strings.Contains(result.Failures[0].Error, "gone/repo#9") {
		t.Fatalf("expected failure error to carry repo#number, got %q", result.Failures[0].Error)
	}

	// 失败行保留旧数据
	var stored model.IssuePullRequest
	if err := svc.db.First(&stored, "id = ?", "link-gone").Error; err != nil {
		t.Fatalf("reload failed link: %v", err)
	}
	if stored.Title != "stale pr" || !stored.SyncedAt.Equal(staleSyncedAt) {
		t.Fatalf("expected failed row untouched, got %+v", stored)
	}
	// 成功行被刷新
	var okStored model.IssuePullRequest
	if err := svc.db.First(&okStored, "id = ?", "link-ok").Error; err != nil {
		t.Fatalf("reload ok link: %v", err)
	}
	if !okStored.SyncedAt.After(staleSyncedAt) {
		t.Fatalf("expected ok row synced_at advanced, got %v", okStored.SyncedAt)
	}
}

// 全部行失败也返回正常结果：items 为空，failures 逐行记录。
func TestIssueServiceSyncPullRequests_AllFailStillReturns(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	for i, id := range []string{"link-a", "link-b"} {
		link := &model.IssuePullRequest{
			ID: id, IssueID: issue.ID, ProjectID: project.ID,
			Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo",
			Number: 10 + i, State: model.IssuePullRequestStateOpen,
			LinkOrigin: model.IssuePullRequestLinkOriginManual,
			SyncedAt:   now, CreatedAt: now, UpdatedAt: now,
		}
		if err := svc.db.Create(link).Error; err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	fake := &fakeIssueGitHubClient{pullRequestErr: errors.New("github down")}
	stubPullRequestClient(t, svc, fake, "gh-token")

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync should not return error, got %v", err)
	}
	if len(result.Items) != 0 || len(result.Failures) != 2 {
		t.Fatalf("expected 0 items and 2 failures, got %+v", result)
	}
}

// 仓库改名：旧行 sync 归一化后撞上既有行唯一键 → 该行记 failure，不删不合并。
func TestIssueServiceSyncPullRequests_RenamedRepoCollisionRecorded(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	// link-old：仓库旧名关联的 PR#7；link-new：改名后新名重新 attach 的同一 PR
	for _, link := range []*model.IssuePullRequest{
		{ID: "link-old", IssueID: issue.ID, ProjectID: project.ID, Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "old-org/repo", Number: 7, Title: "old", State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "link-new", IssueID: issue.ID, ProjectID: project.ID, Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "new-org/repo", Number: 7, Title: "new", State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
	} {
		if err := svc.db.Create(link).Error; err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	clients := map[string]*fakeIssueGitHubClient{
		// 旧名 repo 的 PR 返回新 canonical 名 new-org/repo → 与 link-new 撞唯一键
		"old-org/repo": {pullRequests: map[int]*gh.PullRequest{7: testPullRequest("new-org/repo", 7, "open", nil)}},
		"new-org/repo": {pullRequests: map[int]*gh.PullRequest{7: testPullRequest("new-org/repo", 7, "open", nil)}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		fake, ok := clients[owner+"/"+repo]
		if !ok {
			t.Fatalf("unexpected client repo: %s/%s", owner, repo)
		}
		return fake
	}

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync should not return error, got %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Id != "link-new" {
		t.Fatalf("expected link-new refreshed, got %+v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].Id != "link-old" {
		t.Fatalf("expected link-old failure, got %+v", result.Failures)
	}
	if !strings.Contains(result.Failures[0].Error, "冲突") && !strings.Contains(result.Failures[0].Error, "detach") {
		t.Fatalf("expected conflict hint in failure error, got %q", result.Failures[0].Error)
	}

	// 旧行保留，没有被删除或合并
	var stored model.IssuePullRequest
	if err := svc.db.First(&stored, "id = ?", "link-old").Error; err != nil {
		t.Fatalf("reload link-old: %v", err)
	}
	if stored.RepoFullName != "old-org/repo" {
		t.Fatalf("expected stale row kept with old name, got %+v", stored)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 2 {
		t.Fatalf("expected both rows to remain, got %d", count)
	}
}

// 列表项带聚合计数：一次 GROUP BY，无关联的 Issue 计数为 0。
func TestIssueServiceList_IncludesPullRequestSummary(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issueWith := createTestIssue(t, svc.db, project.ID)
	issueWithout := createInternalTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	seed := []model.IssuePullRequest{
		{ID: "l1", IssueID: issueWith.ID, ProjectID: project.ID, Provider: "github", RepoFullName: "owner/repo", Number: 1, State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "l2", IssueID: issueWith.ID, ProjectID: project.ID, Provider: "github", RepoFullName: "owner/repo", Number: 2, State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "l3", IssueID: issueWith.ID, ProjectID: project.ID, Provider: "github", RepoFullName: "owner/repo", Number: 3, State: model.IssuePullRequestStateMerged, LinkOrigin: model.IssuePullRequestLinkOriginManual, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "l4", IssueID: issueWith.ID, ProjectID: project.ID, Provider: "github", RepoFullName: "owner/repo", Number: 4, State: model.IssuePullRequestStateClosed, LinkOrigin: model.IssuePullRequestLinkOriginSynced, SyncedAt: now, CreatedAt: now, UpdatedAt: now},
	}
	for i := range seed {
		if err := svc.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 issues, got total=%d len=%d", total, len(items))
	}

	byID := map[string]*IssuePullRequestSummaryResponse{}
	for _, item := range items {
		byID[item.Id] = item.PullRequestSummary
	}
	withSummary := byID[issueWith.ID]
	if withSummary == nil {
		t.Fatalf("expected pull_request_summary on list item")
	}
	if withSummary.Total != 4 || withSummary.Open != 2 || withSummary.Merged != 1 {
		t.Fatalf("unexpected summary: %+v", withSummary)
	}
	withoutSummary := byID[issueWithout.ID]
	if withoutSummary == nil || withoutSummary.Total != 0 || withoutSummary.Open != 0 || withoutSummary.Merged != 0 {
		t.Fatalf("expected zero summary for unlinked issue, got %+v", withoutSummary)
	}

	// 列表不塞完整数组
	for _, item := range items {
		if item.PullRequests != nil {
			t.Fatalf("expected no pull_requests array on list item, got %+v", item.PullRequests)
		}
	}
}

// 详情填充的数组来自同一存储；字段逐项校验。
func TestIssueServiceListIssuePullRequests_ReturnsRows(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	items, err := svc.issueService.ListIssuePullRequests(issue.ID)
	if err != nil {
		t.Fatalf("list pull requests: %v", err)
	}
	if len(items) != 1 || items[0].Number != 7 || items[0].RepoFullName != "owner/repo" {
		t.Fatalf("unexpected pull request list: %+v", items)
	}
}

// repo_full_name 归一到 GitHub 返回的 canonical 名：大小写变体 URL 落到同一行。
func TestIssueServiceAttachPullRequest_CaseVariantURLSameRow(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	first, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/Owner/Repo/pull/7",
	})
	if err != nil {
		t.Fatalf("attach case-variant url: %v", err)
	}
	second, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("attach canonical url: %v", err)
	}

	if first.Id != second.Id {
		t.Fatalf("expected same link row, got %q vs %q", first.Id, second.Id)
	}
	if first.RepoFullName != "owner/repo" {
		t.Fatalf("expected canonical repo name, got %q", first.RepoFullName)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 1 {
		t.Fatalf("expected one row for case variants, got %d", count)
	}
}

// GitHub closed 但 merged_at 为空 → state=closed 且 merged_at 为空。
func TestIssueServiceAttachPullRequest_ClosedNotMerged(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	closedAt := &gh.Timestamp{Time: time.Now().Add(-time.Hour).UTC()}
	pr := testPullRequest("owner/repo", 7, "closed", nil)
	pr.ClosedAt = closedAt
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: pr},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	resp, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if resp.State != model.IssuePullRequestStateClosed {
		t.Fatalf("expected closed state, got %+v", resp)
	}
	if resp.MergedAt != nil {
		t.Fatalf("expected merged_at empty for unmerged close, got %+v", resp.MergedAt)
	}
	if resp.ClosedAt == nil {
		t.Fatalf("expected closed_at set, got %+v", resp)
	}
}

// merged 的 PR 被 reopen（open）后 sync：merged_at/closed_at 一并清空。
func TestIssueServiceSyncPullRequests_ReopenClearsMergeFields(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	mergedAt := &gh.Timestamp{Time: time.Now().Add(-time.Hour).UTC()}
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "closed", mergedAt)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	}); err != nil {
		t.Fatalf("attach merged pr: %v", err)
	}

	fake.pullRequests[7] = testPullRequest("owner/repo", 7, "open", nil)
	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].State != model.IssuePullRequestStateOpen {
		t.Fatalf("expected reopened open state, got %+v", result)
	}
	if result.Items[0].MergedAt != nil || result.Items[0].ClosedAt != nil {
		t.Fatalf("expected merged_at/closed_at cleared, got %+v", result.Items[0])
	}
}

// attach 与 sync 都不动 Issue 的 workflow_status。
func TestIssueServicePullRequests_DoNotTouchWorkflowStatus(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	if err := svc.db.Create(&model.IssueInternalMeta{
		IssueID:        issue.ID,
		WorkflowStatus: model.IssueWorkflowStatusDone,
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatalf("seed internal meta: %v", err)
	}

	mergedAt := &gh.Timestamp{Time: now.Add(-time.Hour)}
	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "closed", mergedAt)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var meta model.IssueInternalMeta
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&meta).Error; err != nil {
		t.Fatalf("reload internal meta: %v", err)
	}
	if meta.WorkflowStatus != model.IssueWorkflowStatusDone {
		t.Fatalf("workflow_status mutated: %q", meta.WorkflowStatus)
	}

	var storedIssue model.Issue
	if err := svc.db.First(&storedIssue, "id = ?", issue.ID).Error; err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if storedIssue.State != model.IssueStateOpen {
		t.Fatalf("issue state mutated: %q", storedIssue.State)
	}
}

// 别的用户的 Issue：attach 不可见，返回项目不存在。
func TestIssueServiceAttachPullRequest_CrossUserRejected(t *testing.T) {
	svc := setupTestServices(t)
	owner := createTestUser(t, svc.db, "user-1")
	stranger := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, owner.ID, withTestGitHubToken(t, svc))
	issue := createTestIssue(t, svc.db, project.ID)

	fake := &fakeIssueGitHubClient{
		pullRequests: map[int]*gh.PullRequest{7: testPullRequest("owner/repo", 7, "open", nil)},
	}
	stubPullRequestClient(t, svc, fake, "gh-token")

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, stranger.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	if err == nil {
		t.Fatal("expected error for foreign issue, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrProjectNotFound.Code {
		t.Fatalf("expected 40401, got %v", err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}
}
