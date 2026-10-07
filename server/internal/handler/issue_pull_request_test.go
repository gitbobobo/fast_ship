package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
)

func asTestUser(c *gin.Context, userID string) {
	c.Set(middleware.ContextKeyUserID, userID)
	c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
}

func pullRequestParams(issueID, linkID string) gin.Params {
	params := gin.Params{{Key: "iid", Value: issueID}}
	if linkID != "" {
		params = append(params, gin.Param{Key: "id", Value: linkID})
	}
	return params
}

// 非 GitHub PR URL 返回 400。
func TestIssueHandlerAttachPullRequest_BadURLReturns400(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/pull-requests",
		[]byte(`{"url":"https://gitlab.com/owner/repo/merge_requests/7"}`))
	ctx.Params = pullRequestParams(issue.ID, "")
	asTestUser(ctx, user.ID)

	env.issueHandler.AttachPullRequest(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var env2 struct {
		Code int `json:"code"`
	}
	envelope := decodeEnvelope(t, rec, &env2)
	if envelope.Code != 40001 {
		t.Fatalf("expected error code 40001, got %d", envelope.Code)
	}
}

// 请求体缺 url 字段同样 400。
func TestIssueHandlerAttachPullRequest_MissingURLReturns400(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/pull-requests", []byte(`{}`))
	ctx.Params = pullRequestParams(issue.ID, "")
	asTestUser(ctx, user.ID)

	env.issueHandler.AttachPullRequest(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// detach 不存在的关联返回 404。
func TestIssueHandlerDetachPullRequest_NotFoundReturns404(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodDelete, "/api/issues/"+issue.ID+"/pull-requests/missing", nil)
	ctx.Params = pullRequestParams(issue.ID, "missing")
	asTestUser(ctx, user.ID)

	env.issueHandler.DetachPullRequest(ctx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	envelope := decodeEnvelope(t, rec, nil)
	if envelope.Code != 40412 {
		t.Fatalf("expected error code 40412, got %d", envelope.Code)
	}
}

// Issue 详情响应携带完整 pull_requests 数组。
func TestIssueHandlerGet_IncludesPullRequestsArray(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	now := time.Now().UTC()
	mergedAt := now.Add(-time.Hour)
	link := &model.IssuePullRequest{
		ID:           "link-1",
		IssueID:      issue.ID,
		ProjectID:    project.ID,
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "other-org/other-repo",
		Number:       42,
		HTMLURL:      "https://github.com/other-org/other-repo/pull/42",
		Title:        "Fix crash",
		State:        model.IssuePullRequestStateMerged,
		AuthorLogin:  "bob",
		HeadRef:      "fix-crash",
		BaseRef:      "main",
		MergedAt:     &mergedAt,
		ClosedAt:     &mergedAt,
		LinkOrigin:   model.IssuePullRequestLinkOriginManual,
		SyncedAt:     now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := env.db.Create(link).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}

	ctx, rec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID, nil)
	ctx.Params = pullRequestParams(issue.ID, "")
	asTestUser(ctx, user.ID)

	env.issueHandler.Get(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var detail struct {
		PullRequests []service.IssuePullRequestResponse `json:"pull_requests"`
	}
	decodeEnvelope(t, rec, &detail)
	if len(detail.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", detail.PullRequests)
	}
	got := detail.PullRequests[0]
	if got.Id != "link-1" || got.RepoFullName != "other-org/other-repo" || got.Number != 42 ||
		got.State != model.IssuePullRequestStateMerged || got.LinkOrigin != model.IssuePullRequestLinkOriginManual ||
		got.MergedAt == nil {
		t.Fatalf("unexpected pull request payload: %+v", got)
	}
}

// 无关联的 Issue 详情不出现 pull_requests 字段（omitempty 缺省）。
func TestIssueHandlerGet_WithoutLinksOmitsPullRequests(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID, nil)
	ctx.Params = pullRequestParams(issue.ID, "")
	asTestUser(ctx, user.ID)

	env.issueHandler.Get(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]any
	decodeEnvelope(t, rec, &raw)
	if _, ok := raw["pull_requests"]; ok {
		t.Fatalf("expected pull_requests omitted, got %s", rec.Body.String())
	}
}
