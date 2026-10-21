package githubapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// jwtIssuer 从 Authorization 头解析 App JWT 的 iss（对账端点必须用 App JWT，
// 不是安装令牌）。
func jwtIssuer(t *testing.T, auth string) string {
	t.Helper()
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization = %q, want Bearer 前缀的 App JWT", auth)
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("token 形状不对：%d 段", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("解码 JWT payload: %v", err)
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("解析 JWT claims: %v", err)
	}
	return claims.Issuer
}

func TestListHookDeliveries_PaginatesAndStopsBeforeSince(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var issuers []string
	requests := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		issuers = append(issuers, jwtIssuer(t, r.Header.Get("Authorization")))
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			// 第一页整页都在窗口内，带 next 让客户端继续翻。
			w.Header().Set("Link",
				`<`+ts.URL+`/app/hook/deliveries?per_page=100&cursor=xxx>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "guid": "g1", "event": "pull_request", "action": "synchronize",
					"status_code": 502, "redelivery": false, "delivered_at": since.Add(2 * time.Hour).Format(time.RFC3339)},
				{"id": 2, "guid": "g2", "event": "pull_request", "action": "opened",
					"status_code": 200, "redelivery": false, "delivered_at": since.Add(1 * time.Hour).Format(time.RFC3339)},
				{"id": 3, "guid": "g3", "event": "pull_request", "action": "synchronize",
					"status_code": 200, "redelivery": false, "delivered_at": since.Format(time.RFC3339)},
			})
		case 2:
			// 第二页整页早于 since：应停在这里，不再发第三次请求。
			w.Header().Set("Link",
				`<`+ts.URL+`/app/hook/deliveries?per_page=100&cursor=yyy>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 4, "guid": "g4", "event": "pull_request", "action": "opened",
					"status_code": 500, "redelivery": false, "delivered_at": since.Add(-time.Second).Format(time.RFC3339)},
				{"id": 5, "guid": "g5", "event": "pull_request", "action": "opened",
					"status_code": 200, "redelivery": true, "delivered_at": since.Add(-2 * time.Hour).Format(time.RFC3339)},
			})
		default:
			t.Errorf("不应发第三次请求，实际第 %d 次", requests)
		}
	}))
	defer ts.Close()

	c := NewClient(NewInstallationTokenStore(&AppCredentials{
		AppID: "42", PrivateKeyPEM: mustKeyPEM(t), APIBaseURL: ts.URL,
	}))
	got, err := c.ListHookDeliveries(context.Background(), since)
	if err != nil {
		t.Fatalf("ListHookDeliveries: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	for i, iss := range issuers {
		if iss != "42" {
			t.Fatalf("第 %d 次请求 JWT iss = %q, want 42", i+1, iss)
		}
	}
	if len(got) != 3 {
		t.Fatalf("collected = %d, want 3（窗口内整页）", len(got))
	}
	if got[0].ID != 1 || got[0].GUID != "g1" || got[0].Event != "pull_request" ||
		got[0].Action != "synchronize" || got[0].StatusCode != 502 || got[0].Redelivery {
		t.Fatalf("第 1 行解析不对: %+v", got[0])
	}
	if got[0].Delivered() || !got[1].Delivered() || !got[2].Delivered() {
		t.Fatalf("Delivered 判定不对: %+v", got)
	}
}

func TestRedeliverHookDelivery(t *testing.T) {
	var method, path string
	code := http.StatusAccepted
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if iss := jwtIssuer(t, r.Header.Get("Authorization")); iss != "42" {
			t.Errorf("JWT iss = %q, want 42", iss)
		}
		w.WriteHeader(code)
	}))
	defer ts.Close()

	c := NewClient(NewInstallationTokenStore(&AppCredentials{
		AppID: "42", PrivateKeyPEM: mustKeyPEM(t), APIBaseURL: ts.URL,
	}))
	if err := c.RedeliverHookDelivery(context.Background(), 123); err != nil {
		t.Fatalf("RedeliverHookDelivery: %v", err)
	}
	if method != http.MethodPost || path != "/app/hook/deliveries/123/attempts" {
		t.Fatalf("request = %s %s, want POST /app/hook/deliveries/123/attempts", method, path)
	}

	code = http.StatusInternalServerError
	err := c.RedeliverHookDelivery(context.Background(), 123)
	var statusErr *githubapi.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 500 {
		t.Fatalf("err = %v, want *githubapi.StatusError{500}", err)
	}
}

// TestListHookDeliveries_TruncatedAtPageCap 服务端每页都返回窗口内数据且永远
// 带 next：翻到页数上限就该停，并返回 ErrDeliveriesTruncated——账本不完整，
// 调用方（对账）按错误放弃本轮，而不是拿半本账接着算。
func TestListHookDeliveries_TruncatedAtPageCap(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link",
			`<`+ts.URL+`/app/hook/deliveries?per_page=100&cursor=`+fmt.Sprint(requests)+`; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": requests, "guid": fmt.Sprintf("g%d", requests), "event": "pull_request",
				"action": "synchronize", "status_code": 500, "redelivery": false,
				"delivered_at": since.Add(time.Duration(requests) * time.Minute).Format(time.RFC3339)},
		})
	}))
	defer ts.Close()

	c := NewClient(NewInstallationTokenStore(&AppCredentials{
		AppID: "42", PrivateKeyPEM: mustKeyPEM(t), APIBaseURL: ts.URL,
	}))
	got, err := c.ListHookDeliveries(context.Background(), since)
	if !errors.Is(err, ErrDeliveriesTruncated) {
		t.Fatalf("err = %v, want ErrDeliveriesTruncated", err)
	}
	if requests != hookDeliveryMaxPages {
		t.Fatalf("requests = %d, want %d", requests, hookDeliveryMaxPages)
	}
	if len(got) != hookDeliveryMaxPages {
		t.Fatalf("collected = %d, want %d（每页 1 行）", len(got), hookDeliveryMaxPages)
	}
}

// TestListHookDeliveries_CrossOriginNextIgnored Link 指向别的站点：不能把
// 显式设置的 App JWT 跟过去——当作没有下一页，正常返回已收集的行，不报错。
func TestListHookDeliveries_CrossOriginNextIgnored(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Header().Set("Link", `<http://evil.invalid/app/hook/deliveries?per_page=100&cursor=evil>; rel="next"`)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 11, "guid": "g1", "event": "pull_request", "action": "opened",
				"status_code": 500, "redelivery": false,
				"delivered_at": since.Add(time.Hour).Format(time.RFC3339)},
		})
	}))
	defer ts.Close()

	c := NewClient(NewInstallationTokenStore(&AppCredentials{
		AppID: "42", PrivateKeyPEM: mustKeyPEM(t), APIBaseURL: ts.URL,
	}))
	got, err := c.ListHookDeliveries(context.Background(), since)
	if err != nil {
		t.Fatalf("异源 next 应静默忽略，实际报错: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1（不跟随异源 Link）", requests)
	}
	if len(got) != 1 || got[0].ID != 11 {
		t.Fatalf("collected = %+v, want 第一页的 1 行", got)
	}
}
