package quota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- 测试基础设施 ----

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("读取 fixture %s: %v", name, err)
	}
	return data
}

// newTestServer 启动一个 httptest 服务，handler 记录请求并返回响应。
func newTestServer(t *testing.T, handler func(r *http.Request) (int, []byte)) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	requests := []*http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0)
		if handler != nil {
			status, responseBody := handler(r)
			body = responseBody
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
		requests = append(requests, r)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func requestURLs(requests *[]*http.Request) []string {
	urls := make([]string, 0, len(*requests))
	for _, r := range *requests {
		urls = append(urls, r.URL.Path+"?"+r.URL.RawQuery)
	}
	return urls
}

func fetchCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func requireCredentialError(t *testing.T, err error, statusCode int) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望凭据错误，得到 nil")
	}
	fe, ok := err.(*FetchError)
	if !ok {
		t.Fatalf("期望 *FetchError，得到 %T: %v", err, err)
	}
	if fe.Kind != ErrKindCredential {
		t.Fatalf("期望 credential_invalid，得到 %s: %s", fe.Kind, fe.Message)
	}
	if statusCode != 0 && fe.StatusCode != statusCode {
		t.Fatalf("期望状态码 %d，得到 %d", statusCode, fe.StatusCode)
	}
}

func requireProviderError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望接口错误，得到 nil")
	}
	fe, ok := err.(*FetchError)
	if !ok {
		t.Fatalf("期望 *FetchError，得到 %T: %v", err, err)
	}
	if fe.Kind != ErrKindProvider {
		t.Fatalf("期望 provider_error，得到 %s: %s", fe.Kind, fe.Message)
	}
}

func findWindow(t *testing.T, windows []Window, kind, label string) Window {
	t.Helper()
	for _, w := range windows {
		if w.Kind == kind && w.Label == label {
			return w
		}
	}
	t.Fatalf("找不到窗口 kind=%s label=%s，实际 %+v", kind, label, windows)
	return Window{}
}

func expectHeader(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.Header.Get(key); got != want {
		t.Fatalf("请求头 %s 期望 %q，得到 %q", key, want, got)
	}
}

func unixTime(seconds int64) *time.Time {
	t := time.Unix(seconds, 0).UTC()
	return &t
}

// ---- Command Code ----

func TestCommandCodeParsesCreditsAndSubscription(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return http.StatusOK, readFixture(t, "commandcode_credits_root.json")
		case "/alpha/billing/subscriptions":
			return http.StatusOK, readFixture(t, "commandcode_subscription.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 2 ||
		got[0] != "/alpha/billing/credits?" || got[1] != "/alpha/billing/subscriptions?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	for _, r := range *requests {
		expectHeader(t, r, "Authorization", "Bearer user_test")
		expectHeader(t, r, "Accept", "application/json")
	}

	if snapshot.PlanName != "Go" {
		t.Fatalf("套餐名期望 Go，得到 %q", snapshot.PlanName)
	}

	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 25 {
		t.Fatalf("5 小时窗口期望 25%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(*unixTime(1_780_000_000)) {
		t.Fatalf("5 小时重置时间不符: %v", fiveHour.ResetAt)
	}

	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 10 {
		t.Fatalf("每周窗口期望 10%%，得到 %v", weekly.UsedPercent)
	}
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(*unixTime(1_780_100_000)) {
		t.Fatalf("每周重置时间不符: %v", weekly.ResetAt)
	}

	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.Unit != UnitUSD {
		t.Fatalf("月度窗口单位期望 usd，得到 %s", monthly.Unit)
	}
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 15 {
		t.Fatalf("月度窗口期望 15%%（(10-8.5)/10），得到 %v", monthly.UsedPercent)
	}
	if monthly.Used == nil || *monthly.Used != 1.5 || monthly.Limit == nil || *monthly.Limit != 10 {
		t.Fatalf("月度窗口 used/limit 不符: %v/%v", monthly.Used, monthly.Limit)
	}
	if monthly.ResetAt == nil || !monthly.ResetAt.Equal(time.Date(2026, 6, 6, 7, 28, 50, 0, time.UTC)) {
		t.Fatalf("月度重置时间不符: %v", monthly.ResetAt)
	}

	if snapshot.Balance != nil {
		t.Fatalf(" purchasedCredits=0 时不应有余额: %+v", snapshot.Balance)
	}
}

func TestCommandCodeNestedWindowLimitsAndBalance(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return http.StatusOK, readFixture(t, "commandcode_credits_nested.json")
		case "/alpha/billing/subscriptions":
			return http.StatusOK, readFixture(t, "commandcode_subscription.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 25 {
		t.Fatalf("5 小时窗口期望 25%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(*unixTime(1_780_200_000)) {
		t.Fatalf("5 小时重置时间（秒级字符串）不符: %v", fiveHour.ResetAt)
	}
	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 27.5 {
		t.Fatalf("月度窗口期望 27.5%%，得到 %v", monthly.UsedPercent)
	}
}

func TestCommandCodeGrantedOverridesPlanGrant(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return http.StatusOK, readFixture(t, "commandcode_credits_granted.json")
		case "/alpha/billing/subscriptions":
			return http.StatusOK, []byte(`{"success":true,"data":null}`)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if snapshot.PlanName != "" {
		t.Fatalf("免费档套餐名应为空，得到 %q", snapshot.PlanName)
	}
	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 64.3 {
		t.Fatalf("月度窗口期望 64.3%%（(70-25)/70），得到 %v", monthly.UsedPercent)
	}
	if monthly.Used == nil || *monthly.Used != 45 || monthly.Limit == nil || *monthly.Limit != 70 {
		t.Fatalf("月度窗口 used/limit 期望 45/70，得到 %v/%v", monthly.Used, monthly.Limit)
	}
	if snapshot.Balance == nil || snapshot.Balance.Amount != 4.5 {
		t.Fatalf("充值余额期望 4.5，得到 %+v", snapshot.Balance)
	}
}

func TestCommandCodeSubscriptionFailureKeepsCredits(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return http.StatusOK, readFixture(t, "commandcode_credits_basic.json")
		case "/alpha/billing/subscriptions":
			return http.StatusInternalServerError, []byte(`{"success":false}`)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
	if err != nil {
		t.Fatalf("订阅查询失败不应影响 credits: %v", err)
	}
	if snapshot.PlanName != "" {
		t.Fatalf("订阅不可用时套餐名应为空，得到 %q", snapshot.PlanName)
	}
	if len(snapshot.Windows) != 0 {
		t.Fatalf("无 windowLimits 且无 grant 时不应有窗口: %+v", snapshot.Windows)
	}
}

func TestCommandCodeUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{"detail":"bad key"}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_bad"})
	requireCredentialError(t, err, http.StatusUnauthorized)
}

func TestCommandCodeMalformedResponses(t *testing.T) {
	cases := map[string]func(r *http.Request) (int, []byte){
		"非法 JSON": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`not json`)
		},
		"缺 credits 对象": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"data":{}}`)
		},
		"缺 monthlyCredits": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"credits":{}}`)
		},
		"5xx": func(r *http.Request) (int, []byte) {
			return http.StatusBadGateway, []byte(`{}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, handler)
			ctx, cancel := fetchCtx()
			defer cancel()
			_, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
			requireProviderError(t, err)
		})
	}
}

// ---- OpenCode Go ----

func TestOpenCodeGoParsesUsage(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.URL.Path == "/zen/go/v1/usage" {
			return http.StatusOK, readFixture(t, "opencodego_usage.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 1 || got[0] != "/zen/go/v1/usage?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	expectHeader(t, (*requests)[0], "Authorization", "Bearer go_secret")
	expectHeader(t, (*requests)[0], "Accept", "application/json")

	rolling := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if rolling.UsedPercent == nil || *rolling.UsedPercent != 22 {
		t.Fatalf("rolling 期望 22%%，得到 %v", rolling.UsedPercent)
	}
	if rolling.ResetAt == nil || !rolling.ResetAt.Equal(time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("rolling 重置时间不符: %v", rolling.ResetAt)
	}

	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 8 {
		t.Fatalf("weekly 期望 8%%，得到 %v", weekly.UsedPercent)
	}

	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 3 {
		t.Fatalf("monthly 期望 3%%，得到 %v", monthly.UsedPercent)
	}
	if monthly.ResetAt == nil || !monthly.ResetAt.Equal(*unixTime(1_788_326_400)) {
		t.Fatalf("monthly 重置时间（秒级数字）不符: %v", monthly.ResetAt)
	}
}

func TestOpenCodeGoRelativeResetUsesNow(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"usage":{"rolling":{"percent":10,"resetInSec":3600}}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	before := time.Now().UTC()
	snapshot, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	rolling := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if rolling.ResetAt == nil {
		t.Fatalf("resetInSec 应换算成绝对时间")
	}
	expected := before.Add(time.Hour)
	if diff := rolling.ResetAt.Sub(expected); diff < -30*time.Second || diff > 30*time.Second {
		t.Fatalf("重置时间应约为 now+1h，得到 %v（基准 %v）", rolling.ResetAt, expected)
	}
}

func TestOpenCodeGoMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"非法 JSON":     `not json`,
		"缺 usage":     `{"data":{}}`,
		"缺 rolling":   `{"usage":{"weekly":{"percent":1}}}`,
		"weekly 无百分比": `{"usage":{"rolling":{"percent":1},"weekly":{"nope":true}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()
			_, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
			requireProviderError(t, err)
		})
	}
}

func TestOpenCodeGoUnauthorizedAndServerError(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
	requireCredentialError(t, err, http.StatusUnauthorized)

	server2, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusServiceUnavailable, []byte(`{}`)
	})
	_, err = fetchOpenCodeGo(ctx, server2.Client(), server2.URL, Credentials{"api_key": "go_secret"})
	requireProviderError(t, err)
}

// ---- 智谱 GLM ----

func TestZaiParsesQuotaLimits(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.URL.Path == "/api/monitor/usage/quota/limit" {
			return http.StatusOK, readFixture(t, "zai_quota.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 1 || got[0] != "/api/monitor/usage/quota/limit?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	expectHeader(t, (*requests)[0], "Authorization", "Bearer zl_key")
	expectHeader(t, (*requests)[0], "Accept", "application/json")

	if snapshot.PlanName != "Pro" {
		t.Fatalf("套餐名期望 Pro，得到 %q", snapshot.PlanName)
	}

	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 25 {
		t.Fatalf("5 小时窗口期望 25%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(*unixTime(1_785_816_000)) {
		t.Fatalf("5 小时重置时间（毫秒时间戳）不符: %v", fiveHour.ResetAt)
	}

	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 9 {
		t.Fatalf("每周窗口期望 9%%，得到 %v", weekly.UsedPercent)
	}

	mcp := findWindow(t, snapshot.Windows, WindowOther, "MCP 次数")
	if mcp.UsedPercent == nil || *mcp.UsedPercent != 22.4 {
		t.Fatalf("MCP 窗口期望 22.4%%（224/1000），得到 %v", mcp.UsedPercent)
	}
	if mcp.Unit != UnitCount {
		t.Fatalf("MCP 窗口单位期望 count，得到 %s", mcp.Unit)
	}
	if mcp.Used == nil || *mcp.Used != 224 || mcp.Limit == nil || *mcp.Limit != 1000 {
		t.Fatalf("MCP 窗口 used/limit 期望 224/1000，得到 %v/%v", mcp.Used, mcp.Limit)
	}
}

func TestZaiParsesCreditLimits(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, readFixture(t, "zai_quota_credit.json")
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if snapshot.PlanName != "lite" {
		t.Fatalf("套餐名期望 lite，得到 %q", snapshot.PlanName)
	}
	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 5 {
		t.Fatalf("5 小时窗口期望 5%%（100/2000），得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(time.UnixMilli(1_786_073_946_574).UTC()) {
		t.Fatalf("5 小时重置时间不符: %v", fiveHour.ResetAt)
	}
	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 10 {
		t.Fatalf("每周窗口期望 10%%，得到 %v", weekly.UsedPercent)
	}
}

func TestZaiSkipsUnknownLimits(t *testing.T) {
	body := `{"code":200,"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":25},
		{"type":"FUTURE_POINTS_POOL","pointsRemaining":800}
	]}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(body)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if len(snapshot.Windows) != 1 {
		t.Fatalf("未知类型应被跳过，实际窗口: %+v", snapshot.Windows)
	}
}

func TestZaiMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"非法 JSON":    `not json`,
		"信封失败":       `{"code":401,"success":false,"data":{}}`,
		"缺 data":     `{"code":200,"success":true}`,
		"缺 limits":   `{"code":200,"success":true,"data":{}}`,
		"无可识别窗口":     `{"code":200,"success":true,"data":{"limits":[{"type":"FUTURE","percentage":1}]}}`,
		"条目缺百分比":     `{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5}]}}`,
		"30 天窗口按月处理": `{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":1,"number":30,"percentage":50,"nextResetTime":1787112000000}]}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()
			snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
			if name == "30 天窗口按月处理" {
				if err != nil {
					t.Fatalf("取数失败: %v", err)
				}
				monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
				if monthly.UsedPercent == nil || *monthly.UsedPercent != 50 {
					t.Fatalf("30 天窗口期望按月度处理 50%%，得到 %v", monthly.UsedPercent)
				}
				return
			}
			requireProviderError(t, err)
		})
	}
}

func TestZaiUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusForbidden, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
	requireCredentialError(t, err, http.StatusForbidden)
}

// ---- Grok ----

func TestGrokParsesWeeklyCredits(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.URL.Path == "/v1/billing" && r.URL.Query().Get("format") == "credits" {
			return http.StatusOK, readFixture(t, "grok_credits.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 1 || got[0] != "/v1/billing?format=credits" {
		t.Fatalf("请求路径不符: %v", got)
	}
	expectHeader(t, (*requests)[0], "Authorization", "Bearer token-123")
	expectHeader(t, (*requests)[0], "x-xai-token-auth", "xai-grok-cli")
	expectHeader(t, (*requests)[0], "Accept", "application/json")

	if snapshot.PlanName != "SuperGrok Heavy" {
		t.Fatalf("套餐名期望 SuperGrok Heavy，得到 %q", snapshot.PlanName)
	}
	window := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if window.UsedPercent == nil || *window.UsedPercent != 12.5 {
		t.Fatalf("用量期望 12.5%%，得到 %v", window.UsedPercent)
	}
	if window.ResetAt == nil || !window.ResetAt.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("重置时间不符: %v", window.ResetAt)
	}
	if snapshot.Balance == nil || snapshot.Balance.Amount != 15 {
		t.Fatalf("预付余额期望 15（1500 美分），得到 %+v", snapshot.Balance)
	}
}

func TestGrokDerivesPercentFromOnDemand(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"config":{"onDemandCap":{"val":1000.0},"onDemandUsed":{"val":250.0}}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if len(snapshot.Windows) != 1 {
		t.Fatalf("期望一个窗口，得到 %+v", snapshot.Windows)
	}
	window := snapshot.Windows[0]
	if window.Kind != WindowOther || window.Label != "账单周期" {
		t.Fatalf("无周期信息时窗口应为 other/账单周期，得到 %s/%s", window.Kind, window.Label)
	}
	if window.UsedPercent == nil || *window.UsedPercent != 25 {
		t.Fatalf("用量期望 25%%（250/1000），得到 %v", window.UsedPercent)
	}
}

func TestGrokClampsOutOfRangePercent(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"config":{"creditUsagePercent":104.2,"billingPeriodEnd":"2026-08-13T00:00:00Z"}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if snapshot.Windows[0].UsedPercent == nil || *snapshot.Windows[0].UsedPercent != 100 {
		t.Fatalf("越界百分比应夹到 100，得到 %v", snapshot.Windows[0].UsedPercent)
	}
}

func TestGrokMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"非法 JSON":  `not json`,
		"缺 config": `{"data":{}}`,
		"纯套餐无用量":   `{"config":{"onDemandCap":{"val":0}},"subscriptionTier":"supergrok_heavy"}`,
		"5xx":      `{"config":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			status := http.StatusOK
			if name == "5xx" {
				status = http.StatusInternalServerError
			}
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return status, []byte(body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()
			_, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
			requireProviderError(t, err)
		})
	}
}

func TestGrokUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte("unauthorized")
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
	requireCredentialError(t, err, http.StatusUnauthorized)
}

// ---- Codex ----

func TestCodexParsesUsage(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.URL.Path == "/backend-api/wham/usage" {
			return http.StatusOK, readFixture(t, "codex_usage.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{
		"access_token": "oauth-token",
		"account_id":   "account-123",
	})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 1 || got[0] != "/backend-api/wham/usage?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	expectHeader(t, (*requests)[0], "Authorization", "Bearer oauth-token")
	expectHeader(t, (*requests)[0], "ChatGPT-Account-Id", "account-123")
	expectHeader(t, (*requests)[0], "Accept", "application/json")

	if snapshot.PlanName != "pro" {
		t.Fatalf("套餐名期望 pro，得到 %q", snapshot.PlanName)
	}

	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 22 {
		t.Fatalf("5 小时窗口期望 22%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil || !fiveHour.ResetAt.Equal(*unixTime(1_766_948_068)) {
		t.Fatalf("5 小时重置时间不符: %v", fiveHour.ResetAt)
	}

	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 43 {
		t.Fatalf("每周窗口期望 43%%，得到 %v", weekly.UsedPercent)
	}
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(*unixTime(1_767_407_914)) {
		t.Fatalf("每周重置时间不符: %v", weekly.ResetAt)
	}

	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "月度额度")
	if monthly.Unit != UnitUSD {
		t.Fatalf("月度额度单位期望 usd，得到 %s", monthly.Unit)
	}
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 41.7 {
		t.Fatalf("月度额度期望 41.7%%（12.5/30），得到 %v", monthly.UsedPercent)
	}
	if monthly.Used == nil || *monthly.Used != 12.5 || monthly.Limit == nil || *monthly.Limit != 30 {
		t.Fatalf("月度额度 used/limit 期望 12.5/30，得到 %v/%v", monthly.Used, monthly.Limit)
	}
	if monthly.ResetAt == nil || !monthly.ResetAt.Equal(*unixTime(1_769_875_200)) {
		t.Fatalf("月度额度重置时间不符: %v", monthly.ResetAt)
	}

	if snapshot.Balance == nil || snapshot.Balance.Amount != 4.3 {
		t.Fatalf("余额期望 4.3（字符串数字，四舍五入 1 位），得到 %+v", snapshot.Balance)
	}
}

func TestCodexNormalizesSwappedWindows(t *testing.T) {
	body := `{"rate_limit":{
		"primary_window":{"used_percent":43,"reset_at":1767407914,"limit_window_seconds":604800},
		"secondary_window":{"used_percent":22,"reset_at":1766948068,"limit_window_seconds":18000}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(body)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if snapshot.Windows[0].Kind != WindowFiveHour || snapshot.Windows[1].Kind != WindowWeekly {
		t.Fatalf("主次窗口应按 5 小时/每周归位: %+v", snapshot.Windows)
	}
}

func TestCodexAccountIDHeaderOmitted(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"reset_at":1766948068,"limit_window_seconds":18000}}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	if _, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"}); err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if got := (*requests)[0].Header.Get("ChatGPT-Account-Id"); got != "" {
		t.Fatalf("未提供 account_id 时不应带头，得到 %q", got)
	}
}

func TestCodexMalformedResponses(t *testing.T) {
	cases := map[string]func(r *http.Request) (int, []byte){
		"非法 JSON": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`not json`)
		},
		"无用量无余额": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"plan_type":"plus"}`)
		},
		"主窗口缺百分比": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"rate_limit":{"primary_window":{"reset_at":1}}}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, handler)
			ctx, cancel := fetchCtx()
			defer cancel()
			_, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
			requireProviderError(t, err)
		})
	}
}

func TestCodexUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
	requireCredentialError(t, err, http.StatusUnauthorized)
}

// ---- Devin ----

func TestDevinParsesCurrentQuotaResponse(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.URL.Path == "/api/org-123/billing/quota/usage" {
			return http.StatusOK, readFixture(t, "devin_usage.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{
		"token":  "Bearer sess-token",
		"org_id": "org-123",
	})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 1 || got[0] != "/api/org-123/billing/quota/usage?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	expectHeader(t, (*requests)[0], "Authorization", "Bearer sess-token")
	expectHeader(t, (*requests)[0], "x-cog-org-id", "org-123")
	expectHeader(t, (*requests)[0], "Accept", "application/json")

	if snapshot.PlanName != "Pro" {
		t.Fatalf("套餐名期望 Pro，得到 %q", snapshot.PlanName)
	}

	daily := findWindow(t, snapshot.Windows, WindowDaily, "每日")
	if daily.UsedPercent == nil || *daily.UsedPercent != 12 {
		t.Fatalf("每日窗口期望 12%%（0.12 小数），得到 %v", daily.UsedPercent)
	}
	// 2026-06-11T00:00:00-08:00 → 2026-06-11T08:00:00Z。
	if daily.ResetAt == nil || !daily.ResetAt.Equal(*unixTime(1_781_164_800)) {
		t.Fatalf("每日重置时间（带时区字符串）不符: %v", daily.ResetAt)
	}

	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 42 {
		t.Fatalf("每周窗口期望 42%%，得到 %v", weekly.UsedPercent)
	}
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(*unixTime(1_780_560_000)) {
		t.Fatalf("每周重置时间不符: %v", weekly.ResetAt)
	}

	if snapshot.Balance == nil || snapshot.Balance.Amount != 70.9 {
		t.Fatalf("超额余额期望 70.9（美分换算，四舍五入 1 位），得到 %+v", snapshot.Balance)
	}
}

func TestDevinParsesNestedLegacyResponse(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		// 第一个候选路径 404，第二个候选（slug 形式）成功，验证逐路径回退。
		if r.URL.Path == "/api/org/my-team/billing/quota/usage" {
			return http.StatusNotFound, []byte(`{}`)
		}
		if r.URL.Path == "/api/my-team/billing/quota/usage" {
			return http.StatusOK, readFixture(t, "devin_usage_nested.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{
		"token":  "sess-token",
		"org_id": "my-team",
	})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if got := requestURLs(requests); len(got) != 2 {
		t.Fatalf("应先尝试 org/ 前缀再尝试 slug 形式: %v", got)
	}

	daily := findWindow(t, snapshot.Windows, WindowDaily, "每日")
	if daily.UsedPercent == nil || *daily.UsedPercent != 30 {
		t.Fatalf("每日窗口期望 30%%（used 3 / limit 10），得到 %v", daily.UsedPercent)
	}
	if daily.ResetAt == nil || !daily.ResetAt.Equal(time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("每日重置时间不符: %v", daily.ResetAt)
	}
	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 75 {
		t.Fatalf("每周窗口期望 75%%（remaining 0.25），得到 %v", weekly.UsedPercent)
	}
}

func TestDevinUnauthorizedAndMissingOrg(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{"detail":"No organizations found for auth1 user"}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{"token": "tok", "org_id": "org-123"})
	requireCredentialError(t, err, http.StatusUnauthorized)
	if !strings.Contains(err.Error(), "组织") {
		t.Fatalf("组织无效时应给出组织相关提示: %v", err)
	}

	server2, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{}`)
	})
	_, err = fetchDevin(ctx, server2.Client(), server2.URL, Credentials{"token": "tok", "org_id": "org-123"})
	requireCredentialError(t, err, http.StatusUnauthorized)
}

func TestDevinMalformedAndServerError(t *testing.T) {
	// 所有候选路径都 5xx → provider_error。
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusInternalServerError, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{"token": "tok", "org_id": "org-123"})
	requireProviderError(t, err)

	// 200 但缺窗口 → provider_error。
	server2, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"plan_name":"pro"}`)
	})
	_, err = fetchDevin(ctx, server2.Client(), server2.URL, Credentials{"token": "tok", "org_id": "org-123"})
	requireProviderError(t, err)
}

// ---- Factory ----

func TestFactoryTokenRateLimitsPath(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, readFixture(t, "factory_auth_me.json")
		case "/api/billing/limits":
			return http.StatusOK, readFixture(t, "factory_billing_limits.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-test-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	if got := requestURLs(requests); len(got) != 2 ||
		got[0] != "/api/app/auth/me?" || got[1] != "/api/billing/limits?" {
		t.Fatalf("请求路径不符: %v", got)
	}
	for _, r := range *requests {
		expectHeader(t, r, "Authorization", "Bearer fk-test-key")
		expectHeader(t, r, "x-factory-client", "web-app")
	}

	if snapshot.PlanName != "Team" {
		t.Fatalf("套餐名期望 Team，得到 %q", snapshot.PlanName)
	}

	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 12 {
		t.Fatalf("5 小时窗口期望 12%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt == nil {
		t.Fatalf("secondsRemaining 应换算成重置时间")
	}
	if diff := time.Until(*fiveHour.ResetAt); diff < 55*time.Minute || diff > 65*time.Minute {
		t.Fatalf("5 小时窗口重置应约为 now+1h，得到 %v", fiveHour.ResetAt)
	}
	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.UsedPercent == nil || *weekly.UsedPercent != 34 {
		t.Fatalf("每周窗口期望 34%%，得到 %v", weekly.UsedPercent)
	}
	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 56 {
		t.Fatalf("每月窗口期望 56%%，得到 %v", monthly.UsedPercent)
	}
	if snapshot.Balance == nil || snapshot.Balance.Amount != 5 {
		t.Fatalf("Extra 余额期望 5（500 美分），得到 %+v", snapshot.Balance)
	}
}

func TestFactoryLegacyFallback(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, readFixture(t, "factory_auth_me.json")
		case "/api/billing/limits":
			return http.StatusNotFound, []byte(`{}`)
		case "/api/organization/subscription/usage":
			if r.URL.Query().Get("useCache") != "true" || r.URL.Query().Get("userId") != "user-1" {
				return http.StatusBadRequest, []byte(`{}`)
			}
			return http.StatusOK, readFixture(t, "factory_usage_legacy.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-test-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	standard := findWindow(t, snapshot.Windows, WindowOther, "标准额度")
	if standard.UsedPercent == nil || *standard.UsedPercent != 10 {
		t.Fatalf("标准额度期望 10%%（usedRatio 0.10），得到 %v", standard.UsedPercent)
	}
	premium := findWindow(t, snapshot.Windows, WindowOther, "高级额度")
	if premium.UsedPercent == nil || *premium.UsedPercent != 20 {
		t.Fatalf("高级额度期望 20%%（usedRatio 0.20），得到 %v", premium.UsedPercent)
	}
	if standard.ResetAt == nil || !standard.ResetAt.Equal(*unixTime(1_702_678_400)) {
		t.Fatalf("账期结束时间（毫秒时间戳）不符: %v", standard.ResetAt)
	}
	if snapshot.Balance != nil {
		t.Fatalf("旧版响应不应有余额: %+v", snapshot.Balance)
	}
}

func TestFactoryLegacyUsesJWTSubjectForUserID(t *testing.T) {
	// auth/me 不带 userProfile 时，userId 从 API key（JWT）的 sub 声明解析。
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user_jwt"}`))
	key := "header." + payload + ".signature"

	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{"organization":{"subscription":{"factoryTier":"team"}}}`)
		case "/api/billing/limits":
			return http.StatusNotFound, []byte(`{}`)
		case "/api/organization/subscription/usage":
			if r.URL.Query().Get("userId") != "user_jwt" {
				return http.StatusForbidden, []byte(`{"detail":"Must be manager"}`)
			}
			return http.StatusOK, readFixture(t, "factory_usage_legacy.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	if _, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": key}); err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if got := requestURLs(requests); len(got) != 3 {
		t.Fatalf("应有三个请求: %v", got)
	}
}

func TestFactoryLegacyWindowWithoutRatio(t *testing.T) {
	// 没有 usedRatio 时按 used/allowance 计算。
	body := `{"usage":{"endDate":1702678400000,"standard":{"userTokens":100,"totalAllowance":1000}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{}`)
		case "/api/billing/limits":
			return http.StatusNotFound, []byte(`{}`)
		case "/api/organization/subscription/usage":
			return http.StatusOK, []byte(body)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	standard := findWindow(t, snapshot.Windows, WindowOther, "标准额度")
	if standard.UsedPercent == nil || *standard.UsedPercent != 10 {
		t.Fatalf("标准额度期望 10%%（100/1000），得到 %v", standard.UsedPercent)
	}
}

func TestFactoryUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-bad"})
	requireCredentialError(t, err, http.StatusUnauthorized)

	// 403 同样归为凭据问题。
	server2, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusForbidden, []byte(`{}`)
	})
	_, err = fetchFactory(ctx, server2.Client(), server2.URL, Credentials{"api_key": "fk-bad"})
	requireCredentialError(t, err, http.StatusForbidden)
}

func TestFactoryMalformedResponses(t *testing.T) {
	cases := map[string]func(r *http.Request) (int, []byte){
		"auth/me 非法 JSON": func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`not json`)
		},
		"usage 非法 JSON": func(r *http.Request) (int, []byte) {
			switch r.URL.Path {
			case "/api/app/auth/me":
				return http.StatusOK, []byte(`{}`)
			case "/api/billing/limits":
				return http.StatusNotFound, []byte(`{}`)
			case "/api/organization/subscription/usage":
				return http.StatusOK, []byte(`not json`)
			}
			return http.StatusNotFound, []byte(`{}`)
		},
		"billing/limits 坏结构回退旧流程但 usage 5xx": func(r *http.Request) (int, []byte) {
			switch r.URL.Path {
			case "/api/app/auth/me":
				return http.StatusOK, []byte(`{}`)
			case "/api/billing/limits":
				// 返回结构不符的 200：应回退而不是崩溃。
				return http.StatusOK, []byte(`{"usesTokenRateLimitsBilling":true}`)
			case "/api/organization/subscription/usage":
				return http.StatusInternalServerError, []byte(`{}`)
			}
			return http.StatusNotFound, []byte(`{}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, handler)
			ctx, cancel := fetchCtx()
			defer cancel()
			_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
			requireProviderError(t, err)
		})
	}
}

func TestFactoryExpiredRollingWindowResetsToZero(t *testing.T) {
	// 有 windowEnd 但没有 secondsRemaining 且已过期 → 视为已重置，显示 0%。
	body := `{"usesTokenRateLimitsBilling":true,"limits":{"standard":{
		"fiveHour":{"usedPercent":80,"windowEnd":"2020-01-01T00:00:00Z"},
		"weekly":{"usedPercent":30,"secondsRemaining":600},
		"monthly":{"usedPercent":10}}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{}`)
		case "/api/billing/limits":
			return http.StatusOK, []byte(body)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 0 {
		t.Fatalf("过期滚动窗口应显示 0%%，得到 %v", fiveHour.UsedPercent)
	}
	if fiveHour.ResetAt != nil {
		t.Fatalf("过期滚动窗口不应有重置时间: %v", fiveHour.ResetAt)
	}
	weekly := findWindow(t, snapshot.Windows, WindowWeekly, "每周")
	if weekly.ResetAt == nil {
		t.Fatalf("secondsRemaining 应换算成重置时间")
	}
	monthly := findWindow(t, snapshot.Windows, WindowMonthly, "每月")
	if monthly.UsedPercent == nil || *monthly.UsedPercent != 10 {
		t.Fatalf("每月窗口期望 10%%，得到 %v", monthly.UsedPercent)
	}
}

// ---- 公共行为 ----

func TestMissingCredentialsReported(t *testing.T) {
	server, _ := newTestServer(t, nil)
	ctx, cancel := fetchCtx()
	defer cancel()

	cases := []struct {
		name  string
		fetch func() error
	}{
		{"commandcode", func() error {
			_, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"opencodego", func() error {
			_, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"zai", func() error {
			_, err := fetchZai(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"grok", func() error {
			_, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"codex", func() error {
			_, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"devin", func() error {
			_, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
		{"factory", func() error {
			_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireCredentialError(t, tc.fetch(), 0)
		})
	}
}

func TestRegistryProvidesAllFetchers(t *testing.T) {
	providers := Registry(nil)
	if len(providers) != 7 {
		t.Fatalf("期望 7 个供应商，得到 %d", len(providers))
	}
	for _, p := range providers {
		if p.Fetch == nil {
			t.Fatalf("供应商 %s 缺少 Fetch 实现", p.ID)
		}
		if p.DefaultBaseURL == "" {
			t.Fatalf("供应商 %s 缺少默认 baseURL", p.ID)
		}
	}
	if _, ok := Lookup(nil, "factory"); !ok {
		t.Fatalf("Lookup 应能找到 factory")
	}
	if _, ok := Lookup(nil, "nope"); ok {
		t.Fatalf("Lookup 不应找到未知供应商")
	}
}

func TestPercentAndClampHelpers(t *testing.T) {
	if got := Percent(1, 4); got == nil || *got != 25 {
		t.Fatalf("Percent(1,4) 期望 25，得到 %v", got)
	}
	if got := Percent(1, 0); got != nil {
		t.Fatalf("limit=0 时应返回 nil，得到 %v", got)
	}
	if got := ClampPercent(133.36); got != 100 {
		t.Fatalf("越界应夹到 100，得到 %v", got)
	}
	if got := ClampPercent(-3); got != 0 {
		t.Fatalf("负值应夹到 0，得到 %v", got)
	}
	if got := ClampPercent(33.33333); got != 33.3 {
		t.Fatalf("应四舍五入到 1 位小数，得到 %v", got)
	}
	if got := Round1(12.3456); got != 12.3 {
		t.Fatalf("Round1 期望 12.3，得到 %v", got)
	}
}

func TestFlexibleDateVariants(t *testing.T) {
	cases := []struct {
		input any
		want  time.Time
	}{
		{float64(1_780_000_000), time.Unix(1_780_000_000, 0).UTC()},
		{float64(1_780_000_000_000), time.Unix(1_780_000_000, 0).UTC()},
		{"1780000000", time.Unix(1_780_000_000, 0).UTC()},
		{"2026-06-11T00:00:00-08:00", time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)},
		{"2026-06-11T08:00:00Z", time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)},
		{"2026-06-11T08:00:00.123Z", time.Date(2026, 6, 11, 8, 0, 0, 123000000, time.UTC)},
	}
	for _, tc := range cases {
		got, ok := flexibleDate(tc.input)
		if !ok || !got.Equal(tc.want) {
			t.Fatalf("flexibleDate(%v) 期望 %v，得到 %v (ok=%v)", tc.input, tc.want, got, ok)
		}
	}
	for _, bad := range []any{"", "not-a-date", float64(-5), nil, []any{1}} {
		if _, ok := flexibleDate(bad); ok {
			t.Fatalf("flexibleDate(%v) 应解析失败", bad)
		}
	}
}

func TestErrorMessageSanitized(t *testing.T) {
	secret := "user_SUPERSECRET"
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusUnauthorized, []byte(fmt.Sprintf(`{"detail":"key %s rejected with long internal trace"}`, secret))
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	fetchers := []func() error{
		func() error {
			_, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": secret})
			return err
		},
		func() error {
			_, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": secret})
			return err
		},
		func() error {
			_, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": secret})
			return err
		},
		func() error {
			_, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": secret})
			return err
		},
		func() error {
			_, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": secret})
			return err
		},
		func() error {
			_, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{"token": secret, "org_id": "org-1"})
			return err
		},
		func() error {
			_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": secret})
			return err
		},
	}
	for i, fetch := range fetchers {
		err := fetch()
		if err == nil {
			t.Fatalf("fetcher %d 应返回错误", i)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误消息不得包含凭据明文: %s", err.Error())
		}
	}
}

// ---- 审查回归（F1–F7、F10 与覆盖缺口）----

// F1：available 是「剩余」，放进 used 列表会把用量反向计算（3/10 应为 70% 而不是 30%）。
func TestDevinAvailableIsRemainingNotUsed(t *testing.T) {
	body := `{"quota_usage":{"daily_quota":{"available":3,"quota":10}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(body)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{"token": "tok", "org_id": "org-1"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	daily := findWindow(t, snapshot.Windows, WindowDaily, "每日")
	if daily.UsedPercent == nil || *daily.UsedPercent != 70 {
		t.Fatalf("available=3/quota=10 期望 70%%（余量反推），得到 %v", daily.UsedPercent)
	}
}

// Devin 百分比推导覆盖：remaining 键反推、hide_daily_quota、嵌套对象里含 reset 的键。
func TestDevinPercentDerivations(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		kind    string
		percent float64
		resetAt *time.Time
		windowN int
	}{
		{
			name:    "remaining_percent 小数反推",
			body:    `{"quota_usage":{"daily_quota":{"remaining_percent":0.3}}}`,
			kind:    WindowDaily,
			percent: 70,
			windowN: 1,
		},
		{
			name:    "remainingPercent 百分数反推",
			body:    `{"quota_usage":{"daily_quota":{"remainingPercent":40}}}`,
			kind:    WindowDaily,
			percent: 60,
			windowN: 1,
		},
		{
			name:    "hide_daily_quota 只留每周",
			body:    `{"hide_daily_quota":true,"weekly_percentage":42}`,
			kind:    WindowWeekly,
			percent: 42,
			windowN: 1,
		},
		{
			name:    "嵌套对象任意 reset 键扫描",
			body:    `{"quota_usage":{"daily_quota":{"used":3,"limit":10,"next_reset_time":1788326400}}}`,
			kind:    WindowDaily,
			percent: 30,
			resetAt: unixTime(1_788_326_400),
			windowN: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := parseDevinUsage([]byte(tc.body))
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(snapshot.Windows) != tc.windowN {
				t.Fatalf("期望 %d 个窗口，得到 %+v", tc.windowN, snapshot.Windows)
			}
			w := snapshot.Windows[0]
			if w.Kind != tc.kind {
				t.Fatalf("窗口类型期望 %s，得到 %s", tc.kind, w.Kind)
			}
			if w.UsedPercent == nil || *w.UsedPercent != tc.percent {
				t.Fatalf("期望 %v%%，得到 %v", tc.percent, w.UsedPercent)
			}
			if tc.resetAt == nil {
				if w.ResetAt != nil {
					t.Fatalf("不应有重置时间，得到 %v", w.ResetAt)
				}
			} else if w.ResetAt == nil || !w.ResetAt.Equal(*tc.resetAt) {
				t.Fatalf("重置时间期望 %v，得到 %v", tc.resetAt, w.ResetAt)
			}
		})
	}
}

// F2：只有 onDemandCap 没有 onDemandUsed 时推不出百分比，应报 provider_error 而不是空快照。
func TestGrokCapWithoutUsedIsProviderError(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"config":{"onDemandCap":{"val":500}}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
	requireProviderError(t, err)
}

// F3：5 小时 Coding Plan 窗口的过远未来重置时间应省略（百分比保留），阈值内保留。
func TestZaiFiveHourFarFutureResetOmitted(t *testing.T) {
	now := time.Now().UTC()
	body := fmt.Sprintf(`{"code":200,"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":25,"nextResetTime":%d},
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":30,"nextResetTime":%d}]}}`,
		now.Add(10*time.Hour).UnixMilli(), now.Add(3*time.Hour).UnixMilli())
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(body)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("期望两个 5 小时窗口，得到 %+v", snapshot.Windows)
	}
	far, near := snapshot.Windows[0], snapshot.Windows[1]
	if far.UsedPercent == nil || *far.UsedPercent != 25 {
		t.Fatalf("过远窗口百分比应保留 25%%，得到 %v", far.UsedPercent)
	}
	if far.ResetAt != nil {
		t.Fatalf("> 5h+1min 的未来重置时间应省略，得到 %v", far.ResetAt)
	}
	if near.ResetAt == nil {
		t.Fatalf("阈值内的重置时间应保留")
	}
}

// F4（opencodego）：绝对时间键解析失败时按序回退到下一个键，而不是直接丢弃。
func TestOpenCodeGoResetKeyFallbackParsesNext(t *testing.T) {
	body := `{"usage":{"rolling":{"percent":10,"resetsAt":"garbage","reset_at":1788326400}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(body)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	rolling := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if rolling.ResetAt == nil || !rolling.ResetAt.Equal(*unixTime(1_788_326_400)) {
		t.Fatalf("resetsAt 不可解析时应回退 reset_at，得到 %v", rolling.ResetAt)
	}
}

// F4（codex）：月度重置键序 resetsAt → resets_at → reset_at，且按「可解析」回退。
func TestCodexMonthlyResetKeyPriority(t *testing.T) {
	cases := map[string]string{
		"camelCase 键": `{"individual_limit":{"limit":30,"used":12,"resetsAt":1769875200}}`,
		"解析失败回退下一键":   `{"individual_limit":{"limit":30,"used":12,"resets_at":"garbage","reset_at":1769875200}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			snapshot, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
			if err != nil {
				t.Fatalf("取数失败: %v", err)
			}
			monthly := findWindow(t, snapshot.Windows, WindowMonthly, "月度额度")
			if monthly.ResetAt == nil || !monthly.ResetAt.Equal(*unixTime(1_769_875_200)) {
				t.Fatalf("月度重置时间期望 %v，得到 %v", unixTime(1_769_875_200), monthly.ResetAt)
			}
		})
	}
}

// F5：unixDate 下界——过小的正数不是 Unix 秒/毫秒，按解析失败处理。
func TestUnixDateRejectsSmallNumbers(t *testing.T) {
	for _, bad := range []float64{3600, 1e6, 1e9} {
		if _, ok := unixDate(bad); ok {
			t.Fatalf("unixDate(%v) 应解析失败", bad)
		}
	}
	if got, ok := unixDate(1e9 + 1); !ok || !got.Equal(time.Unix(1_000_000_001, 0).UTC()) {
		t.Fatalf("unixDate 刚过下界应按秒解析，得到 %v (ok=%v)", got, ok)
	}
	if got, ok := unixDate(2e12); !ok || !got.Equal(time.UnixMilli(2e12).UTC()) {
		t.Fatalf("unixDate(2e12) 应按毫秒解析，得到 %v (ok=%v)", got, ok)
	}

	// 端到端：reset_at=3600 不再变成 1970-01-01，而是没有重置时间。
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"usage":{"rolling":{"percent":10,"reset_at":3600}}}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	snapshot, err := fetchOpenCodeGo(ctx, server.Client(), server.URL, Credentials{"api_key": "go_secret"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	rolling := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if rolling.ResetAt != nil {
		t.Fatalf("reset_at=3600 应被丢弃而不是变成 %v", rolling.ResetAt)
	}
}

// F6：legacy usage 的 userId 来自 JWT sub，可能含 | / 等字符，必须 URL 转义。
func TestFactoryLegacyEscapesUserIDQuery(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user|abc/1"}`))
	key := "header." + payload + ".signature"

	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{}`)
		case "/api/billing/limits":
			return http.StatusNotFound, []byte(`{}`)
		case "/api/organization/subscription/usage":
			if got := r.URL.Query().Get("userId"); got != "user|abc/1" {
				return http.StatusBadRequest, []byte(`{}`)
			}
			return http.StatusOK, readFixture(t, "factory_usage_legacy.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	if _, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": key}); err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	var rawQuery string
	for _, r := range *requests {
		if r.URL.Path == "/api/organization/subscription/usage" {
			rawQuery = r.URL.RawQuery
		}
	}
	if !strings.Contains(rawQuery, "userId=user%7Cabc%2F1") {
		t.Fatalf("userId 必须转义后再拼进 query，得到 %q", rawQuery)
	}
}

// F7：空 windows 必须序列化成 [] 而不是 null。
func TestEmptyWindowsSerializeAsArray(t *testing.T) {
	t.Run("codex 只有余额", func(t *testing.T) {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"credits":{"balance":"4.25"}}`)
		})
		ctx, cancel := fetchCtx()
		defer cancel()
		snapshot, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
		if err != nil {
			t.Fatalf("取数失败: %v", err)
		}
		if len(snapshot.Windows) != 0 {
			t.Fatalf("期望空 windows，得到 %+v", snapshot.Windows)
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if !strings.Contains(string(data), `"windows":[]`) {
			t.Fatalf("空 windows 应输出 []，得到 %s", data)
		}
	})
	t.Run("commandcode 订阅失败", func(t *testing.T) {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			switch r.URL.Path {
			case "/alpha/billing/credits":
				return http.StatusOK, readFixture(t, "commandcode_credits_basic.json")
			case "/alpha/billing/subscriptions":
				return http.StatusInternalServerError, []byte(`{}`)
			}
			return http.StatusNotFound, []byte(`{}`)
		})
		ctx, cancel := fetchCtx()
		defer cancel()
		snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
		if err != nil {
			t.Fatalf("取数失败: %v", err)
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if !strings.Contains(string(data), `"windows":[]`) {
			t.Fatalf("空 windows 应输出 []，得到 %s", data)
		}
	})
}

// F8：core 池「有数据」按键存在判定（含 0 值 secondsRemaining）。
func TestFactoryCorePoolZeroValuesStillHasData(t *testing.T) {
	body := `{"usesTokenRateLimitsBilling":true,"limits":{
		"standard":{"fiveHour":{"usedPercent":10,"secondsRemaining":600}},
		"core":{"fiveHour":{"usedPercent":0,"secondsRemaining":0}}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{}`)
		case "/api/billing/limits":
			return http.StatusOK, []byte(body)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	core := findWindow(t, snapshot.Windows, WindowFiveHour, "Core 5 小时")
	if core.UsedPercent == nil || *core.UsedPercent != 0 {
		t.Fatalf("core 池 0 值也算有数据，期望 0%%，得到 %v", core.UsedPercent)
	}
}

// F10：subscriptions 挂起时最多多等一个独立子超时就放弃富化，credits 不受影响。
func TestCommandCodeSubscriptionGraceTimeout(t *testing.T) {
	old := commandCodeSubscriptionGrace
	commandCodeSubscriptionGrace = 50 * time.Millisecond
	t.Cleanup(func() { commandCodeSubscriptionGrace = old })

	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return http.StatusOK, readFixture(t, "commandcode_credits_root.json")
		case "/alpha/billing/subscriptions":
			time.Sleep(600 * time.Millisecond)
			return http.StatusOK, readFixture(t, "commandcode_subscription.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	start := time.Now()
	snapshot, err := fetchCommandCode(ctx, server.Client(), server.URL, Credentials{"api_key": "user_test"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("订阅挂起不应影响 credits: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("订阅富化应受独立子超时约束，实际耗时 %v", elapsed)
	}
	if snapshot.PlanName != "" {
		t.Fatalf("富化超时后不应有套餐名，得到 %q", snapshot.PlanName)
	}
	fiveHour := findWindow(t, snapshot.Windows, WindowFiveHour, "5 小时")
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 25 {
		t.Fatalf("credits 主数据应完整，得到 %v", fiveHour.UsedPercent)
	}
}

// codexIndividualLimit 回退优先级：根 → rate_limit → spend_control。
func TestCodexIndividualLimitFallbackPriority(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantUsed float64
		wantCap  float64
	}{
		{
			name:     "根级优先于 rate_limit",
			body:     `{"rate_limit":{"individual_limit":{"limit":40,"used":20}},"individual_limit":{"limit":30,"used":12}}`,
			wantUsed: 12,
			wantCap:  30,
		},
		{
			name:     "rate_limit 回退",
			body:     `{"rate_limit":{"individual_limit":{"limit":30,"used":12}}}`,
			wantUsed: 12,
			wantCap:  30,
		},
		{
			name:     "spend_control 回退",
			body:     `{"spend_control":{"individual_limit":{"limit":50,"used":25}}}`,
			wantUsed: 25,
			wantCap:  50,
		},
		{
			name:     "spendControl 驼峰回退",
			body:     `{"spendControl":{"individualLimit":{"limit":50,"used":10}}}`,
			wantUsed: 10,
			wantCap:  50,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(tc.body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			snapshot, err := fetchCodex(ctx, server.Client(), server.URL, Credentials{"access_token": "tok"})
			if err != nil {
				t.Fatalf("取数失败: %v", err)
			}
			monthly := findWindow(t, snapshot.Windows, WindowMonthly, "月度额度")
			if monthly.Used == nil || *monthly.Used != tc.wantUsed || monthly.Limit == nil || *monthly.Limit != tc.wantCap {
				t.Fatalf("月度额度期望 %v/%v，得到 %v/%v", tc.wantUsed, tc.wantCap, monthly.Used, monthly.Limit)
			}
		})
	}
}

// Grok billingPeriodEnd 回退与月度周期 → monthly 窗口。
func TestGrokBillingPeriodFallbackAndMonthlyKind(t *testing.T) {
	t.Run("billingPeriodEnd 回退", func(t *testing.T) {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"config":{"creditUsagePercent":20,"billingPeriodEnd":"2026-08-13T00:00:00Z"}}`)
		})
		ctx, cancel := fetchCtx()
		defer cancel()
		snapshot, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
		if err != nil {
			t.Fatalf("取数失败: %v", err)
		}
		window := findWindow(t, snapshot.Windows, WindowOther, "账单周期")
		if window.ResetAt == nil || !window.ResetAt.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("无 currentPeriod 时应回落 billingPeriodEnd，得到 %v", window.ResetAt)
		}
	})
	t.Run("30 天周期按月度窗口", func(t *testing.T) {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"config":{"creditUsagePercent":20,
				"currentPeriod":{"start":"2026-07-14T00:00:00Z","end":"2026-08-13T00:00:00Z"}}}`)
		})
		ctx, cancel := fetchCtx()
		defer cancel()
		snapshot, err := fetchGrok(ctx, server.Client(), server.URL, Credentials{"token": "token-123"})
		if err != nil {
			t.Fatalf("取数失败: %v", err)
		}
		if len(snapshot.Windows) != 1 {
			t.Fatalf("期望一个窗口，得到 %+v", snapshot.Windows)
		}
		window := snapshot.Windows[0]
		if window.Kind != WindowMonthly || window.Label != "每月" {
			t.Fatalf("30 天周期应为 monthly/每月，得到 %s/%s", window.Kind, window.Label)
		}
		if window.ResetAt == nil || !window.ResetAt.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("月度重置时间应取 currentPeriod.end，得到 %v", window.ResetAt)
		}
	})
}

// Factory legacy：ratio=0 且 used>0 是占位哨兵；allowance 超过 1e12 视为不限量折算；
// legacy usage 401/403 归为凭据错误。
func TestFactoryLegacyRatioSentinelAndUncapped(t *testing.T) {
	cases := []struct {
		name    string
		usage   string
		percent float64
	}{
		{
			name:    "ratio 0 哨兵按 used/allowance",
			usage:   `{"usage":{"standard":{"userTokens":50,"totalAllowance":1000,"usedRatio":0}}}`,
			percent: 5,
		},
		{
			name:    "超大 allowance 不限量折算",
			usage:   `{"usage":{"standard":{"userTokens":50000000,"totalAllowance":2000000000000}}}`,
			percent: 50,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				switch r.URL.Path {
				case "/api/app/auth/me":
					return http.StatusOK, []byte(`{}`)
				case "/api/billing/limits":
					return http.StatusNotFound, []byte(`{}`)
				case "/api/organization/subscription/usage":
					return http.StatusOK, []byte(tc.usage)
				}
				return http.StatusNotFound, []byte(`{}`)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
			if err != nil {
				t.Fatalf("取数失败: %v", err)
			}
			standard := findWindow(t, snapshot.Windows, WindowOther, "标准额度")
			if standard.UsedPercent == nil || *standard.UsedPercent != tc.percent {
				t.Fatalf("期望 %v%%，得到 %v", tc.percent, standard.UsedPercent)
			}
		})
	}
}

func TestFactoryLegacyUsageAuthError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			switch r.URL.Path {
			case "/api/app/auth/me":
				return http.StatusOK, []byte(`{}`)
			case "/api/billing/limits":
				return http.StatusNotFound, []byte(`{}`)
			case "/api/organization/subscription/usage":
				return status, []byte(`{}`)
			}
			return http.StatusNotFound, []byte(`{}`)
		})
		ctx, cancel := fetchCtx()
		defer cancel()
		_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
		requireCredentialError(t, err, status)
	}
}

// zai：只有 TIME_LIMIT 的响应给出 MCP 窗口；未知 unit 归 other 并给准确标签。
func TestZaiOnlyTimeLimitAndUnknownUnits(t *testing.T) {
	cases := []struct {
		name  string
		limit string
		kind  string
		label string
		unit  string
	}{
		{
			name:  "只有 TIME_LIMIT",
			limit: `{"type":"TIME_LIMIT","unit":5,"number":1,"percentage":22}`,
			kind:  WindowOther,
			label: "MCP 次数",
			unit:  UnitCount,
		},
		{
			name:  "未知 unit 的 TOKENS_LIMIT",
			limit: `{"type":"TOKENS_LIMIT","unit":99,"number":1,"percentage":40}`,
			kind:  WindowOther,
			label: "Token 额度",
			unit:  UnitPercent,
		},
		{
			name:  "未知 unit 的 CREDIT_LIMIT",
			limit: `{"type":"CREDIT_LIMIT","unit":99,"number":1,"percentage":60}`,
			kind:  WindowOther,
			label: "Credit 额度",
			unit:  UnitPercent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"code":200,"success":true,"data":{"limits":[` + tc.limit + `]}}`
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			snapshot, err := fetchZai(ctx, server.Client(), server.URL, Credentials{"api_key": "zl_key"})
			if err != nil {
				t.Fatalf("取数失败: %v", err)
			}
			if len(snapshot.Windows) != 1 {
				t.Fatalf("期望一个窗口，得到 %+v", snapshot.Windows)
			}
			window := snapshot.Windows[0]
			if window.Kind != tc.kind || window.Label != tc.label {
				t.Fatalf("窗口期望 %s/%s，得到 %s/%s", tc.kind, tc.label, window.Kind, window.Label)
			}
			if window.Unit != tc.unit {
				t.Fatalf("单位期望 %s，得到 %s", tc.unit, window.Unit)
			}
		})
	}
}

// Registry 的 CredentialFields 必填/密文标记与冻结契约 §1 一致。
func TestRegistryCredentialFieldsMatchContract(t *testing.T) {
	want := map[string][]CredentialField{
		"commandcode": {{Key: "api_key", Secret: true, Required: true}},
		"opencodego":  {{Key: "api_key", Secret: true, Required: true}},
		"zai":         {{Key: "api_key", Secret: true, Required: true}},
		"grok":        {{Key: "token", Secret: true, Required: true}},
		"codex":       {{Key: "access_token", Secret: true, Required: true}, {Key: "account_id"}},
		"devin":       {{Key: "token", Secret: true, Required: true}, {Key: "org_id", Required: true}},
		"factory":     {{Key: "api_key", Secret: true, Required: true}},
	}
	providers := Registry(nil)
	if len(providers) != len(want) {
		t.Fatalf("期望 %d 个供应商，得到 %d", len(want), len(providers))
	}
	for _, p := range providers {
		expected, ok := want[p.ID]
		if !ok {
			t.Fatalf("未知供应商 %s", p.ID)
		}
		if len(p.CredentialFields) != len(expected) {
			t.Fatalf("%s 凭据字段数期望 %d，得到 %d", p.ID, len(expected), len(p.CredentialFields))
		}
		for i, field := range p.CredentialFields {
			e := expected[i]
			if field.Key != e.Key {
				t.Fatalf("%s 第 %d 个字段期望 %s，得到 %s", p.ID, i, e.Key, field.Key)
			}
			if field.Required != e.Required {
				t.Fatalf("%s 字段 %s required 期望 %v，得到 %v", p.ID, field.Key, e.Required, field.Required)
			}
			if field.Secret != e.Secret {
				t.Fatalf("%s 字段 %s secret 期望 %v，得到 %v", p.ID, field.Key, e.Secret, field.Secret)
			}
		}
	}
	// baseURLs 覆盖仍生效。
	custom := Registry(map[string]string{"factory": "http://127.0.0.1:5099"})
	for _, p := range custom {
		if p.ID == "factory" && p.DefaultBaseURL != "http://127.0.0.1:5099" {
			t.Fatalf("baseURL 覆盖失效: %s", p.DefaultBaseURL)
		}
	}
}

// available/remaining 是「剩余」，既不能当已用，也不能当总量：used=3、available=7
// 的真值是 30%。把剩余当分母会得到 42.9%（低估用量），所以没有显式上限时用
// 「已用 + 剩余」推总量。
func TestDevinRemainingIsNotTheTotalLimit(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{
			name: "used 与 available，无显式上限",
			body: `{"quota_usage":{"daily_quota":{"used":3,"available":7}}}`,
			want: 30,
		},
		{
			name: "used 与 remaining，无显式上限",
			body: `{"quota_usage":{"daily_quota":{"used":30,"remaining":70}}}`,
			want: 30,
		},
		{
			name: "显式上限优先于剩余",
			body: `{"quota_usage":{"daily_quota":{"used":3,"available":7,"quota":10}}}`,
			want: 30,
		},
		{
			name: "只有 remaining 与 limit",
			body: `{"quota_usage":{"daily_quota":{"remaining":70,"limit":100}}}`,
			want: 30,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				return http.StatusOK, []byte(tc.body)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			snapshot, err := fetchDevin(ctx, server.Client(), server.URL, Credentials{"token": "tok", "org_id": "org-1"})
			if err != nil {
				t.Fatalf("取数失败: %v", err)
			}
			daily := findWindow(t, snapshot.Windows, WindowDaily, "每日")
			if daily.UsedPercent == nil || *daily.UsedPercent != tc.want {
				t.Fatalf("%s 期望 %v%%，得到 %v", tc.body, tc.want, daily.UsedPercent)
			}
		})
	}
}

// core 池只报 usedPercent: 0（没有 windowEnd / secondsRemaining）时也算有数据：
// 判定按「键存在」而不是「值非零」，否则整池 0% 的核心窗口会被丢掉。
func TestFactoryCorePoolZeroUsedPercentAloneIsData(t *testing.T) {
	body := `{"usesTokenRateLimitsBilling":true,"limits":{
		"standard":{"fiveHour":{"usedPercent":10,"secondsRemaining":600}},
		"core":{"fiveHour":{"usedPercent":0}}}}`
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/app/auth/me":
			return http.StatusOK, []byte(`{}`)
		case "/api/billing/limits":
			return http.StatusOK, []byte(body)
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	snapshot, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	core := findWindow(t, snapshot.Windows, WindowFiveHour, "Core 5 小时")
	if core.UsedPercent == nil || *core.UsedPercent != 0 {
		t.Fatalf("usedPercent=0 单独出现也算有数据，期望 0%%，得到 %v", core.UsedPercent)
	}
	if core.ResetAt != nil {
		t.Fatalf("没有计时字段时不应有重置时间，得到 %v", core.ResetAt)
	}
}

// 旧版 Factory 接口返回 2xx 但没有可识别额度（usage 缺失/为空）时必须报接口异常：
// 返回空快照会被 service 当成成功，用空窗口覆盖掉上次可用的数据。
func TestFactoryLegacyEmptyUsageIsProviderError(t *testing.T) {
	cases := map[string]string{
		"usage 缺失": `{"userId":"user-1"}`,
		"usage 为空": `{"usage":{},"userId":"user-1"}`,
		"标准与高级窗口都没有可解析字段": `{"usage":{"standard":{"x":1},"premium":{"y":2}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				switch r.URL.Path {
				case "/api/app/auth/me":
					return http.StatusOK, []byte(`{"organization":{"subscription":{"orbSubscription":{"plan":{"name":"Pro"}}}}}`)
				case "/api/billing/limits":
					return http.StatusNotFound, []byte(`{}`)
				case "/api/organization/subscription/usage":
					return http.StatusOK, []byte(body)
				}
				return http.StatusNotFound, []byte(`{}`)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			_, err := fetchFactory(ctx, server.Client(), server.URL, Credentials{"api_key": "fk-key"})
			requireProviderError(t, err)
		})
	}
}
