package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

var handlerTestPNGBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
}

func TestIssueHandlerCreate_CreatesInternalIssue(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	body := []byte(`{"title":"补充发布检查","body":"## 步骤","workflow_status":"todo"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", body)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.Create(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Source       string `json:"source"`
		Reference    string `json:"reference"`
		Title        string `json:"title"`
		ProjectID    string `json:"project_id"`
		InternalMeta struct {
			WorkflowStatus string `json:"workflow_status"`
		} `json:"internal_meta"`
	}
	decodeEnvelope(t, rec, &result)

	if result.Source != string(model.IssueSourceInternal) || result.Reference != "INT-1" {
		t.Fatalf("unexpected created issue payload: %+v", result)
	}
	if result.Title != "补充发布检查" || result.ProjectID != project.ID {
		t.Fatalf("unexpected created issue payload: %+v", result)
	}
	if result.InternalMeta.WorkflowStatus != string(model.IssueWorkflowStatusTodo) {
		t.Fatalf("expected todo workflow status, got %+v", result)
	}
}

func TestIssueHandlerUpdate_UpdatesInternalIssue(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	createBody := []byte(`{"title":"补充发布检查","body":"old body"}`)
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", createBody)
	createCtx.Params = ginParams("id", project.ID)
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create issue failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, createRec, &created)

	body := []byte(`{"title":"更新后的标题","body":"new body","state":"closed"}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+created.ID, body)
	ctx.Params = ginParams("iid", created.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.Update(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Title    string  `json:"title"`
		State    string  `json:"state"`
		ClosedAt *string `json:"closed_at"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Title != "更新后的标题" || result.State != string(model.IssueStateClosed) || result.ClosedAt == nil {
		t.Fatalf("unexpected update payload: %+v", result)
	}
}

func TestIssueHandlerCreateComment_CreatesInternalComment(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	createBody := []byte(`{"title":"补充发布检查","body":"old body"}`)
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", createBody)
	createCtx.Params = ginParams("id", project.ID)
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create issue failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, createRec, &created)

	body := []byte(`{"body":"第一条内部评论"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/issues/"+created.ID+"/comments", body)
	ctx.Params = ginParams("iid", created.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.CreateComment(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Source string `json:"source"`
		Body   string `json:"body"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Source != string(model.IssueSourceInternal) || result.Body != "第一条内部评论" {
		t.Fatalf("unexpected comment payload: %+v", result)
	}
}

func TestIssueHandlerUploadAssetAndReadContent(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	createBody := []byte(`{"title":"补充发布检查","body":"old body"}`)
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", createBody)
	createCtx.Params = ginParams("id", project.ID)
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create issue failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, createRec, &created)

	req, _ := newMultipartUploadRequest(
		t,
		"/api/issues/"+created.ID+"/assets",
		"file",
		"clip.png",
		handlerTestPNGBytes,
		nil,
	)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("iid", created.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.UploadAsset(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var uploaded struct {
		ID         string `json:"id"`
		MimeType   string `json:"mime_type"`
		ContentURL string `json:"content_url"`
		Markdown   string `json:"markdown"`
	}
	decodeEnvelope(t, rec, &uploaded)
	if uploaded.ID == "" || uploaded.ContentURL == "" || uploaded.Markdown == "" {
		t.Fatalf("unexpected upload payload: %+v", uploaded)
	}

	contentRec := httptest.NewRecorder()
	contentCtx, _ := gin.CreateTestContext(contentRec)
	contentCtx.Request = httptest.NewRequest(http.MethodGet, uploaded.ContentURL, nil)
	contentCtx.Params = ginParams("aid", uploaded.ID)
	contentCtx.Set(middleware.ContextKeyUserID, user.ID)
	contentCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.AssetContent(contentCtx)

	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected content 200, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	if contentRec.Header().Get("Content-Type") != uploaded.MimeType {
		t.Fatalf("expected content type %q, got %q", uploaded.MimeType, contentRec.Header().Get("Content-Type"))
	}
	if body := contentRec.Body.Bytes(); string(body) != string(handlerTestPNGBytes) {
		t.Fatalf("unexpected asset body length: %d", len(body))
	}
}

func TestIssueHandlerUpdateInternalMeta_UpdatesWorkflowStatus(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	body := []byte(`{"workflow_status":"done"}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/internal-meta", body)
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.UpdateInternalMeta(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		WorkflowStatus string  `json:"workflow_status"`
		StartedAt      *string `json:"started_at"`
		CompletedAt    *string `json:"completed_at"`
	}
	decodeEnvelope(t, rec, &result)
	if result.WorkflowStatus != string(model.IssueWorkflowStatusDone) {
		t.Fatalf("expected done workflow status, got %+v", result)
	}
	if result.StartedAt == nil || result.CompletedAt == nil {
		t.Fatalf("expected timestamps to be populated, got %+v", result)
	}

	var stored model.IssueInternalMeta
	if err := env.db.Where("issue_id = ?", issue.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored internal meta: %v", err)
	}
	if stored.WorkflowStatus != model.IssueWorkflowStatusDone {
		t.Fatalf("unexpected stored workflow status: %q", stored.WorkflowStatus)
	}
}

func TestIssueHandlerUpdateInternalMeta_RejectsMissingWorkflowStatus(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/internal-meta", []byte(`{}`))
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)

	env.issueHandler.UpdateInternalMeta(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
