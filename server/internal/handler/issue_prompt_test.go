package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestIssuePromptHandler_GetPrompts_NotConfigured(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-issue-prompt-get-empty")

	ctx, rec := newJSONContext(http.MethodGet, "/api/issue-prompts", nil)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.issuePromptHandler.GetPrompts(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 未配置时 data.prompts 必须为 JSON null（而非 []）。
	var envelope apiEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var data struct {
		Prompts *[]model.IssuePromptItem `json:"prompts"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.Prompts != nil {
		t.Fatalf("expected prompts JSON null for unconfigured user, got: %#v", data.Prompts)
	}
}

func TestIssuePromptHandler_UpdateAndGet_RoundTrip(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-issue-prompt-put-get")

	body, _ := json.Marshal(map[string]any{
		"prompts": []map[string]string{
			{"id": "id-1", "name": "默认", "content": "请处理此问题"},
			{"id": "id-2", "name": "详细", "content": "请仔细分析并修复此问题"},
		},
	})

	putCtx, putRec := newJSONContext(http.MethodPut, "/api/issue-prompts", body)
	putCtx.Set(middleware.ContextKeyUserID, user.ID)
	env.issuePromptHandler.UpdatePrompts(putCtx)

	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", putRec.Code, putRec.Body.String())
	}

	var putData struct {
		Prompts []model.IssuePromptItem `json:"prompts"`
	}
	decodeEnvelope(t, putRec, &putData)
	if len(putData.Prompts) != 2 {
		t.Fatalf("expected 2 prompts in PUT response, got %d", len(putData.Prompts))
	}
	if putData.Prompts[0].Name != "默认" || putData.Prompts[1].ID != "id-2" {
		t.Fatalf("unexpected PUT payload: %#v", putData.Prompts)
	}

	getCtx, getRec := newJSONContext(http.MethodGet, "/api/issue-prompts", nil)
	getCtx.Set(middleware.ContextKeyUserID, user.ID)
	env.issuePromptHandler.GetPrompts(getCtx)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET, got %d: %s", getRec.Code, getRec.Body.String())
	}

	var getData struct {
		Prompts []model.IssuePromptItem `json:"prompts"`
	}
	decodeEnvelope(t, getRec, &getData)
	if len(getData.Prompts) != 2 {
		t.Fatalf("expected 2 prompts persisted, got %d", len(getData.Prompts))
	}
	if getData.Prompts[0].Content != "请处理此问题" || getData.Prompts[1].Name != "详细" {
		t.Fatalf("unexpected GET payload: %#v", getData.Prompts)
	}
}

func TestIssuePromptHandler_Update_InvalidParams(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-issue-prompt-invalid")

	cases := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "empty prompts array", body: `{"prompts":[]}`},
		{name: "missing prompts field", body: `{"foo":"bar"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newJSONContext(http.MethodPut, "/api/issue-prompts", []byte(tc.body))
			ctx.Set(middleware.ContextKeyUserID, user.ID)
			env.issuePromptHandler.UpdatePrompts(ctx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}

			var envelope apiEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Code != 40001 {
				t.Fatalf("expected code 40001 for %s, got %d", tc.name, envelope.Code)
			}
		})
	}
}
