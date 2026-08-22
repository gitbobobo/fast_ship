package handler

import (
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestIssueHandlerUpsertShipHook_WithJWT(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	body := []byte(`{"comment_body":"已随 {version} 发出。","close":true,"workflow_status":"done"}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/ship-hook", body)
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.issueHandler.UpsertShipHook(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Status          string `json:"status"`
		CommentEnabled  bool   `json:"comment_enabled"`
		CommentBody     string `json:"comment_body"`
		CloseEnabled    bool   `json:"close_enabled"`
		WorkflowEnabled bool   `json:"workflow_enabled"`
		WorkflowStatus  string `json:"workflow_status"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Status != "pending" || !result.CommentEnabled || result.CommentBody == "" ||
		!result.CloseEnabled || !result.WorkflowEnabled || result.WorkflowStatus != "done" {
		t.Fatalf("unexpected ship hook payload: %+v", result)
	}
}

func TestIssueHandlerDeleteShipHook_WithJWT(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	putBody := []byte(`{"workflow_status":"done"}`)
	putCtx, putRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/ship-hook", putBody)
	putCtx.Params = ginParams("iid", issue.ID)
	putCtx.Set(middleware.ContextKeyUserID, user.ID)
	putCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.UpsertShipHook(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("upsert failed: %d %s", putRec.Code, putRec.Body.String())
	}

	ctx, rec := newJSONContext(http.MethodDelete, "/api/issues/"+issue.ID+"/ship-hook", nil)
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.DeleteShipHook(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssueHandlerGet_WithAPIKey_IncludesShipHook(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	putBody := []byte(`{"workflow_status":"todo"}`)
	putCtx, putRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/ship-hook", putBody)
	putCtx.Params = ginParams("iid", issue.ID)
	putCtx.Set(middleware.ContextKeyUserID, user.ID)
	putCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.UpsertShipHook(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("upsert failed: %d %s", putRec.Code, putRec.Body.String())
	}

	ctx, rec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID, nil)
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)

	env.issueHandler.Get(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		ShipHook *struct {
			Status          string `json:"status"`
			CommentEnabled  bool   `json:"comment_enabled"`
			CloseEnabled    bool   `json:"close_enabled"`
			WorkflowEnabled bool   `json:"workflow_enabled"`
			WorkflowStatus  string `json:"workflow_status"`
		} `json:"ship_hook"`
	}
	decodeEnvelope(t, rec, &result)
	if result.ShipHook == nil || result.ShipHook.Status != "pending" || result.ShipHook.WorkflowStatus != "todo" {
		t.Fatalf("expected ship_hook in get response, got %+v", result.ShipHook)
	}
	if !result.ShipHook.WorkflowEnabled || result.ShipHook.CommentEnabled || result.ShipHook.CloseEnabled {
		t.Fatalf("expected only workflow enabled, got %+v", result.ShipHook)
	}
}
