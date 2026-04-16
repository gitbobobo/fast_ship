package handler

import (
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

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
