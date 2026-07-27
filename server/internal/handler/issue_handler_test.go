package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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

func TestIssueHandlerList_FiltersBySource(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	createBody := []byte(`{"title":"补充发布检查","body":"仅内部跟进"}`)
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", createBody)
	createCtx.Params = ginParams("id", project.ID)
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create internal issue failed: %d %s", createRec.Code, createRec.Body.String())
	}

	createHandlerTestIssue(t, env.db, project.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
	})

	ctx, rec := newJSONContext(http.MethodGet, "/api/projects/"+project.ID+"/issues?source=internal", nil)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.List(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Items []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			Title  string `json:"title"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decodeEnvelope(t, rec, &result)

	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("expected one filtered issue, got %+v", result)
	}
	if result.Items[0].Source != string(model.IssueSourceInternal) || result.Items[0].Title != "补充发布检查" {
		t.Fatalf("unexpected filtered issue payload: %+v", result.Items[0])
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

func TestIssueHandlerReplaceChecklist_UpdatesProgress(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	body := []byte(`{"items":[{"title":"梳理复现路径","is_completed":true},{"title":"修复崩溃","is_completed":false}]}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/checklist", body)
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.ReplaceChecklist(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		WorkflowStatus  string `json:"workflow_status"`
		ProgressPercent *int   `json:"progress_percent"`
		ChecklistTotal  int    `json:"checklist_total"`
		ChecklistDone   int    `json:"checklist_done"`
		Checklist       []struct {
			Title       string `json:"title"`
			IsCompleted bool   `json:"is_completed"`
		} `json:"checklist"`
	}
	decodeEnvelope(t, rec, &result)
	if result.ProgressPercent == nil || *result.ProgressPercent != 50 {
		t.Fatalf("expected progress 50, got %+v", result)
	}
	if result.WorkflowStatus != "" {
		t.Fatalf("expected empty workflow status (checklist should not auto-set it), got %+v", result)
	}
	if result.ChecklistTotal != 2 || result.ChecklistDone != 1 || len(result.Checklist) != 2 {
		t.Fatalf("unexpected checklist payload: %+v", result)
	}
}

func TestIssueHandlerCreate_WithApiKey(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)

	body := []byte(`{"title":"API Key 创建的问题","body":"通过 API Key 创建","workflow_status":"todo"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", body)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	ctx.Set(middleware.ContextKeyAPIKey, "ci-key")

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
	if result.Title != "API Key 创建的问题" || result.ProjectID != project.ID {
		t.Fatalf("unexpected created issue payload: %+v", result)
	}
	if result.InternalMeta.WorkflowStatus != string(model.IssueWorkflowStatusTodo) {
		t.Fatalf("expected todo workflow status, got %+v", result)
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsStateChange(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{"title":"API Key 更新后的标题","body":"new body","state":"closed"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	envelope := decodeEnvelope(t, updateRec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}

	var stored model.Issue
	if err := env.db.Where("id = ?", issueID).First(&stored).Error; err != nil {
		t.Fatalf("load stored issue: %v", err)
	}
	if stored.Title != "原始标题" || stored.State != model.IssueStateOpen {
		t.Fatalf("issue should remain unchanged after forbidden update: %+v", stored)
	}
}

func TestIssueHandlerUpdate_WithApiKey_AllowsTitleAndBody(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{"title":"API Key 更新后的标题","body":"new body"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updateRec.Code, updateRec.Body.String())
	}

	var result struct {
		Title string `json:"title"`
		State string `json:"state"`
	}
	decodeEnvelope(t, updateRec, &result)
	if result.Title != "API Key 更新后的标题" || result.State != string(model.IssueStateOpen) {
		t.Fatalf("unexpected update payload: %+v", result)
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsWhenStateMixedWithAllowedFields(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{"title":"被拒应不入库","state":"closed"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	envelope := decodeEnvelope(t, updateRec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}

	var stored model.Issue
	if err := env.db.Where("id = ?", issueID).First(&stored).Error; err != nil {
		t.Fatalf("load stored issue: %v", err)
	}
	if stored.Title != "原始标题" {
		t.Fatalf("title must not be partially applied, got %q", stored.Title)
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsStateReasonOnly(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{"state_reason":"completed"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	envelope := decodeEnvelope(t, updateRec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}

	var stored model.Issue
	if err := env.db.Where("id = ?", issueID).First(&stored).Error; err != nil {
		t.Fatalf("load stored issue: %v", err)
	}
	if stored.StateReason != "" {
		t.Fatalf("state_reason must not be written, got %q", stored.StateReason)
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsStateOpenOnly(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{"state":"open"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	envelope := decodeEnvelope(t, updateRec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsEmptyPayload(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "原始标题")

	updateCtx, updateRec := apiKeyUpdateContext(t, issueID, user.ID, `{}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
}

func TestIssueHandlerUpdate_WithApiKey_RejectsStateChangeOnGithubSource(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey-gh-update")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	updateCtx, updateRec := apiKeyUpdateContext(t, issue.ID, user.ID, `{"state":"closed"}`)
	env.issueHandler.Update(updateCtx)

	if updateRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	envelope := decodeEnvelope(t, updateRec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}
}

func createInternalIssueViaAPIKey(t *testing.T, env *handlerTestEnv, userID, projectID, title string) string {
	t.Helper()

	createBody, _ := json.Marshal(map[string]string{
		"title": title,
		"body":  "old body",
	})
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+projectID+"/issues", createBody)
	createCtx.Params = ginParams("id", projectID)
	createCtx.Set(middleware.ContextKeyUserID, userID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	createCtx.Set(middleware.ContextKeyAPIKey, "ci-key")
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create issue failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, createRec, &created)
	return created.ID
}

func apiKeyUpdateContext(t *testing.T, issueID, userID, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	updateCtx, updateRec := newJSONContext(http.MethodPut, "/api/issues/"+issueID, []byte(body))
	updateCtx.Params = ginParams("iid", issueID)
	updateCtx.Set(middleware.ContextKeyUserID, userID)
	updateCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	updateCtx.Set(middleware.ContextKeyAPIKey, "ci-key")
	return updateCtx, updateRec
}

func TestIssueHandlerCreate_WithApiKey_RejectsGitHubSource(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-apikey-gh")
	project := createHandlerTestProject(t, env.db, user.ID)

	body := []byte(`{"title":"API Key 尝试创建 GitHub Issue","body":"test","source":"github"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", body)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	ctx.Set(middleware.ContextKeyAPIKey, "ci-key")

	env.issueHandler.Create(ctx)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	envelope := decodeEnvelope(t, rec, nil)
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}
}

func createHandlerTestInternalIssueDone(t *testing.T, env *handlerTestEnv, projectID, userID string, title string) string {
	t.Helper()

	createBody, err := json.Marshal(map[string]string{
		"title":           title,
		"body":            "done issue",
		"workflow_status": "done",
	})
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+projectID+"/issues", createBody)
	createCtx.Params = ginParams("id", projectID)
	createCtx.Set(middleware.ContextKeyUserID, userID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create internal issue failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, createRec, &created)
	return created.ID
}

func TestIssueHandlerBatchCloseDone_ClosesMatchingInternalIssues(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-close")
	project := createHandlerTestProject(t, env.db, user.ID)

	createHandlerTestInternalIssueDone(t, env, project.ID, user.ID, "done issue 1")
	createHandlerTestInternalIssueDone(t, env, project.ID, user.ID, "done issue 2")

	createBody := []byte(`{"title":"todo issue","body":"not done"}`)
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues", createBody)
	createCtx.Params = ginParams("id", project.ID)
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.Create(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create todo issue failed: %d %s", createRec.Code, createRec.Body.String())
	}

	body := []byte(`{"source":"internal"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues/batch-close", body)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.BatchCloseDone(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Total     int64 `json:"total"`
		Succeeded int   `json:"succeeded"`
		Failed    int   `json:"failed"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 {
		t.Fatalf("unexpected batch close result: %+v", result)
	}

	listCtx, listRec := newJSONContext(http.MethodGet, "/api/projects/"+project.ID+"/issues?state=open&workflow_status=done&source=internal", nil)
	listCtx.Params = ginParams("id", project.ID)
	listCtx.Set(middleware.ContextKeyUserID, user.ID)
	listCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.List(listCtx)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", listRec.Code, listRec.Body.String())
	}
	var listResult struct {
		Total int64 `json:"total"`
	}
	decodeEnvelope(t, listRec, &listResult)
	if listResult.Total != 0 {
		t.Fatalf("expected no open done internal issues, got total=%d", listResult.Total)
	}
}

func TestIssueHandlerBatchCloseDone_EmptyResult(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-empty")
	project := createHandlerTestProject(t, env.db, user.ID)

	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues/batch-close", nil)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.BatchCloseDone(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Total int64 `json:"total"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Total != 0 {
		t.Fatalf("expected total 0, got %d", result.Total)
	}
}

func TestIssueHandlerBatchCloseDone_RejectsInvalidSource(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-invalid")
	project := createHandlerTestProject(t, env.db, user.ID)

	body := []byte(`{"source":"invalid"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues/batch-close", body)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.BatchCloseDone(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssueHandlerCount_ReturnsFilteredCount(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-count")
	project := createHandlerTestProject(t, env.db, user.ID)

	createHandlerTestInternalIssueDone(t, env, project.ID, user.ID, "count me")

	ctx, rec := newJSONContext(http.MethodGet, "/api/projects/"+project.ID+"/issues/count?state=open&workflow_status=done&source=internal", nil)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.Count(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Count int64 `json:"count"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Count != 1 {
		t.Fatalf("expected count 1, got %d", result.Count)
	}
}
