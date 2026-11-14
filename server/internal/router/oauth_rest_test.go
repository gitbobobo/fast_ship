package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// OAuth 2.1 授权服务（INT-222）路由层测试：well-known 元数据、动态客户端
// 注册（DCR）、授权端点鉴权、完整授权码流程、refresh 流程与吊销即时生效。

func oauthEnv(t *testing.T) *routerTestEnv {
	t.Helper()
	return setupRouterTestEnv(t, func(c *config.Config) {
		c.Server.PublicURL = "https://fs.example.com"
	})
}

// oauthGetJSON GET 请求并解析 JSON 响应体。
func oauthGetJSON(t *testing.T, env *routerTestEnv, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// oauthPostRaw 以指定 Content-Type POST 原始 body（协议端点不套 envelope）。
func oauthPostRaw(t *testing.T, env *routerTestEnv, path, contentType, body string, headers map[string]string) (int, http.Header, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	var parsed map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, rec.Header(), parsed
}

// oauthPostJSON JSON POST。
func oauthPostJSON(t *testing.T, env *routerTestEnv, path, body string, headers map[string]string) (int, map[string]interface{}) {
	t.Helper()
	code, _, parsed := oauthPostRaw(t, env, path, "application/json", body, headers)
	return code, parsed
}

// oauthRegisterClient 经 /oauth/register 注册一个客户端，返回 client_id。
func oauthRegisterClient(t *testing.T, env *routerTestEnv) string {
	t.Helper()
	code, body := oauthPostJSON(t, env, "/oauth/register",
		`{"client_name":"MCP 客户端","redirect_uris":["https://client.example.com/cb","http://127.0.0.1:8931/callback"],"token_endpoint_auth_method":"none"}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("register client: %d %v", code, body)
	}
	id, _ := body["client_id"].(string)
	if id == "" {
		t.Fatalf("register 响应缺 client_id: %v", body)
	}
	return id
}

// oauthPKCEPair 生成 S256 verifier/challenge 对。
func oauthPKCEPair() (string, string) {
	verifier := strings.Repeat("m", 64)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// oauthAuthorize 以 JWT 身份提交授权，返回 redirect 地址。
func oauthAuthorize(t *testing.T, env *routerTestEnv, jwt, clientID, redirectURI, challenge, state string, projectIDs []string, write bool) (int, string) {
	t.Helper()
	ids, _ := json.Marshal(projectIDs)
	body := `{"client_id":"` + clientID + `","redirect_uri":"` + redirectURI + `","code_challenge":"` + challenge + `","code_challenge_method":"S256","state":"` + state + `","project_ids":` + string(ids) + `,"write":` + boolStr(write) + `}`
	code, resp := oauthPostJSON(t, env, "/api/oauth/authorize", body, map[string]string{"Authorization": "Bearer " + jwt})
	if code != http.StatusOK {
		return code, ""
	}
	data, _ := resp["data"].(map[string]interface{})
	redirect, _ := data["redirect"].(string)
	return code, redirect
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// oauthToken 调 token 端点（form 编码），返回 (status, body)。
func oauthToken(t *testing.T, env *routerTestEnv, form url.Values) (int, map[string]interface{}) {
	t.Helper()
	code, _, body := oauthPostRaw(t, env, "/oauth/token", "application/x-www-form-urlencoded", form.Encode(), nil)
	return code, body
}

func TestOAuthWellKnownMetadata(t *testing.T) {
	env := oauthEnv(t)

	code, body := oauthGetJSON(t, env, "/.well-known/oauth-protected-resource")
	if code != http.StatusOK {
		t.Fatalf("protected-resource: %d %v", code, body)
	}
	if body["resource"] != "https://fs.example.com/mcp" {
		t.Fatalf("resource 错误: %v", body["resource"])
	}
	servers, _ := body["authorization_servers"].([]interface{})
	if len(servers) != 1 || servers[0] != "https://fs.example.com" {
		t.Fatalf("authorization_servers 错误: %v", servers)
	}

	code, body = oauthGetJSON(t, env, "/.well-known/oauth-authorization-server")
	if code != http.StatusOK {
		t.Fatalf("authorization-server: %d %v", code, body)
	}
	if body["issuer"] != "https://fs.example.com" ||
		body["authorization_endpoint"] != "https://fs.example.com/oauth/authorize" ||
		body["token_endpoint"] != "https://fs.example.com/oauth/token" ||
		body["registration_endpoint"] != "https://fs.example.com/oauth/register" {
		t.Fatalf("8414 元数据错误: %v", body)
	}
	if grants, _ := body["grant_types"].([]interface{}); len(grants) != 2 {
		t.Fatalf("grant_types 错误: %v", grants)
	}
	if methods, _ := body["code_challenge_methods"].([]interface{}); len(methods) != 1 || methods[0] != "S256" {
		t.Fatalf("code_challenge_methods 错误: %v", methods)
	}
	if methods, _ := body["token_endpoint_auth_methods"].([]interface{}); len(methods) != 1 || methods[0] != "none" {
		t.Fatalf("token_endpoint_auth_methods 错误: %v", methods)
	}
}

func TestOAuthDynamicClientRegistration(t *testing.T) {
	env := oauthEnv(t)

	// 成功注册：201 + client_id，无 secret。
	code, body := oauthPostJSON(t, env, "/oauth/register",
		`{"client_name":"MCP 客户端","redirect_uris":["https://client.example.com/cb","http://127.0.0.1:8931/callback"],"token_endpoint_auth_method":"none","software_id":"unknown-field-ignored"}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("register: %d %v", code, body)
	}
	if id, _ := body["client_id"].(string); id == "" {
		t.Fatalf("缺 client_id: %v", body)
	}
	if body["token_endpoint_auth_method"] != "none" {
		t.Fatalf("auth_method 应为 none: %v", body)
	}
	if _, hasSecret := body["client_secret"]; hasSecret {
		t.Fatalf("公开客户端不应返回 client_secret")
	}

	// 非法 redirect_uri：400 invalid_redirect_uri。
	code, body = oauthPostJSON(t, env, "/oauth/register",
		`{"client_name":"bad","redirect_uris":["http://evil.example.com/cb"]}`, nil)
	if code != http.StatusBadRequest || body["error"] != "invalid_redirect_uri" {
		t.Fatalf("非回环 http 应 invalid_redirect_uri: %d %v", code, body)
	}

	// auth_method 非 none：400。
	code, body = oauthPostJSON(t, env, "/oauth/register",
		`{"client_name":"bad","redirect_uris":["https://client.example.com/cb"],"token_endpoint_auth_method":"client_secret_basic"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("auth_method 非 none 应拒绝: %d %v", code, body)
	}
}

// GET /api/oauth/authorize 未登录 401；登录后返回客户端名与项目清单。
func TestOAuthAuthorizeEndpointAuth(t *testing.T) {
	env := oauthEnv(t)

	_, challenge := oauthPKCEPair()
	code, _ := oauthGetJSON(t, env, "/api/oauth/authorize?client_id=x&redirect_uri=https://a&code_challenge="+challenge+"&code_challenge_method=S256")
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，得到 %d", code)
	}

	auth := registerAndLoginRouterUser(t, env.router, "oauthuser", "oauthuser@example.com", "Password123")
	clientID := oauthRegisterClient(t, env)

	req := httptest.NewRequest(http.MethodGet, "/api/oauth/authorize?client_id="+clientID+"&redirect_uri=https%3A%2F%2Fclient.example.com%2Fcb&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize preview: %d %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		ClientName string `json:"client_name"`
		Projects   []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	decodeRouterEnvelope(t, rec, &envelope)
	if envelope.ClientName != "MCP 客户端" || len(envelope.Projects) != 0 {
		t.Fatalf("preview 数据错误: %+v body=%s", envelope, rec.Body.String())
	}
}

// 完整授权码流程：注册 → 项目 → 授权 → 换 token → 校验 access。
func TestOAuthFullAuthorizationCodeFlow(t *testing.T) {
	env := oauthEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "oauthflow", "oauthflow@example.com", "Password123")

	// 经 REST 建一个项目（归属当前用户）。
	code, body := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodPost, "/api/projects", `{"name":"oauth-flow-project","description":"d"}`)
	if code != http.StatusOK {
		t.Fatalf("create project: %d %v", code, body)
	}
	projectID, _ := body["data"].(map[string]interface{})["id"].(string)
	if projectID == "" {
		t.Fatalf("缺项目 id: %v", body)
	}

	clientID := oauthRegisterClient(t, env)
	verifier, challenge := oauthPKCEPair()

	// GET 预取：应列出该项目。
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/authorize?client_id="+clientID+"&redirect_uri=https%3A%2F%2Fclient.example.com%2Fcb&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	var preview struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	decodeRouterEnvelope(t, rec, &preview)
	if len(preview.Projects) != 1 || preview.Projects[0].ID != projectID {
		t.Fatalf("preview 项目清单错误: %+v", preview)
	}

	// POST 授权：redirect 带 code + state。
	code, redirect := oauthAuthorize(t, env, auth.Token, clientID, "https://client.example.com/cb", challenge, "st-xyz", []string{projectID}, true)
	if code != http.StatusOK || !strings.HasPrefix(redirect, "https://client.example.com/cb?") {
		t.Fatalf("authorize: %d %q", code, redirect)
	}
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := u.Query()
	if q.Get("code") == "" || q.Get("state") != "st-xyz" {
		t.Fatalf("redirect 缺 code/state: %s", redirect)
	}
	authCode := q.Get("code")

	// token 端点：错误的 redirect_uri 拒绝。
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", authCode)
	form.Set("redirect_uri", "https://client.example.com/other")
	form.Set("code_verifier", verifier)
	code, body = oauthToken(t, env, form)
	if code != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("redirect_uri 不匹配应 invalid_grant: %d %v", code, body)
	}

	// 正确兑换。
	form.Set("redirect_uri", "https://client.example.com/cb")
	code, body = oauthToken(t, env, form)
	if code != http.StatusOK {
		t.Fatalf("token: %d %v", code, body)
	}
	if body["token_type"] != "Bearer" || body["expires_in"].(float64) != 3600 {
		t.Fatalf("token 响应形状错误: %v", body)
	}
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	if !strings.HasPrefix(access, "fsm_") || !strings.HasPrefix(refresh, "fsn_") {
		t.Fatalf("令牌前缀错误: %v", body)
	}

	// access 可被资源服务器校验，scope 覆盖项目写权限。
	info, err := env.oauthService.ValidateAccessToken(context.Background(), access)
	if err != nil {
		t.Fatalf("validate access: %v", err)
	}
	if !service.ScopeHasWrite(info.Scopes, projectID) {
		t.Fatalf("scope 应含 write: %s", info.Scopes)
	}

	// 授权码重放：拒绝。
	code, body = oauthToken(t, env, form)
	if code != http.StatusBadRequest {
		t.Fatalf("授权码重放应拒绝: %d %v", code, body)
	}

	// refresh 流程。
	rform := url.Values{}
	rform.Set("grant_type", "refresh_token")
	rform.Set("client_id", clientID)
	rform.Set("refresh_token", refresh)
	code, body = oauthToken(t, env, rform)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %v", code, body)
	}
	nextAccess, _ := body["access_token"].(string)
	if nextAccess == "" || nextAccess == access {
		t.Fatalf("refresh 应换出新 access")
	}
	// 旧 refresh 重放拒绝。
	code, _ = oauthToken(t, env, rform)
	if code != http.StatusBadRequest {
		t.Fatalf("旧 refresh 重放应拒绝")
	}

	// DELETE 授权 → access 校验失败。
	listCode, listBody := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodGet, "/api/oauth/authorizations", "")
	if listCode != http.StatusOK {
		t.Fatalf("list authorizations: %d %v", listCode, listBody)
	}
	items, _ := listBody["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("应有 1 条授权: %v", listBody)
	}
	authID, _ := items[0].(map[string]interface{})["id"].(string)
	delCode, delBody := doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodDelete, "/api/oauth/authorizations/"+authID, "")
	if delCode != http.StatusOK {
		t.Fatalf("delete authorization: %d %v", delCode, delBody)
	}
	if _, err := env.oauthService.ValidateAccessToken(context.Background(), nextAccess); err == nil {
		t.Fatalf("吊销授权后 access 校验应失败")
	}
}

// doRouterJSON 带 JSON body 的路由调用，返回 (status, parsed body)。
func doRouterJSON(t *testing.T, env *routerTestEnv, bearer string, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	var parsed map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

// doRouterJSON 带 JSON body 的路由调用，返回 (status, parsed body)。

// deny 端点：注册 redirect_uri 校验通过后 302 携 error=access_denied；
// 未注册 redirect_uri 一律 400、绝不重定向（防开放重定向）。

// deny 端点：注册 redirect_uri 校验通过后 302 携 error=access_denied；
// 未注册 redirect_uri 一律 400、绝不重定向（防开放重定向）。
func TestOAuthDenyRedirect(t *testing.T) {
	env := oauthEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "denyuser", "denyuser@example.com", "Password123!")
	doRouterJSON(t, env, "Bearer "+auth.Token, http.MethodPost, "/api/projects", `{"name":"deny-project","description":"d"}`)

	clientID := oauthRegisterClient(t, env)
	const redirectURI = "https://client.example.com/cb"

	// 注册 redirect_uri：302 回调携带 error=access_denied 与原样 state。
	denyPath := "/oauth/deny?client_id=" + clientID +
		"&redirect_uri=" + url.QueryEscape(redirectURI) + "&state=deny-st"
	req := httptest.NewRequest(http.MethodGet, denyPath, nil)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("deny 应 302: %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, redirectURI+"?") ||
		!strings.Contains(loc, "error=access_denied") ||
		!strings.Contains(loc, "state=deny-st") {
		t.Fatalf("Location 错误: %s", loc)
	}

	// 未注册 redirect_uri：400，无 Location。
	req = httptest.NewRequest(http.MethodGet, "/oauth/deny?client_id="+clientID+
		"&redirect_uri="+url.QueryEscape("https://evil.example.com/cb")+"&state=x", nil)
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Fatalf("未注册 redirect_uri 应 400 且不重定向: %d %q", w.Code, w.Header().Get("Location"))
	}

	// 未知 client：400。
	req = httptest.NewRequest(http.MethodGet, "/oauth/deny?client_id=nope"+
		"&redirect_uri="+url.QueryEscape(redirectURI), nil)
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知 client 应 400: %d", w.Code)
	}
}
