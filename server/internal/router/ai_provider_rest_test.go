package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// AI 提供商 REST 契约（INT-133）：按登录用户隔离、目录端点、密钥只回末四位、
// 被引用删除 409。

func aiProviderCreate(t *testing.T, e *e2eEnv, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	return e.doJSON(t, http.MethodPost, "/api/ai-providers", body)
}

func TestAIProviderRESTLifecycle(t *testing.T) {
	e := setupE2E(t)

	// custom：地址规范化 + 末四位 + bot_count=0。
	code, body := aiProviderCreate(t, e, map[string]interface{}{
		"catalog_id": "custom", "name": "网关", "base_url": "https://gw.example.com/v1/", "api_key": "sk-rest-1111",
	})
	if code != http.StatusOK {
		t.Fatalf("create custom: %d %v", code, body)
	}
	data := envelopeData(body)
	if data["base_url"] != "https://gw.example.com/v1" || data["api_key_last4"] != "1111" || data["bot_count"].(float64) != 0 {
		t.Fatalf("unexpected provider: %v", data)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "sk-rest-1111") {
		t.Fatalf("响应泄露明文 key")
	}
	id := data["id"].(string)

	// 目录提供商：地址由目录决定、名称取目录名。
	code, body = aiProviderCreate(t, e, map[string]interface{}{
		"catalog_id": "deepseek", "base_url": "https://ignored.example.com", "api_key": "sk-rest-2222",
	})
	if code != http.StatusOK {
		t.Fatalf("create catalog: %d %v", code, body)
	}
	catalogData := envelopeData(body)
	if catalogData["name"] != "DeepSeek" || catalogData["catalog_id"] != "deepseek" {
		t.Fatalf("unexpected catalog provider: %v", catalogData)
	}

	// 校验失败面。
	for i, bad := range []map[string]interface{}{
		{"catalog_id": "custom", "api_key": ""},
		{"catalog_id": "custom", "base_url": "https://x.example.com"},
		{"catalog_id": "no-such", "api_key": "sk-x"},
		{"base_url": "https://x.example.com", "api_key": "sk-x"},
	} {
		if code, body := aiProviderCreate(t, e, bad); code != http.StatusBadRequest {
			t.Fatalf("用例 %d 应 400: %d %v", i, code, body)
		}
	}

	// 更新：custom 可改地址与密钥；目录 provider 的 base_url 忽略。
	code, body = e.doJSON(t, http.MethodPut, "/api/ai-providers/"+id, map[string]interface{}{
		"base_url": "https://gw2.example.com/v1", "api_key": "sk-rest-3333",
	})
	if code != http.StatusOK {
		t.Fatalf("update: %d %v", code, body)
	}
	if got := envelopeData(body); got["base_url"] != "https://gw2.example.com/v1" || got["api_key_last4"] != "3333" {
		t.Fatalf("unexpected updated: %v", got)
	}

	// 列表与详情。
	code, body = e.doJSON(t, http.MethodGet, "/api/ai-providers", nil)
	if code != http.StatusOK || len(envelopeItems(body)) != 2 {
		t.Fatalf("list: %d %v", code, body)
	}
	if code, body = e.doJSON(t, http.MethodGet, "/api/ai-providers/"+id, nil); code != http.StatusOK {
		t.Fatalf("get: %d %v", code, body)
	}

	// 建一个机器人引用后删除应 409。
	code, body = e.doJSON(t, http.MethodPost, "/api/bots", map[string]interface{}{
		"name": "rest-bot", "provider_id": id, "model": "m",
	})
	if code != http.StatusOK {
		t.Fatalf("create bot: %d %v", code, body)
	}
	botID := envelopeData(body)["id"].(string)
	if code, body := e.doJSON(t, http.MethodDelete, "/api/ai-providers/"+id, nil); code != http.StatusConflict {
		t.Fatalf("被引用删除应 409: %d %v", code, body)
	}

	// 目录端点：内置 models.dev 子集，含重点提供商。
	code, body = e.doJSON(t, http.MethodGet, "/api/ai-providers/catalog", nil)
	if code != http.StatusOK {
		t.Fatalf("catalog: %d %v", code, body)
	}
	items := envelopeItems(body)
	if len(items) < 100 {
		t.Fatalf("目录应约 200 家，实得 %d", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		p := it.(map[string]interface{})
		seen[p["id"].(string)] = true
	}
	for _, want := range []string{"zai", "deepseek", "alibaba", "volcengine"} {
		if !seen[want] {
			t.Fatalf("目录应包含 %s", want)
		}
	}

	// 解除引用（删除机器人）后可删除 provider。
	if code, body := e.doJSON(t, http.MethodDelete, "/api/bots/"+botID, nil); code != http.StatusOK {
		t.Fatalf("delete bot: %d %v", code, body)
	}
	if code, _ := e.doJSON(t, http.MethodDelete, "/api/ai-providers/"+id, nil); code != http.StatusOK {
		t.Fatalf("解除引用后应可删除")
	}
}

func TestAIProviderRESTIsolation(t *testing.T) {
	e := setupE2E(t)
	// 公开注册关闭（INT-129）：第二个用户走邀请注册通道。
	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token, "aiprov-b", "aiprov-b@example.com", "Password123")
	jwtB := "Bearer " + other.Token

	code, body := aiProviderCreate(t, e, map[string]interface{}{
		"catalog_id": "custom", "base_url": "https://a.example.com/v1", "api_key": "sk-iso-1111",
	})
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, body)
	}
	id := envelopeData(body)["id"].(string)

	// 另一用户的列表看不到它；直读/改/删都 404。
	code, body = e.doJSONAs(t, jwtB, http.MethodGet, "/api/ai-providers", nil)
	if code != http.StatusOK || len(envelopeItems(body)) != 0 {
		t.Fatalf("B 列表应为空: %d %v", code, body)
	}
	if code, _ = e.doJSONAs(t, jwtB, http.MethodGet, "/api/ai-providers/"+id, nil); code != http.StatusNotFound {
		t.Fatalf("B 直读应 404")
	}
	if code, _ = e.doJSONAs(t, jwtB, http.MethodPut, "/api/ai-providers/"+id, map[string]interface{}{"name": "hijack"}); code != http.StatusNotFound {
		t.Fatalf("B 改应 404")
	}
	if code, _ = e.doJSONAs(t, jwtB, http.MethodDelete, "/api/ai-providers/"+id, nil); code != http.StatusNotFound {
		t.Fatalf("B 删应 404")
	}
}

func TestAIProviderRESTRequiresJWT(t *testing.T) {
	e := setupE2E(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/ai-providers"},
		{http.MethodPost, "/api/ai-providers"},
		{http.MethodPost, "/api/ai-providers/test"},
		{http.MethodGet, "/api/ai-providers/catalog"},
		{http.MethodGet, "/api/ai-providers/some-id"},
		{http.MethodPut, "/api/ai-providers/some-id"},
		{http.MethodDelete, "/api/ai-providers/some-id"},
	} {
		code, _ := e.doJSONAs(t, "", tc.method, tc.path, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s %s 应 401，实得 %d", tc.method, tc.path, code)
		}
	}
}
