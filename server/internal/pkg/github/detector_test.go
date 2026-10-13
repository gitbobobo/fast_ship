package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestDetector(t *testing.T, handler http.HandlerFunc) (*TokenDetector, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewTokenDetectorWithClient(server.URL, server.Client()), server
}

func TestTokenDetector_OKClassicWithScopes(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_classic" {
			t.Errorf("expected bearer token header, got %q", got)
		}
		w.Header().Set("X-OAuth-Scopes", "repo, read:org, ")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"login":"octocat"}`))
	})

	got := detector.Detect(context.Background(), "ghp_classic")
	if got.Status != DetectStatusOK || got.Login != "octocat" || got.TokenType != DetectTypeClassic {
		t.Fatalf("unexpected detection: %+v", got)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "repo" || got.Scopes[1] != "read:org" {
		t.Fatalf("unexpected scopes: %v", got.Scopes)
	}
}

func TestTokenDetector_OKFineGrainedEmptyScopes(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"login":"octocat"}`))
	})

	got := detector.Detect(context.Background(), "github_pat_fine")
	if got.Status != DetectStatusOK || got.TokenType != DetectTypeFineGrained {
		t.Fatalf("unexpected detection: %+v", got)
	}
	if got.Detail != fineGrainedDetail || len(got.Scopes) != 0 {
		t.Fatalf("expected fine-grained detail without scopes, got %+v", got)
	}
}

func TestTokenDetector_OKUnknownPrefixEmptyScopes(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"login":"app-bot"}`))
	})

	got := detector.Detect(context.Background(), "gho_unknown_prefix")
	if got.Status != DetectStatusOK || got.TokenType != DetectTypeUnknown || got.Detail != "" {
		t.Fatalf("unexpected detection: %+v", got)
	}
}

func TestTokenDetector_InvalidOn401(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	})

	got := detector.Detect(context.Background(), "ghp_dead")
	if got.Status != DetectStatusInvalid || got.Login != "" {
		t.Fatalf("expected invalid, got %+v", got)
	}
}

func TestTokenDetector_ServerErrorIsFailedNotInvalid(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	got := detector.Detect(context.Background(), "ghp_ok")
	if got.Status != DetectStatusFailed {
		t.Fatalf("5xx must be failed, got %+v", got)
	}
	if got.Detail != detectFailedDetail || strings.Contains(got.Detail, "ghp_ok") {
		t.Fatalf("detail must be the fixed message and not leak token, got %q", got.Detail)
	}
	if got.Scopes == nil {
		t.Fatal("scopes must be empty slice, not nil")
	}
}

func TestTokenDetector_NetworkErrorIsFailedNotInvalid(t *testing.T) {
	// 指向已关闭端口的检测器：连接被拒属于网络失败，不得判成凭证无效。
	detector := NewTokenDetectorWithClient("http://127.0.0.1:1/", &http.Client{Timeout: 2e9})

	got := detector.Detect(context.Background(), "ghp_netfail")
	if got.Status != DetectStatusFailed {
		t.Fatalf("network failure must be failed, got %+v", got)
	}
}

func TestTokenDetector_RateLimitIsFailed(t *testing.T) {
	detector, _ := newTestDetector(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})

	got := detector.Detect(context.Background(), "ghp_limited")
	if got.Status != DetectStatusFailed {
		t.Fatalf("rate limit must be failed (not invalid), got %+v", got)
	}
}
