package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 以下用例对照真实 T3 协议形状（packages/contracts/src/auth.ts、environmentHttp.ts），
// 假服务端按真实信封返回。

func TestExchangeBearer_Success(t *testing.T) {
	var gotForm map[string]string
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = map[string]string{}
		for k := range r.PostForm {
			gotForm[k] = r.PostForm.Get(k)
		}
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token": "bearer-token-1",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"token_type": "Bearer",
			"expires_in": 3600,
			"scope": "orchestration:read orchestration:operate"
		}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	creds, err := client.exchangeBearer(context.Background(), server.URL, "pairing-code-1")
	if err != nil {
		t.Fatalf("exchangeBearer: %v", err)
	}
	if creds.AccessToken != "bearer-token-1" {
		t.Fatalf("access token = %q", creds.AccessToken)
	}
	if creds.ExpiresAt.IsZero() {
		t.Fatalf("expected expiry to be set from expires_in")
	}
	// 真实请求字段：grant_type/subject_token/subject_token_type/requested_token_type；
	// scope 不携带（auth.ts optional，授予由配对链接的授权决定）
	if gotForm["grant_type"] != t3GrantTypeTokenExchange {
		t.Fatalf("grant_type = %q", gotForm["grant_type"])
	}
	if gotForm["subject_token"] != "pairing-code-1" {
		t.Fatalf("subject_token = %q", gotForm["subject_token"])
	}
	if gotForm["subject_token_type"] != t3TokenTypeBootstrap {
		t.Fatalf("subject_token_type = %q", gotForm["subject_token_type"])
	}
	if gotForm["requested_token_type"] != t3TokenTypeAccessToken {
		t.Fatalf("requested_token_type = %q", gotForm["requested_token_type"])
	}
	if _, hasScope := gotForm["scope"]; hasScope {
		t.Fatalf("exchange request must not carry scope, got %q", gotForm["scope"])
	}
	if ct := gotHeaders.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		t.Fatalf("content type = %q", ct)
	}
}

func TestExchangeBearer_RejectsWrongGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","reason":"invalid_scope","traceId":"t-1","message":"The environment rejected the request (invalid_scope)."}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	_, err := client.exchangeBearer(context.Background(), server.URL, "bad-code")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_scope") {
		t.Fatalf("expected upstream reason in error, got %v", err)
	}
}

func TestExchangeBearer_RejectsAuthInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-2","message":"The environment rejected this client's credentials (invalid_credential)."}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	_, err := client.exchangeBearer(context.Background(), server.URL, "consumed-code")
	if err == nil || !strings.Contains(err.Error(), "invalid_credential") {
		t.Fatalf("expected auth_invalid reason, got %v", err)
	}
}

// TestExchangeBearer_StoresGrantedScope 授予的 scope（只读或宽）都原样落库，
// 不再按「恰好 orchestration:read」拒绝（INT-85：不申请 scope，授予值由
// 配对链接的授权决定；会话能力由 access 判定，见 t3HasOperateScope）。
func TestExchangeBearer_StoresGrantedScope(t *testing.T) {
	cases := map[string]string{
		"read-only": "orchestration:read",
		"standard":  "orchestration:read orchestration:operate settings:write",
	}
	for name, granted := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"tok","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600,"scope":"` + granted + `"}`))
			}))
			defer server.Close()

			client := newT3HTTPClient()
			creds, err := client.exchangeBearer(context.Background(), server.URL, "code")
			if err != nil {
				t.Fatalf("expected grant to be stored, got %v", err)
			}
			if creds.Scope != granted {
				t.Fatalf("scope = %q, want %q", creds.Scope, granted)
			}
		})
	}
}

func TestExchangeBearer_RejectsDPoPTokenType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"dpop-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"DPoP","expires_in":3600,"scope":"orchestration:read"}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	_, err := client.exchangeBearer(context.Background(), server.URL, "code")
	if err == nil || !strings.Contains(err.Error(), "DPoP") {
		t.Fatalf("expected DPoP token type rejection, got %v", err)
	}
}

func TestProbeSession_StatusBranches(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer good-token" {
			t.Errorf("authorization = %q", auth)
		}
		if r.URL.Path != "/api/auth/session" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"authenticated":true,"auth":{"policy":"remote-reachable","bootstrapMethods":["one-time-token"],"sessionMethods":["bearer-access-token"],"sessionCookieName":"t3_session"},"scopes":["orchestration:read"],"sessionMethod":"bearer-access-token"}`))
	}))
	defer okServer.Close()

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-3"}`))
	}))
	defer badServer.Close()

	client := newT3HTTPClient()

	status, msg := client.probeSession(context.Background(), okServer.URL, "good-token")
	if status != T3EnvironmentStatusOK || msg != "" {
		t.Fatalf("ok probe = (%v, %q)", status, msg)
	}

	status, msg = client.probeSession(context.Background(), badServer.URL, "stale-token")
	if status != T3EnvironmentStatusRepair || msg == "" {
		t.Fatalf("stale probe = (%v, %q)", status, msg)
	}

	// 不可达：连接拒绝
	status, _ = client.probeSession(context.Background(), "http://127.0.0.1:1/", "token")
	if status != T3EnvironmentStatusUnreachable {
		t.Fatalf("unreachable probe = %v", status)
	}
}

func TestFetchThreadDetail_ProtocolAndMapping(t *testing.T) {
	projection := map[string]any{
		"thread": map[string]any{"id": "th-1", "projectId": "p-1", "title": "线程一"},
		"runs":   []any{},
		"turnItems": []any{
			map[string]any{"id": "i-1", "type": "user_message", "status": "completed", "text": "你好"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/orchestration/threads/th-1" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-t3-orchestration-protocol") != "2" {
			t.Errorf("missing orchestration protocol header")
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer good-token" {
			t.Errorf("authorization = %q", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"snapshotSequence": 7,
			"projection":       projection,
		})
	}))
	defer server.Close()

	client := newT3HTTPClient()
	raw, err := client.fetchThreadDetail(context.Background(), server.URL, "good-token", "th-1")
	if err != nil {
		t.Fatalf("fetchThreadDetail: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("projection decode: %v", err)
	}

	// 404 → errT3ThreadNotFound
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","reason":"thread_not_found","traceId":"t-4","message":"The environment could not find what this request named (thread_not_found)."}`))
	}))
	defer notFound.Close()
	if _, err := client.fetchThreadDetail(context.Background(), notFound.URL, "good-token", "th-x"); err == nil || err != errT3ThreadNotFound {
		t.Fatalf("expected errT3ThreadNotFound, got %v", err)
	}

	// 401 → errT3CredentialRejected
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-5"}`))
	}))
	defer unauthorized.Close()
	if _, err := client.fetchThreadDetail(context.Background(), unauthorized.URL, "stale", "th-x"); err == nil || err != errT3CredentialRejected {
		t.Fatalf("expected errT3CredentialRejected, got %v", err)
	}
}

func TestResolvePairedHTTPURL(t *testing.T) {
	base := "http://127.0.0.1:3773"
	got, err := resolvePairedHTTPURL(base, "/uploads/a?sig=1")
	if err != nil || got != "http://127.0.0.1:3773/uploads/a?sig=1" {
		t.Fatalf("relative path: %q %v", got, err)
	}
	same, err := resolvePairedHTTPURL(base, "http://127.0.0.1:3773/uploads/a")
	if err != nil || same != "http://127.0.0.1:3773/uploads/a" {
		t.Fatalf("same-origin absolute: %q %v", same, err)
	}
	// `@host` 经 ResolveReference 会落在配对 origin 的路径上，不再是拼接攻击。
	// 真正危险的是协议相对 URL、换 host 的绝对 URL、以及带 userinfo 的绝对 URL。
	for _, bad := range []string{
		"//evil.example/steal",
		"https://evil.example/steal",
		"http://127.0.0.1:3773@169.254.169.254/x",
		"",
	} {
		if _, err := resolvePairedHTTPURL(base, bad); err == nil {
			t.Fatalf("expected reject for %q", bad)
		}
	}
}

// TestFetchProjectSnapshot_ProtocolAndMapping 对照 contracts project.ts 的
// Project 形状（workspaceRoot 必填、deletedAt 软删、defaultModelSelection 可空）
// 与 environmentHttp.ts projects group：GET /api/projects、Bearer 认证、
// 不要求 orchestration protocol 头。
func TestFetchProjectSnapshot_ProtocolAndMapping(t *testing.T) {
	var gotHeader http.Header
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		_, _ = w.Write([]byte(`{
			"projects": [
				{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo/fast-ship", "defaultModelSelection": {"instanceId": "inst", "model": "claude-opus-5-5", "options": []}, "scripts": [], "createdAt": "2026-01-01T00:00:00.000Z", "updatedAt": "2026-01-02T00:00:00.000Z", "deletedAt": null},
				{"id": "p-2", "title": "已删除", "workspaceRoot": "/repo/gone", "defaultModelSelection": null, "scripts": [], "createdAt": "2026-01-01T00:00:00.000Z", "updatedAt": "2026-01-02T00:00:00.000Z", "deletedAt": "2026-01-03T00:00:00.000Z"},
				{"id": "p-3", "title": "无模型", "workspaceRoot": "/repo/plain", "scripts": [], "createdAt": "2026-01-01T00:00:00.000Z", "updatedAt": "2026-01-02T00:00:00.000Z", "deletedAt": null}
			],
			"updatedAt": "2026-01-04T00:00:00.000Z"
		}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	projects, err := client.fetchProjectSnapshot(context.Background(), server.URL, "good-token")
	if err != nil {
		t.Fatalf("fetchProjectSnapshot: %v", err)
	}
	if gotPath != "/api/projects" {
		t.Fatalf("path = %q", gotPath)
	}
	if auth := gotHeader.Get("Authorization"); auth != "Bearer good-token" {
		t.Fatalf("authorization = %q", auth)
	}
	// 该端点无需 orchestration protocol 头（environmentHttp.ts projects group
	// 只挂 OptionalBearerHeaders）。
	if gotHeader.Get(t3OrchestrationProtocolHdr) != "" {
		t.Fatalf("must not send orchestration protocol header")
	}
	// 软删项被过滤，映射到 shell 同构形状。
	if len(projects) != 2 {
		t.Fatalf("projects = %+v, want 2 (deleted filtered)", projects)
	}
	if projects[0].ID != "p-1" || projects[0].Title != "Fast Ship" {
		t.Fatalf("p-1 = %+v", projects[0])
	}
	if projects[0].WorkspaceRoot == nil || *projects[0].WorkspaceRoot != "/repo/fast-ship" {
		t.Fatalf("p-1 workspaceRoot = %+v", projects[0].WorkspaceRoot)
	}
	if projects[0].DefaultModelSelection == nil || projects[0].DefaultModelSelection.Model != "claude-opus-5-5" {
		t.Fatalf("p-1 modelSelection = %+v", projects[0].DefaultModelSelection)
	}
	if projects[1].ID != "p-3" || projects[1].DefaultModelSelection != nil {
		t.Fatalf("p-3 = %+v", projects[1])
	}

	// 401 → errT3CredentialRejected（凭证失效引导重新配对）。
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential"}`))
	}))
	defer unauthorized.Close()
	if _, err := client.fetchProjectSnapshot(context.Background(), unauthorized.URL, "stale"); err != errT3CredentialRejected {
		t.Fatalf("expected errT3CredentialRejected, got %v", err)
	}

	// 404 一律按上游错误（端点缺失=版本不兼容），不得产生 errT3ThreadNotFound。
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","reason":"route_not_found"}`))
	}))
	defer notFound.Close()
	_, err = client.fetchProjectSnapshot(context.Background(), notFound.URL, "tok")
	if err == nil || errors.Is(err, errT3ThreadNotFound) {
		t.Fatalf("404 must be a generic upstream error, got %v", err)
	}

	// 500 → 上游错误（带状态码）。
	serverError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"boom"}`))
	}))
	defer serverError.Close()
	_, err = client.fetchProjectSnapshot(context.Background(), serverError.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected upstream error with status, got %v", err)
	}

	// 坏 JSON → 上游错误。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer broken.Close()
	if _, err := client.fetchProjectSnapshot(context.Background(), broken.URL, "tok"); err == nil {
		t.Fatalf("expected decode error")
	}
}

// TestT3GrantedScopesAndOperate 判定逻辑（INT-85 契约）。
func TestT3GrantedScopesAndOperate(t *testing.T) {
	if got := t3GrantedScopes("orchestration:read orchestration:operate"); len(got) != 2 || got[0] != t3ScopeOrchestrationRead {
		t.Fatalf("granted = %+v", got)
	}
	if got := t3GrantedScopes(""); len(got) != 0 {
		t.Fatalf("empty grant = %+v", got)
	}
	// operate 判定唯一看精确 token；只读/legacy 宽串不含即 false
	if !t3HasOperateScope("orchestration:read orchestration:operate") {
		t.Fatalf("standard grant should carry operate")
	}
	if !t3HasOperateScope("orchestration:operate") {
		t.Fatalf("exact operate should pass")
	}
	for _, scope := range []string{"", "orchestration:read", "access:write", "orchestration:read-write"} {
		if t3HasOperateScope(scope) {
			t.Fatalf("scope %q must not imply operate", scope)
		}
	}
}

// TestDoJSON_SizeLimitExceeded 响应体达到上限 +1 即明确拒绝，
// 不把被截断的响应体当成功解析（panel r1 Grok10）。
func TestDoJSON_SizeLimitExceeded(t *testing.T) {
	payload := strings.Repeat("x", 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	client.maxBodyBytes = 64
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _, err = client.doJSON(req)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("expected explicit size-limit rejection, got %v", err)
	}

	// 恰好在限额内的响应照常返回。
	client.maxBodyBytes = int64(len(payload))
	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _, err := client.doJSON(req)
	if err != nil || string(body) != payload {
		t.Fatalf("expected full body within limit, got (%q, %v)", string(body), err)
	}
}
