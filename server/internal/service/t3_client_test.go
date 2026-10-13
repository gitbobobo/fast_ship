package service

import (
	"context"
	"encoding/json"
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
			"scope": "orchestration:read"
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
	// 真实请求字段：grant_type/subject_token/subject_token_type/requested_token_type/scope
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
	if gotForm["scope"] != "orchestration:read" {
		t.Fatalf("scope = %q", gotForm["scope"])
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

func TestExchangeBearer_RejectsWideScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"wide","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600,"scope":"orchestration:read orchestration:operate access:write"}`))
	}))
	defer server.Close()

	client := newT3HTTPClient()
	_, err := client.exchangeBearer(context.Background(), server.URL, "code")
	if err == nil || !strings.Contains(err.Error(), "超出") {
		t.Fatalf("expected wide scope rejection, got %v", err)
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

func TestT3ScopeExactlyRead(t *testing.T) {
	if !t3ScopeExactlyRead("orchestration:read") {
		t.Fatalf("exact scope should pass")
	}
	for _, scope := range []string{"", "orchestration:read orchestration:operate", "access:write", "orchestration:read "} {
		if t3ScopeExactlyRead(scope) {
			t.Fatalf("scope %q should fail", scope)
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
