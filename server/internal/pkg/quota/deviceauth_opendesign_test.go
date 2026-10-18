package quota

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 设备授权适配器测试（INT-117 第 3 轮）：直接用真实上游的脱敏样本
// （testdata/opendesign_device_*.json，2026-10-18 实测抓取）驱动，
// 断言协议解析与状态映射，而不是只检查样本形状。

// openDesignDeviceTestDeviceID 是脱敏样本里的 deviceId，token 端点的路由用。
const openDesignDeviceTestDeviceID = "dev_00000000000000000000000"

// 起始：真实 201 样本逐字段解析，发起请求带 UA 与标识 body。
func TestOpenDesignDeviceAuthStart(t *testing.T) {
	var startBody []byte
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/cli/device-authorizations" {
			startBody, _ = io.ReadAll(r.Body)
			// 真实上游返回 201，不是 200。
			return http.StatusCreated, readFixture(t, "opendesign_device_start.json")
		}
		return http.StatusNotFound, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()

	start, err := OpenDesignDeviceAuth().Start(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("start 失败: %v", err)
	}
	if start.DeviceID != openDesignDeviceTestDeviceID {
		t.Fatalf("deviceId 期望样本值，得到 %q", start.DeviceID)
	}
	if start.DeviceSecret != "device_secret_placeholder" {
		t.Fatalf("deviceSecret 期望样本占位值，得到 %q", start.DeviceSecret)
	}
	if start.UserCode != "ABCDEF" {
		t.Fatalf("userCode 期望 ABCDEF，得到 %q", start.UserCode)
	}
	wantURL := "https://open-design.ai/cloud/cli/activate?deviceId=dev_00000000000000000000000&userCode=ABCDEF"
	if start.ActivationURL != wantURL {
		t.Fatalf("activationUrl 期望 %q，得到 %q", wantURL, start.ActivationURL)
	}
	if !start.ExpiresAt.Equal(time.Date(2026, 10, 18, 13, 15, 0, 0, time.UTC)) {
		t.Fatalf("expiresAt 期望 UTC 2026-10-18T13:15:00Z，得到 %v", start.ExpiresAt)
	}
	if start.PollInterval != 2*time.Second {
		t.Fatalf("pollInterval 期望 2s，得到 %v", start.PollInterval)
	}

	if len(*requests) != 1 {
		t.Fatalf("应恰好一次起始请求，得到 %d", len(*requests))
	}
	r := (*requests)[0]
	expectHeader(t, r, "User-Agent", userAgent)
	expectHeader(t, r, "Content-Type", "application/json")
	expectHeader(t, r, "Accept", "application/json")
	// 起始 body 是无认证的固定标识；上游把它记在授权记录里。
	var sent map[string]any
	if err := json.Unmarshal(startBody, &sent); err != nil {
		t.Fatalf("起始请求体应为 JSON: %v", err)
	}
	if sent["cliVersion"] != "fast-ship" || sent["host"] != "fast-ship" || sent["profile"] != "prod" {
		t.Fatalf("起始请求体标识不符: %v", sent)
	}
	if _, hasAuth := r.Header["Authorization"]; hasAuth {
		t.Fatalf("设备授权端点不应带认证头")
	}
}

// 上游给的 pollIntervalSeconds 缺失或非正数时缺省 2s。
func TestOpenDesignDeviceAuthStartPollIntervalDefault(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"deviceId":"dev_x","deviceSecret":"ds_x","userCode":"ABCD",` +
			`"activationUrl":"https://open-design.ai/activate","pollIntervalSeconds":0}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	start, err := OpenDesignDeviceAuth().Start(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("start 失败: %v", err)
	}
	if start.PollInterval != 2*time.Second {
		t.Fatalf("pollInterval 缺省应为 2s，得到 %v", start.PollInterval)
	}
}

// userCode 是用户要输入的验证码：上游 201 但缺失（或置空）时必须报接口变更，
// 而不是下发空 user_code 让用户干等到过期。
func TestOpenDesignDeviceAuthStartRejectsEmptyUserCode(t *testing.T) {
	cases := []string{
		`{"deviceId":"dev_x","deviceSecret":"ds_x","activationUrl":"https://open-design.ai/activate"}`,
		`{"deviceId":"dev_x","deviceSecret":"ds_x","userCode":"","activationUrl":"https://open-design.ai/activate"}`,
	}
	for _, body := range cases {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			return http.StatusCreated, []byte(body)
		})
		ctx, cancel := fetchCtx()
		_, err := OpenDesignDeviceAuth().Start(ctx, server.Client(), server.URL)
		cancel()
		requireProviderError(t, err)
		if !strings.Contains(err.Error(), "授权码为空") {
			t.Fatalf("缺 userCode 应报「授权码为空」，得到 %v", err)
		}
	}
}

// activation_url 限定 http/https：前端 target=_blank 直接打开，
// 上游被劫持或接口变更时不能把用户带去任意 scheme。
func TestOpenDesignDeviceAuthStartRejectsUnsafeActivationURL(t *testing.T) {
	cases := []string{
		"javascript:alert(1)",
		"ftp://open-design.ai/activate",
		"file:///etc/passwd",
		"", // 缺失同样不合法
	}
	for _, activationURL := range cases {
		server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
			return http.StatusOK, []byte(`{"deviceId":"dev_x","deviceSecret":"ds_x","userCode":"ABCD","activationUrl":"` + activationURL + `"}`)
		})
		ctx, cancel := fetchCtx()
		_, err := OpenDesignDeviceAuth().Start(ctx, server.Client(), server.URL)
		cancel()
		requireProviderError(t, err)
		if !strings.Contains(err.Error(), "授权链接不合法") {
			t.Fatalf("activationUrl %q 应报「授权链接不合法」，得到 %v", activationURL, err)
		}
	}
}

// token 端点的状态映射：真实样本驱动的 pending/expired/401/404，
// 外加批准（只取控制面密钥）、别名状态与 5xx。
func TestOpenDesignDeviceAuthTokenStates(t *testing.T) {
	approvedBody := `{"status":"pending","controlKey":"vela_ctrl_test_key","runtimeKey":"sk-amr-runtime_key","user":null}`
	cases := []struct {
		name        string
		status      int
		body        string
		fixture     string
		wantState   DeviceAuthState
		wantControl string // approved 时期望的凭据值
		wantNoCreds bool
		wantMessage string
	}{
		{name: "真实样本 pending", fixture: "opendesign_device_token_pending.json",
			wantState: DeviceAuthPending, wantNoCreds: true},
		{name: "真实样本 expired（200 而非 404）", fixture: "opendesign_device_token_expired.json",
			wantState: DeviceAuthExpired, wantMessage: "验证码已过期，请重新发起授权。"},
		{name: "真实样本 401 invalid_device_secret", fixture: "opendesign_device_token_bad_secret.json", status: http.StatusUnauthorized,
			wantState: DeviceAuthInvalid, wantMessage: "设备授权已失效，请重新发起授权。"},
		{name: "真实样本 404 device_authorization_not_found", fixture: "opendesign_device_token_unknown.json", status: http.StatusNotFound,
			wantState: DeviceAuthInvalid, wantMessage: "设备授权已失效，请重新发起授权。"},
		{name: "批准只取控制面密钥，runtimeKey 丢弃", body: approvedBody,
			wantState: DeviceAuthApproved, wantControl: "vela_ctrl_test_key"},
		{name: "canceled 别名归 expired", body: `{"status":"canceled"}`,
			wantState: DeviceAuthExpired, wantMessage: "验证码已过期，请重新发起授权。"},
		{name: "rejected 别名归 denied", body: `{"status":"rejected"}`,
			wantState: DeviceAuthDenied, wantMessage: "授权已被拒绝，请重新发起授权。"},
		{name: "未知状态保持 pending", body: `{"status":"still_waiting"}`,
			wantState: DeviceAuthPending, wantNoCreds: true},
		{name: "无 status 无凭据保持 pending", body: `{}`,
			wantState: DeviceAuthPending, wantNoCreds: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tokenBody []byte
			server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
				if r.Method == http.MethodPost &&
					r.URL.Path == "/api/v1/cli/device-authorizations/"+openDesignDeviceTestDeviceID+"/token" {
					tokenBody, _ = io.ReadAll(r.Body)
					status := tc.status
					if status == 0 {
						status = http.StatusOK
					}
					if tc.fixture != "" {
						return status, readFixture(t, tc.fixture)
					}
					return status, []byte(tc.body)
				}
				return http.StatusNotFound, []byte(`{}`)
			})
			ctx, cancel := fetchCtx()
			defer cancel()

			poll, err := OpenDesignDeviceAuth().Poll(ctx, server.Client(), server.URL,
				openDesignDeviceTestDeviceID, "device_secret_placeholder")
			if err != nil {
				t.Fatalf("poll 失败: %v", err)
			}
			if poll.State != tc.wantState {
				t.Fatalf("state 期望 %q，得到 %q", tc.wantState, poll.State)
			}
			if poll.Message != tc.wantMessage {
				t.Fatalf("message 期望 %q，得到 %q", tc.wantMessage, poll.Message)
			}
			if tc.wantControl != "" {
				if len(poll.Credentials) != 1 || poll.Credentials["control_key"] != tc.wantControl {
					t.Fatalf("凭据应只含控制面密钥，得到 %v", poll.Credentials)
				}
				if !strings.Contains(string(tokenBody), "device_secret_placeholder") {
					t.Fatalf("token 请求体应携带设备密钥")
				}
			}
			if tc.wantNoCreds && len(poll.Credentials) != 0 {
				t.Fatalf("pending 不应带凭据，得到 %v", poll.Credentials)
			}
		})
	}
}

// token 端点 5xx：返回错误（service 层保持 pending 待下次重试），不映射成终态。
func TestOpenDesignDeviceAuthTokenUpstreamError(t *testing.T) {
	server, _ := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusBadGateway, []byte(`{}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	_, err := OpenDesignDeviceAuth().Poll(ctx, server.Client(), server.URL,
		openDesignDeviceTestDeviceID, "device_secret_placeholder")
	requireProviderError(t, err)
}

// 设备 ID 进 URL 路径必须转义（url.PathEscape），路径参数不改变请求语义。
func TestOpenDesignDeviceAuthTokenPathEscape(t *testing.T) {
	server, requests := newTestServer(t, func(r *http.Request) (int, []byte) {
		return http.StatusOK, []byte(`{"status":"pending"}`)
	})
	ctx, cancel := fetchCtx()
	defer cancel()
	if _, err := OpenDesignDeviceAuth().Poll(ctx, server.Client(), server.URL,
		"dev x/1", "device_secret_placeholder"); err != nil {
		t.Fatalf("poll 失败: %v", err)
	}
	if len(*requests) != 1 {
		t.Fatalf("应恰好一次请求，得到 %d", len(*requests))
	}
	if escaped := (*requests)[0].URL.EscapedPath(); !strings.HasSuffix(escaped, "/dev%20x%2F1/token") {
		t.Fatalf("deviceId 应 PathEscape，得到 %q", escaped)
	}
}
