package githubapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// Clients「部署级单身份、保存即生效」与检测映射（INT-129）定向测试。

// mustKeyPEM 生成一把测试用 RSA 私钥 PEM。
func mustKeyPEM(t *testing.T) []byte {
	t.Helper()
	pem, err := GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pem
}

// startAppServer 起一个只在 GET /app 上给固定回复的假 GitHub（Verify 永远打
// {apiBase}/app，所以按场景各起一个服务）。
func startAppServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestVerify_MapsOutcomes(t *testing.T) {
	key := mustKeyPEM(t)

	// 私钥解析失败 → invalid。
	if got := Verify(context.Background(), "123", []byte("not a pem"), ""); got.Status != DetectionInvalid || got.Detail != "私钥不是有效的 RSA PEM" {
		t.Fatalf("bad pem = %+v", got)
	}

	// 200 → ok，detail 取 name（缺 name 回落 slug），slug 原样带回。
	named := startAppServer(t, http.StatusOK, `{"name":"fast-ship","slug":"fast-ship-bot"}`)
	if got := Verify(context.Background(), "123", key, named.URL); got.Status != DetectionOK || got.Detail != "已连接 GitHub App：fast-ship" || got.Slug != "fast-ship-bot" {
		t.Fatalf("ok = %+v", got)
	}
	slugged := startAppServer(t, http.StatusOK, `{"name":"","slug":"fast-ship-bot"}`)
	if got := Verify(context.Background(), "123", key, slugged.URL); got.Status != DetectionOK || got.Detail != "已连接 GitHub App：fast-ship-bot" || got.Slug != "fast-ship-bot" {
		t.Fatalf("slug = %+v", got)
	}
	noSlug := startAppServer(t, http.StatusOK, `{"name":"fast-ship"}`)
	if got := Verify(context.Background(), "123", key, noSlug.URL); got.Slug != "" {
		t.Fatalf("missing slug = %+v, want empty Slug", got)
	}

	// 401 → invalid（凭证问题）；其他状态码 → failed。
	unauthorized := startAppServer(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`)
	if got := Verify(context.Background(), "123", key, unauthorized.URL); got.Status != DetectionInvalid || got.Detail != "GitHub 拒绝该 App 身份（401）" {
		t.Fatalf("401 = %+v", got)
	}
	broken := startAppServer(t, http.StatusInternalServerError, `{}`)
	if got := Verify(context.Background(), "123", key, broken.URL); got.Status != DetectionFailed || got.Detail != "检测失败（HTTP 500）" {
		t.Fatalf("500 = %+v", got)
	}

	// 网络错误 → failed。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if got := Verify(context.Background(), "123", key, deadURL); got.Status != DetectionFailed || got.Detail != "网络错误，未能连接 GitHub" {
		t.Fatalf("network = %+v", got)
	}
}

func TestClients_NotConfigured(t *testing.T) {
	c := NewClients(func(ctx context.Context) (RuntimeConfig, bool, error) {
		return RuntimeConfig{}, false, nil
	})
	_, err := c.ClientFor(context.Background())
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInternal.Code || appErr.Message != "GitHub App 未配置" {
		t.Fatalf("err = %v, want 50000 GitHub App 未配置", err)
	}

	// 来源报错（如解密失败）原样透传。
	want := errs.New(errs.ErrInternal.Code, "GitHub App 配置解密失败")
	c2 := NewClients(func(ctx context.Context) (RuntimeConfig, bool, error) {
		return RuntimeConfig{}, false, want
	})
	if _, err := c2.ClientFor(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want source error passthrough", err)
	}

	// 身份缺私钥同样视为未配置。
	c3 := NewClients(func(ctx context.Context) (RuntimeConfig, bool, error) {
		return RuntimeConfig{AppID: "111"}, true, nil
	})
	_, err = c3.ClientFor(context.Background())
	if !errors.As(err, &appErr) || appErr.Message != "GitHub App 未配置" {
		t.Fatalf("missing pem err = %v", err)
	}
}

// recordIssuerServer 记录每次安装令牌换新请求里 App JWT 的 iss（=App ID）。
func recordIssuerServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var issuers []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/app/installations/") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 1, "title": "T",
				"base": map[string]any{"repo": map[string]any{"full_name": "octocat/hw"}},
				"head": map[string]any{"sha": "s1"},
			})
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			t.Errorf("malformed app JWT %q", tok)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Errorf("decode payload: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var claims struct {
			Issuer string `json:"iss"`
		}
		_ = json.Unmarshal(raw, &claims)
		mu.Lock()
		issuers = append(issuers, claims.Issuer)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "inst-tok",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), issuers...)
	}
}

// TestClients_RebuildsOnFingerprintChange 部署换 App 身份（含只换私钥）后下一
// 次调用重建 client；配置不变时命中缓存不再换新令牌。
func TestClients_RebuildsOnFingerprintChange(t *testing.T) {
	key1 := mustKeyPEM(t)
	key2 := mustKeyPEM(t)

	ts, issuers := recordIssuerServer(t)

	current := RuntimeConfig{AppID: "111", PrivateKeyPEM: key1}
	c := NewClients(func(ctx context.Context) (RuntimeConfig, bool, error) {
		return current, current.AppID != "", nil
	})
	c.SetAPIBase(ts.URL)
	ctx := context.Background()

	// ClientFor 只建不调：真正发起请求才验证换新令牌的 issuer。
	call := func() {
		t.Helper()
		gh, err := c.ClientFor(ctx)
		if err != nil {
			t.Fatalf("ClientFor: %v", err)
		}
		if _, err := gh.GetPullRequest(ctx, 7, "octocat/hw", 1); err != nil {
			t.Fatalf("GetPullRequest: %v", err)
		}
	}

	call()
	// 换私钥（App ID 不变）：指纹变化，下一次调用自动重建，不带旧缓存。
	current = RuntimeConfig{AppID: "111", PrivateKeyPEM: key2}
	call()
	// 同一配置再调用：命中缓存，不再换新令牌。
	call()
	if got := issuers(); len(got) != 2 || got[0] != "111" || got[1] != "111" {
		t.Fatalf("issuers = %v, want [111 111] then cache hit", got)
	}

	// 换 App ID 同样触发重建。
	current = RuntimeConfig{AppID: "222", PrivateKeyPEM: key2}
	call()
	if got := issuers(); len(got) != 3 || got[2] != "222" {
		t.Fatalf("issuers after app change = %v", got)
	}
}

// TestStaticClients 恒返回同一 client 的解析器：fake 自测路径用。
func TestStaticClients(t *testing.T) {
	key := mustKeyPEM(t)
	want := NewClient(NewInstallationTokenStore(&AppCredentials{AppID: "1", PrivateKeyPEM: key}))
	got, err := StaticClients{Client: want}.ClientFor(context.Background())
	if err != nil || got != want {
		t.Fatalf("static = %v %v", got, err)
	}
}
