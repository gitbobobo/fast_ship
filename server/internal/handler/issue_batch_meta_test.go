package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

func batchMetaJSONContext(t *testing.T, userID string, items []map[string]any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("marshal batch body: %v", err)
	}
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/internal-meta", body)
	ctx.Set(middleware.ContextKeyUserID, userID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	ctx.Set(middleware.ContextKeyAPIKey, "ci-key")
	return ctx, rec
}

func TestIssueHandlerBatchUpdateInternalMeta_UpdatesWorkflowStatuses(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-meta")
	project := createHandlerTestProject(t, env.db, user.ID)
	issueAID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "A")
	issueBID := createInternalIssueViaAPIKey(t, env, user.ID, project.ID, "B")

	ctx, rec := batchMetaJSONContext(t, user.ID, []map[string]any{
		{"issue_id": issueAID, "workflow_status": "done"},
		{"issue_id": issueBID, "workflow_status": "in_progress"},
	})
	env.issueHandler.BatchUpdateInternalMeta(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Total     int64 `json:"total"`
		Succeeded int   `json:"succeeded"`
		Failed    int   `json:"failed"`
		Failures  []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"failures"`
		ElapsedMs int64 `json:"elapsed_ms"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 || len(result.Failures) != 0 {
		t.Fatalf("unexpected batch result: %+v", result)
	}

	var metaA model.IssueInternalMeta
	if err := env.db.Where("issue_id = ?", issueAID).First(&metaA).Error; err != nil {
		t.Fatalf("load issueA meta: %v", err)
	}
	if metaA.WorkflowStatus != model.IssueWorkflowStatusDone {
		t.Fatalf("expected issueA done, got %q", metaA.WorkflowStatus)
	}
	var metaB model.IssueInternalMeta
	if err := env.db.Where("issue_id = ?", issueBID).First(&metaB).Error; err != nil {
		t.Fatalf("load issueB meta: %v", err)
	}
	if metaB.WorkflowStatus != model.IssueWorkflowStatusInProgress {
		t.Fatalf("expected issueB in_progress, got %q", metaB.WorkflowStatus)
	}
}

func TestIssueHandlerBatchUpdateInternalMeta_PartialFailureStillReturns200(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-meta-partial")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)
	missingID := uuid.NewString()

	ctx, rec := batchMetaJSONContext(t, user.ID, []map[string]any{
		{"issue_id": issue.ID, "workflow_status": "done"},
		{"issue_id": missingID, "workflow_status": "done"},
		{"issue_id": issue.ID, "workflow_status": "bogus"},
	})
	env.issueHandler.BatchUpdateInternalMeta(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Total     int64 `json:"total"`
		Succeeded int   `json:"succeeded"`
		Failed    int   `json:"failed"`
		Failures  []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"failures"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Total != 3 || result.Succeeded != 1 || result.Failed != 2 {
		t.Fatalf("unexpected batch result: %+v", result)
	}
	if len(result.Failures) != 2 || result.Failures[0].ID != missingID || result.Failures[1].ID != issue.ID {
		t.Fatalf("unexpected failures: %+v", result.Failures)
	}
}

func TestIssueHandlerBatchUpdateInternalMeta_RejectsInvalidRequests(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-meta-invalid")

	overLimit := make([]map[string]any, 0, 201)
	for i := 0; i <= 200; i++ {
		overLimit = append(overLimit, map[string]any{"issue_id": uuid.NewString(), "workflow_status": "done"})
	}

	cases := []struct {
		name string
		body []byte
	}{
		{"malformed json", []byte(`{"items":`)},
		{"missing items", []byte(`{}`)},
		{"empty items", []byte(`{"items":[]}`)},
		{"empty issue_id", []byte(`{"items":[{"issue_id":"","workflow_status":"done"}]}`)},
		{"blank issue_id", []byte(`{"items":[{"issue_id":"  ","workflow_status":"done"}]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newJSONContext(http.MethodPut, "/api/issues/internal-meta", tc.body)
			ctx.Set(middleware.ContextKeyUserID, user.ID)
			ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
			env.issueHandler.BatchUpdateInternalMeta(ctx)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("over limit", func(t *testing.T) {
		ctx, rec := batchMetaJSONContext(t, user.ID, overLimit)
		env.issueHandler.BatchUpdateInternalMeta(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestIssueHandlerGet_IncludesCollabArea(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-collab-inline")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	getIssue := func() map[string]json.RawMessage {
		ctx, rec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID, nil)
		ctx.Params = ginParams("iid", issue.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
		env.issueHandler.Get(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var data map[string]json.RawMessage
		decodeEnvelope(t, rec, &data)
		return data
	}

	decodeCollab := func(data map[string]json.RawMessage) struct {
		Consensus *struct {
			Body string `json:"body"`
		} `json:"consensus"`
		Summary *struct {
			Body string `json:"body"`
		} `json:"summary"`
	} {
		raw, ok := data["collab"]
		if !ok {
			t.Fatalf("expected collab field in issue detail response")
		}
		var collab struct {
			Consensus *struct {
				Body string `json:"body"`
			} `json:"consensus"`
			Summary *struct {
				Body string `json:"body"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw, &collab); err != nil {
			t.Fatalf("decode collab: %v", err)
		}
		return collab
	}

	collab := decodeCollab(getIssue())
	if collab.Consensus != nil || collab.Summary != nil {
		t.Fatalf("expected empty collab docs, got %+v", collab)
	}

	now := time.Now().UTC()
	if err := env.db.Create(&model.IssueCollabDocument{
		IssueID:      issue.ID,
		Kind:         model.CollabDocumentKindConsensus,
		Body:         "已达成共识",
		AuthorUserID: user.ID,
		AuthorKind:   model.CollabAuthorAgent,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create collab doc: %v", err)
	}

	collab = decodeCollab(getIssue())
	if collab.Consensus == nil || collab.Consensus.Body != "已达成共识" {
		t.Fatalf("expected consensus body, got %+v", collab.Consensus)
	}
	if collab.Summary != nil {
		t.Fatalf("expected nil summary, got %+v", collab.Summary)
	}
}

func TestIssueHandlerList_OmitsCollabArea(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-collab-list")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	now := time.Now().UTC()
	if err := env.db.Create(&model.IssueCollabDocument{
		IssueID:      issue.ID,
		Kind:         model.CollabDocumentKindSummary,
		Body:         "总结",
		AuthorUserID: user.ID,
		AuthorKind:   model.CollabAuthorAgent,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create collab doc: %v", err)
	}

	ctx, rec := newJSONContext(http.MethodGet, "/api/projects/"+project.ID+"/issues", nil)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.issueHandler.List(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeEnvelope(t, rec, &result)
	if len(result.Items) != 1 {
		t.Fatalf("expected one issue, got %+v", result.Items)
	}
	if _, ok := result.Items[0]["collab"]; ok {
		t.Fatalf("list response must not include collab field: %s", string(result.Items[0]["collab"]))
	}
}
