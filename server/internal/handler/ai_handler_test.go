package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/middleware"
)

func TestAIHandler_GenerateTitle_E2E(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-e2e")

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		t.Logf("AI request body: %s", string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "修复登录页面白屏问题" } }
			]
		}`))
	}))
	defer aiServer.Close()

	setupAIForUser(t, env, user.ID, aiServer.URL)

	genBody, _ := json.Marshal(map[string]string{
		"body": "打开应用后登录页面白屏，输入账号密码后无法跳转到首页",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/ai/generate-title", genBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	t.Logf("Response: status=%d body=%s", rec.Code, rec.Body.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Title string `json:"title"`
	}
	decodeEnvelope(t, rec, &data)
	if data.Title != "修复登录页面白屏问题" {
		t.Fatalf("unexpected title: %q", data.Title)
	}
}

func TestAIHandler_GenerateTitle_AIProviderReturnsHTTP500(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-ai-provider-500")

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer aiServer.Close()

	setupAIForUser(t, env, user.ID, aiServer.URL)

	genBody, _ := json.Marshal(map[string]string{
		"body": "这是一段足够长的正文内容用于测试AI服务端错误的情况",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/ai/generate-title", genBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	t.Logf("Response: status=%d body=%s", rec.Code, rec.Body.String())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when AI provider returns error, got %d", rec.Code)
	}
}

func TestAIHandler_GenerateTitle_AIReturnsQuotedTitle(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-quoted")

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "\"修复登录白屏\"" } }
			]
		}`))
	}))
	defer aiServer.Close()

	setupAIForUser(t, env, user.ID, aiServer.URL)

	genBody, _ := json.Marshal(map[string]string{
		"body": "这是一段足够长的正文内容用于测试标题中的引号是否被正确处理",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/ai/generate-title", genBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	t.Logf("Response: status=%d body=%s", rec.Code, rec.Body.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for quoted title, got %d: %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Title string `json:"title"`
	}
	decodeEnvelope(t, rec, &data)
	if data.Title != "修复登录白屏" {
		t.Fatalf("expected quotes stripped, got: %q", data.Title)
	}
}

func TestAIHandler_GenerateTitle_AIReturnsEmptyContent(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-empty")

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "" } }
			]
		}`))
	}))
	defer aiServer.Close()

	setupAIForUser(t, env, user.ID, aiServer.URL)

	genBody, _ := json.Marshal(map[string]string{
		"body": "这是一段足够长的正文内容用于测试空内容返回时的处理逻辑",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/ai/generate-title", genBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	t.Logf("Response: status=%d body=%s", rec.Code, rec.Body.String())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for empty AI content, got %d", rec.Code)
	}
}

func TestAIHandler_GenerateTitle_AIReturnsBaseRespError(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-base-err")

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 1001, "status_msg": "rate limit exceeded" },
			"choices": []
		}`))
	}))
	defer aiServer.Close()

	setupAIForUser(t, env, user.ID, aiServer.URL)

	genBody, _ := json.Marshal(map[string]string{
		"body": "这是一段足够长的正文内容用于测试AI限流返回错误的情况",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/ai/generate-title", genBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	t.Logf("Response: status=%d body=%s", rec.Code, rec.Body.String())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for base_resp error, got %d", rec.Code)
	}
}

func TestAIHandler_GenerateTitle_ShortBody(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-short")

	reqBody, _ := json.Marshal(map[string]string{
		"body": "太短了",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/ai/generate-title", reqBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAIHandler_GenerateTitle_NoAISettings(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-gen-title-no-ai")

	reqBody, _ := json.Marshal(map[string]string{
		"body": "这是一段足够长的正文内容用于测试生成标题",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/ai/generate-title", reqBody)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.aiHandler.GenerateTitle(ctx)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func setupAIForUser(t *testing.T, env *handlerTestEnv, userID, aiServerURL string) {
	t.Helper()
	ctx, rec := newJSONContext(http.MethodPut, "/ai/settings", marshalJSON(t, updateAISettingsRequest{
		APIHost: aiServerURL,
		APIKey:  "sk-test-key",
		Model:   "MiniMax-M2.5",
	}))
	ctx.Set(middleware.ContextKeyUserID, userID)
	env.aiHandler.UpdateSettings(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup AI settings failed: %d %s", rec.Code, rec.Body.String())
	}
}

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}
