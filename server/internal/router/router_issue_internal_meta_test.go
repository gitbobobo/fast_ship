package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

func doBatchInternalMetaReq(t *testing.T, env *routerTestEnv, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()

	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPut, "/api/issues/internal-meta", nil)
	} else {
		req = httptest.NewRequest(http.MethodPut, "/api/issues/internal-meta", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

func createRouterBatchMetaAPIKey(t *testing.T, env *routerTestEnv, userID, name, rawKey string) string {
	t.Helper()
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      name,
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return "Bearer " + service.FormatApiKey(rawKey)
}

func TestRouterBatchInternalMetaAuthAndFlow(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "batchmeta", "batchmeta@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	issueA := createRouterTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})
	issueB := createRouterTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
		i.SequenceNumber = 2
	})
	apiKeyAuth := createRouterBatchMetaAPIKey(t, env, auth.UserID, "CI-BatchMeta", "BATCHMETAKEY1234567890")

	body := []byte(`{"items":[{"issue_id":"` + issueA.ID + `","workflow_status":"done"},{"issue_id":"` + issueB.ID + `","workflow_status":"in_progress"}]}`)

	// 无凭证 → 401
	if rec := doBatchInternalMetaReq(t, env, "", string(body)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	type batchResult struct {
		Total     int64 `json:"total"`
		Succeeded int   `json:"succeeded"`
		Failed    int   `json:"failed"`
		Failures  []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"failures"`
	}

	// JWT → 200 全部成功
	jwtRec := doBatchInternalMetaReq(t, env, "Bearer "+auth.Token, string(body))
	if jwtRec.Code != http.StatusOK {
		t.Fatalf("JWT batch expected 200, got %d: %s", jwtRec.Code, jwtRec.Body.String())
	}
	var jwtResult batchResult
	decodeRouterEnvelope(t, jwtRec, &jwtResult)
	if jwtResult.Total != 2 || jwtResult.Succeeded != 2 || jwtResult.Failed != 0 {
		t.Fatalf("unexpected JWT batch result: %+v", jwtResult)
	}

	// API Key → 200 全部成功（幂等重复同一状态）
	keyRec := doBatchInternalMetaReq(t, env, apiKeyAuth, string(body))
	if keyRec.Code != http.StatusOK {
		t.Fatalf("API key batch expected 200, got %d: %s", keyRec.Code, keyRec.Body.String())
	}
	var keyResult batchResult
	decodeRouterEnvelope(t, keyRec, &keyResult)
	if keyResult.Total != 2 || keyResult.Succeeded != 2 || keyResult.Failed != 0 {
		t.Fatalf("unexpected API key batch result: %+v", keyResult)
	}

	var stored model.IssueInternalMeta
	if err := env.db.Where("issue_id = ?", issueA.ID).First(&stored).Error; err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if stored.WorkflowStatus != model.IssueWorkflowStatusDone {
		t.Fatalf("expected issueA workflow done, got %q", stored.WorkflowStatus)
	}

	// 参数错误 → 400（两类凭证一致）
	for _, tc := range []struct {
		name string
		auth string
		body string
	}{
		{"jwt empty items", "Bearer " + auth.Token, `{"items":[]}`},
		{"apikey missing items", apiKeyAuth, `{}`},
	} {
		if rec := doBatchInternalMetaReq(t, env, tc.auth, tc.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	// 单条路径不受全局路径影响
	perIssueReq := httptest.NewRequest(http.MethodPut, "/api/issues/"+issueA.ID+"/internal-meta", bytes.NewReader([]byte(`{"workflow_status":"todo"}`)))
	perIssueReq.Header.Set("Content-Type", "application/json")
	perIssueReq.Header.Set("Authorization", apiKeyAuth)
	perIssueRec := httptest.NewRecorder()
	env.router.ServeHTTP(perIssueRec, perIssueReq)
	if perIssueRec.Code != http.StatusOK {
		t.Fatalf("per-issue internal-meta expected 200, got %d: %s", perIssueRec.Code, perIssueRec.Body.String())
	}
}

func TestRouterBatchInternalMetaCrossUserCountsAsItemFailure(t *testing.T) {
	env := setupRouterTestEnv(t)
	owner := createRouterTestUser(t, env.db, "user-batch-owner", "batchowner", "batchowner@example.com")
	intruder := createRouterTestUser(t, env.db, "user-batch-intruder", "batchintruder", "batchintruder@example.com")
	ownerProject := createRouterTestProject(t, env.db, owner.ID)
	issue := createRouterTestIssue(t, env.db, ownerProject.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})
	intruderAuth := createRouterBatchMetaAPIKey(t, env, intruder.ID, "CI-Intruder", "BATCHMETAKEYFOREIGN1")

	rec := doBatchInternalMetaReq(t, env, intruderAuth, `{"items":[{"issue_id":"`+issue.ID+`","workflow_status":"done"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-user item expected 200 envelope, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Total    int64 `json:"total"`
		Failed   int   `json:"failed"`
		Failures []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"failures"`
	}
	decodeRouterEnvelope(t, rec, &result)
	if result.Total != 1 || result.Failed != 1 || len(result.Failures) != 1 || result.Failures[0].ID != issue.ID {
		t.Fatalf("expected single counted failure for foreign issue, got %+v", result)
	}
}
