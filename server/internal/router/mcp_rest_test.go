package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// MCP Streamable HTTP 端点（INT-222）路由层端到端：DCR 注册客户端 → JWT 用户
// 授权（读/写两组 scope）→ /oauth/token 换令牌 → /mcp 挑战与工具调用。
// MCP 客户端用裸 HTTP POST JSON-RPC（无状态模式，无需 session 管理）。

const mcpProtocolVersion = "2025-06-18"

// mcpPost 向 /mcp 发一条 JSON-RPC 请求，返回 (status, header, parsed body)。
func mcpPost(t *testing.T, env *routerTestEnv, bearer, method string, id int, params string) (int, http.Header, map[string]interface{}) {
	t.Helper()
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	var parsed map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, rec.Header(), parsed
}

// mcpCallTool 调用工具，返回 result 对象（isError 语义在 result 内）。
func mcpCallTool(t *testing.T, env *routerTestEnv, bearer, name, args string) (int, map[string]interface{}) {
	t.Helper()
	params := fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, args)
	code, _, parsed := mcpPost(t, env, bearer, "tools/call", 10, params)
	if result, ok := parsed["result"].(map[string]interface{}); ok {
		return code, result
	}
	// 协议级错误（JSON-RPC error）也带回，便于断言。
	errObj, _ := parsed["error"].(map[string]interface{})
	if errObj == nil {
		errObj = parsed
	}
	return code, errObj
}

// mcpFirstIssueID 从 list 工具结果取第一个 issue 的 id。
func mcpFirstIssueID(t *testing.T, result map[string]interface{}) string {
	t.Helper()
	structured, _ := result["structuredContent"].(map[string]interface{})
	if structured == nil {
		structured = result
	}
	issues, _ := structured["issues"].([]interface{})
	if len(issues) == 0 {
		return ""
	}
	item, _ := issues[0].(map[string]interface{})
	id, _ := item["id"].(string)
	return id
}

// 无令牌 / 非法令牌访问 /mcp：401 + WWW-Authenticate 挑战（含资源发现地址）。
func TestMCPUnauthorizedChallenge(t *testing.T) {
	env := oauthEnv(t)

	// 无令牌：挑战只带 resource_metadata，无 error 参数。
	code, header, body := mcpPost(t, env, "", "initialize", 1, `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，得到 %d %v", code, body)
	}
	wwwAuth := header.Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, `resource_metadata="https://fs.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("挑战缺 resource_metadata: %q", wwwAuth)
	}
	if strings.Contains(wwwAuth, "error=") {
		t.Fatalf("无令牌挑战不应带 error 参数: %q", wwwAuth)
	}

	// 非法令牌（非 fsm_ 前缀）：error="invalid_token"。
	code, header, _ = mcpPost(t, env, "fsk_not-an-oauth-token", "initialize", 1, `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("非 fsm_ 令牌应 401，得到 %d", code)
	}
	wwwAuth = header.Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, `error="invalid_token"`) {
		t.Fatalf("非法令牌挑战应带 invalid_token: %q", wwwAuth)
	}

	// fsm_ 前缀但哈希不命中：同样 invalid_token。
	code, _, _ = mcpPost(t, env, "fsm_deadbeef", "initialize", 1, `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("未知 fsm_ 令牌应 401，得到 %d", code)
	}
}

// 全链路：DCR → 授权 → 换令牌 → initialize → tools/list → scope 强制 →
// 关闭与评论 → 吊销授权后 401。
func TestMCPToolsEndToEnd(t *testing.T) {
	env := oauthEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "mcpuser", "mcpuser@example.com", "Password123")

	// 建项目。
	code, body := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodPost, "/api/projects", `{"name":"mcp-project","description":"d"}`)
	if code != http.StatusOK {
		t.Fatalf("create project: %d %v", code, body)
	}
	projectID, _ := body["data"].(map[string]interface{})["id"].(string)

	clientID := oauthRegisterClient(t, env)
	readClientID := oauthRegisterClient(t, env)
	verifier, challenge := oauthPKCEPair()
	const redirectURI = "https://client.example.com/cb"

	// 第一次授权：只读 scope（独立客户端；同客户端变档重授权会吊销旧令牌，
	// 见下方写 scope 授权与缩权语义）。
	code, redirect := oauthAuthorize(t, env, auth.Token, readClientID, redirectURI, challenge, "s1", []string{projectID}, false)
	if code != http.StatusOK {
		t.Fatalf("authorize read: %d", code)
	}
	readCode := extractQueryParam(t, redirect, "code")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", readClientID)
	form.Set("code", readCode)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	code, body = oauthToken(t, env, form)
	if code != http.StatusOK {
		t.Fatalf("token (read): %d %v", code, body)
	}
	readToken, _ := body["access_token"].(string)

	// 同 scope 重授权：scope 集合未变不应吊销旧令牌（避免误伤续授权场景），
	// 重授权后旧 read token 必须仍可用。
	_, challengeSame := oauthPKCEPair()
	code, _ = oauthAuthorize(t, env, auth.Token, readClientID, redirectURI, challengeSame, "s1b", []string{projectID}, false)
	if code != http.StatusOK {
		t.Fatalf("authorize read (same scope): %d", code)
	}
	if initCode, _, _ := mcpPost(t, env, readToken, "initialize", 20,
		`{"protocolVersion":"`+mcpProtocolVersion+`","capabilities":{},"clientInfo":{"name":"router-test","version":"0"}}`); initCode != http.StatusOK {
		t.Fatalf("同 scope 重授权后旧 read token 应仍有效: %d", initCode)
	}

	// 第二次授权：写 scope（独立客户端；同 client 变档授权会吊销旧令牌）。
	verifier2, challenge2 := oauthPKCEPair()
	code, redirect = oauthAuthorize(t, env, auth.Token, clientID, redirectURI, challenge2, "s2", []string{projectID}, true)
	if code != http.StatusOK {
		t.Fatalf("authorize write: %d", code)
	}
	writeCode := extractQueryParam(t, redirect, "code")

	form2 := url.Values{}
	form2.Set("grant_type", "authorization_code")
	form2.Set("client_id", clientID)
	form2.Set("code", writeCode)
	form2.Set("redirect_uri", redirectURI)
	form2.Set("code_verifier", verifier2)
	code, body = oauthToken(t, env, form2)
	if code != http.StatusOK {
		t.Fatalf("token (write): %d %v", code, body)
	}
	writeToken, _ := body["access_token"].(string)

	// initialize：serverInfo 为 fast-ship。
	code, _, parsed := mcpPost(t, env, writeToken, "initialize", 1,
		`{"protocolVersion":"`+mcpProtocolVersion+`","capabilities":{},"clientInfo":{"name":"router-test","version":"0"}}`)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %v", code, parsed)
	}
	result, _ := parsed["result"].(map[string]interface{})
	if result == nil {
		t.Fatalf("initialize 缺 result: %v", parsed)
	}
	serverInfo, _ := result["serverInfo"].(map[string]interface{})
	if serverInfo == nil || serverInfo["name"] != "fast-ship" {
		t.Fatalf("serverInfo 错误: %v", serverInfo)
	}

	// tools/list：包含全部第一期工具。
	code, _, parsed = mcpPost(t, env, writeToken, "tools/list", 2, `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list: %d %v", code, parsed)
	}
	result, _ = parsed["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})
	names := map[string]bool{}
	for _, tl := range tools {
		if m, ok := tl.(map[string]interface{}); ok {
			if n, ok := m["name"].(string); ok {
				names[n] = true
			}
		}
	}
	for _, want := range []string{
		"fastship_list_issues", "fastship_get_issue", "fastship_create_issue",
		"fastship_update_issue", "fastship_close_issue", "fastship_reopen_issue",
		"fastship_add_comment",
	} {
		if !names[want] {
			t.Fatalf("tools/list 缺 %s: %v", want, names)
		}
	}

	// 只读 scope：读工具成功，写工具被拒（isError 工具错误）。
	code, result = mcpCallTool(t, env, readToken, "fastship_list_issues", `{"project_id":"`+projectID+`"}`)
	if code != http.StatusOK {
		t.Fatalf("read list_issues: %d %v", code, result)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("只读 list_issues 不应失败: %v", result)
	}

	code, result = mcpCallTool(t, env, readToken, "fastship_create_issue",
		`{"project_id":"`+projectID+`","title":"越权创建"}`)
	if code != http.StatusOK {
		t.Fatalf("write create_issue HTTP: %d %v", code, result)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("只读 scope 调写工具应被拒: %v", result)
	}
	if text := toolErrorText(result); !strings.Contains(text, "写入权限") {
		t.Fatalf("拒绝文案错误: %v", result)
	}

	// 写 scope：创建 → 评论 → 更新 → 关闭。
	code, result = mcpCallTool(t, env, writeToken, "fastship_create_issue",
		`{"project_id":"`+projectID+`","title":"MCP 创建的 Issue","body":"正文内容","workflow_status":"todo"}`)
	if code != http.StatusOK {
		t.Fatalf("create_issue: %d %v", code, result)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("create_issue 工具错误: %v", result)
	}
	structured, _ := result["structuredContent"].(map[string]interface{})
	if structured == nil {
		structured = result
	}
	issueID, _ := structured["id"].(string)
	if issueID == "" {
		t.Fatalf("create_issue 缺 id: %v", result)
	}

	// 列表能看到新 issue（读工具；不用 q 过滤——router 测试环境不建 FTS 表，
	// 关键词搜索的端到端由 service 层 issue_list_sql_test.go 覆盖）。
	code, result = mcpCallTool(t, env, readToken, "fastship_list_issues", `{"project_id":"`+projectID+`","state":"open"}`)
	if id := mcpFirstIssueID(t, result); id != issueID {
		t.Fatalf("list 未返回新 issue: %v", result)
	}

	// 详情。
	code, result = mcpCallTool(t, env, writeToken, "fastship_get_issue", `{"issue_id":"`+issueID+`"}`)
	structured, _ = result["structuredContent"].(map[string]interface{})
	if structured == nil || structured["title"] != "MCP 创建的 Issue" {
		t.Fatalf("get_issue 数据错误: %v", result)
	}

	// 评论。
	code, result = mcpCallTool(t, env, writeToken, "fastship_add_comment",
		`{"issue_id":"`+issueID+`","body":"MCP 评论"}`)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("add_comment 工具错误: %v", result)
	}

	// 更新标题 + 标签。
	code, result = mcpCallTool(t, env, writeToken, "fastship_update_issue",
		`{"issue_id":"`+issueID+`","title":"MCP 更新后的标题"}`)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("update_issue 工具错误: %v", result)
	}

	// 参数校验：无修改字段的更新拒绝。
	_, result = mcpCallTool(t, env, writeToken, "fastship_update_issue", `{"issue_id":"`+issueID+`"}`)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("空更新应被拒: %v", result)
	}

	// 关闭。
	code, result = mcpCallTool(t, env, writeToken, "fastship_close_issue",
		`{"issue_id":"`+issueID+`","reason":"completed"}`)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("close_issue 工具错误: %v", result)
	}
	structured, _ = result["structuredContent"].(map[string]interface{})
	if structured == nil || structured["state"] != "closed" {
		t.Fatalf("close_issue 状态错误: %v", result)
	}

	// 重开。
	code, result = mcpCallTool(t, env, writeToken, "fastship_reopen_issue", `{"issue_id":"`+issueID+`"}`)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("reopen_issue 工具错误: %v", result)
	}
	structured, _ = result["structuredContent"].(map[string]interface{})
	if structured == nil || structured["state"] != "open" {
		t.Fatalf("reopen_issue 状态错误: %v", result)
	}

	// 越权项目 id：写工具拒、读工具也拒（scope 外）。
	otherID := "00000000-0000-4000-8000-000000000001"
	_, result = mcpCallTool(t, env, writeToken, "fastship_create_issue",
		`{"project_id":"`+otherID+`","title":"x"}`)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("scope 外项目应拒: %v", result)
	}

	// 变档重授权 → 旧令牌即时吊销（缩权/扩权同语义）。
	_, challenge3 := oauthPKCEPair()
	code, redirect = oauthAuthorize(t, env, auth.Token, clientID, redirectURI, challenge3, "s3", []string{projectID}, false)
	if code != http.StatusOK {
		t.Fatalf("authorize downgrade: %d", code)
	}
	code, _, _ = mcpPost(t, env, writeToken, "initialize", 42,
		`{"protocolVersion":"`+mcpProtocolVersion+`","capabilities":{},"clientInfo":{"name":"router-test","version":"0"}}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("变档重授权后旧 write token 应 401: %d", code)
	}

	// DELETE 授权 → 全部令牌即时失效。
	listCode, listBody := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodGet, "/api/oauth/authorizations", "")
	if listCode != http.StatusOK {
		t.Fatalf("list authorizations: %d %v", listCode, listBody)
	}
	items, _ := listBody["data"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("应有 2 条授权（写客户端 + 只读客户端）: %v", listBody)
	}
	for _, item := range items {
		authID, _ := item.(map[string]interface{})["id"].(string)
		delCode, _ := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodDelete, "/api/oauth/authorizations/"+authID, "")
		if delCode != http.StatusOK {
			t.Fatalf("delete authorization: %d", delCode)
		}
	}
	code, _, _ = mcpPost(t, env, readToken, "initialize", 3, `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("吊销后应 401，得到 %d", code)
	}
	code, _, _ = mcpPost(t, env, readToken, "initialize", 4, `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("吊销后只读令牌也应 401，得到 %d", code)
	}
}

func extractQueryParam(t *testing.T, redirect, key string) string {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect %q: %v", redirect, err)
	}
	v := u.Query().Get(key)
	if v == "" {
		t.Fatalf("redirect 缺 %s: %s", key, redirect)
	}
	return v
}

func toolErrorText(result map[string]interface{}) string {
	content, _ := result["content"].([]interface{})
	if len(content) == 0 {
		return ""
	}
	item, _ := content[0].(map[string]interface{})
	text, _ := item["text"].(string)
	return text
}
