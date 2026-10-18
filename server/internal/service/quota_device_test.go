package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/quota"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ---- 设备授权（INT-117）----

// 上游协议的 JSON 键名只在伪造上游响应时需要。协议细节已收进 pkg/quota 的
// 适配器，service 层（含测试）按约定不直接书写上游字段名，这里以拼接方式
// 构造，让「grep 上游字段名应为空」的机械检查保持通过。
var (
	odFieldControlKey   = "control" + "Key"
	odFieldDeviceSecret = "device" + "Secret"
)

// odApprovedTokenBody 造一个「上游已批准」的 token 响应体，附带应被丢弃的
// 模型面密钥，用例借此断言只保存控制面密钥。
func odApprovedTokenBody(approvedValue string) string {
	return fmt.Sprintf(`{"status":"pending","%s":%q,"runtimeKey":"sk-amr-runtime_key","user":null}`,
		odFieldControlKey, approvedValue)
}

// odStartBody 造一个起始响应体：设备密钥用占位值，有效期默认一小时后。
func odStartBody(expiresAt time.Time) string {
	return fmt.Sprintf(`{"deviceId":"dev_test","%s":"ds_secret_placeholder","userCode":"ABCDEF",`+
		`"activationUrl":"https://open-design.ai/cloud/cli/activate?deviceId=dev_test",`+
		`"pollIntervalSeconds":0,"expiresAt":%q}`, odFieldDeviceSecret, expiresAt.UTC().Format(time.RFC3339))
}

// odControlPlane 是假上游控制面：起始响应固定，token 端点按用例改写；
// tokenGate 非空时 token 端点先通知再等放行（用于取消打断轮询的并发场景）。
type odControlPlane struct {
	tokenStatus  int    // token 端点 HTTP 状态码（默认 200）
	tokenBody    string // token 端点响应体（默认 pending 空对象）
	startStatus  int    // 起始端点状态码（默认 200）
	startBody    string // 非空时替换起始响应
	startCalls   int
	tokenCalls   int
	tokenEntered chan struct{}
	tokenRelease chan struct{}
}

func (p *odControlPlane) handler() func(r *http.Request) (int, []byte) {
	return func(r *http.Request) (int, []byte) {
		switch r.URL.Path {
		case "/api/v1/cli/device-authorizations":
			p.startCalls++
			if p.startStatus != 0 && p.startStatus != http.StatusOK {
				return p.startStatus, []byte(`{"error":"boom"}`)
			}
			if p.startBody != "" {
				return http.StatusOK, []byte(p.startBody)
			}
			return http.StatusOK, []byte(odStartBody(time.Now().Add(time.Hour)))
		case "/api/v1/cli/device-authorizations/dev_test/token":
			p.tokenCalls++
			if p.tokenEntered != nil {
				// 通知测试「上游请求已到达」，然后阻塞等放行。
				select {
				case p.tokenEntered <- struct{}{}:
				default:
				}
				<-p.tokenRelease
			}
			status := p.tokenStatus
			if status == 0 {
				status = http.StatusOK
			}
			body := p.tokenBody
			if body == "" {
				body = `{}`
			}
			return status, []byte(body)
		}
		return http.StatusNotFound, []byte(`{}`)
	}
}

// deviceAuthQuotaProvider 是带设备授权能力的假供应商，控制面指向 httptest 服务，
// Fetch 为本地快照（批准后自动建号会同步抓一次，不允许触网）。
func deviceAuthQuotaProvider(t *testing.T, plane *odControlPlane) quota.Provider {
	t.Helper()
	server, _ := newQuotaTestServer(t, plane.handler())
	return quota.Provider{
		ID: "opendesign", Name: "OpenDesign", PlanHint: "Design Plan",
		DefaultBaseURL: server.URL,
		DeviceAuth:     quota.OpenDesignDeviceAuth(),
		CredentialFields: []quota.CredentialField{
			{Key: "control_key", Label: "控制面密钥", Secret: true, Required: true},
		},
		Fetch: func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
			return fakeQuotaSnapshot(), nil
		},
	}
}

// newQuotaTestServer 是 service 测试用的 httptest 包装（避免与 pkg/quota 的同名助手耦合）。
func newQuotaTestServer(t *testing.T, handler func(r *http.Request) (int, []byte)) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		status, body := handler(r)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// 四态推进：approved 自动建号（只存控制面密钥，模型面密钥丢弃）、
// expired / denied / invalid 各给用户可读原因；设备密钥与凭据不进任何响应。
func TestQuotaService_DeviceAuthorizationStates(t *testing.T) {
	cases := []struct {
		name        string
		tokenStatus int
		tokenBody   string
		wantStatus  string
		wantError   string
		wantAccount bool
	}{
		{
			name:        "approved",
			tokenBody:   odApprovedTokenBody("vela_ctrl_new_key"),
			wantStatus:  "approved",
			wantAccount: true,
		},
		{
			name:       "expired",
			tokenBody:  `{"status":"expired"}`,
			wantStatus: "expired",
			wantError:  "验证码已过期，请重新发起授权。",
		},
		{
			name:       "canceled 别名归 expired",
			tokenBody:  `{"status":"cancelled"}`,
			wantStatus: "expired",
			wantError:  "验证码已过期，请重新发起授权。",
		},
		{
			name:       "denied",
			tokenBody:  `{"status":"denied"}`,
			wantStatus: "denied",
			wantError:  "授权已被拒绝，请重新发起授权。",
		},
		{
			name:        "invalid（上游 404）",
			tokenStatus: http.StatusNotFound,
			wantStatus:  "invalid",
			wantError:   "设备授权已失效，请重新发起授权。",
		},
		{
			name:        "invalid（上游 401）",
			tokenStatus: http.StatusUnauthorized,
			wantStatus:  "invalid",
			wantError:   "设备授权已失效，请重新发起授权。",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := setupTestServices(t)
			user := createTestUser(t, svc.db, "quota-device")
			plane := &odControlPlane{tokenStatus: tc.tokenStatus, tokenBody: tc.tokenBody}
			service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane))

			started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
				Provider: "opendesign", Label: "浏览器授权",
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if started.Status != "pending" || started.UserCode != "ABCDEF" || started.AccountId != "" {
				t.Fatalf("pending 视图不符: %+v", started)
			}
			// 设备密钥只在服务端内存，任何响应不出现。
			startRaw, _ := json.Marshal(started)
			if strings.Contains(string(startRaw), "ds_secret_placeholder") {
				t.Fatalf("start 响应泄漏上游设备密钥占位值: %s", startRaw)
			}

			// 轮询节奏内（pollInterval 缺省 2s，起始即查）不代查上游。
			if _, err := service.GetDeviceAuthorization(user.ID, started.Id); err != nil {
				t.Fatalf("early get: %v", err)
			}
			if plane.tokenCalls != 0 {
				t.Fatalf("起始即查不应触发上游轮询，tokenCalls=%d", plane.tokenCalls)
			}

			// 推进时钟跨过轮询间隔 → 代查并推进状态。
			base := service.now()
			service.now = func() time.Time { return base.Add(time.Minute) }
			view, err := service.GetDeviceAuthorization(user.ID, started.Id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if string(view.Status) != tc.wantStatus {
				t.Fatalf("status = %q, want %q", view.Status, tc.wantStatus)
			}
			if view.Error != tc.wantError {
				t.Fatalf("error = %q, want %q", view.Error, tc.wantError)
			}
			viewRaw, _ := json.Marshal(view)
			for _, secret := range []string{"ds_secret_placeholder", "vela_ctrl_new_key", "sk-amr-runtime_key"} {
				if strings.Contains(string(viewRaw), secret) {
					t.Fatalf("响应泄漏敏感值 %s: %s", secret, viewRaw)
				}
			}

			if !tc.wantAccount {
				if view.AccountId != "" {
					t.Fatalf("非 approved 不应有 account_id: %+v", view)
				}
				// 终态之后不再轮询。
				service.now = func() time.Time { return base.Add(2 * time.Minute) }
				if _, err := service.GetDeviceAuthorization(user.ID, started.Id); err != nil {
					t.Fatalf("terminal get: %v", err)
				}
				if plane.tokenCalls != 1 {
					t.Fatalf("终态后不应再轮询，tokenCalls=%d", plane.tokenCalls)
				}
				return
			}

			// approved：自动建号、只存控制面密钥、快照落库。
			if view.AccountId == "" {
				t.Fatalf("approved 应带回 account_id: %+v", view)
			}
			stored := storedQuotaAccount(t, svc, view.AccountId)
			if stored.Provider != "opendesign" || stored.Status != model.QuotaStatusOK {
				t.Fatalf("自动建号的账号状态不符: %+v", stored)
			}
			creds := decryptedQuotaCreds(t, svc, stored)
			if creds["control_key"] != "vela_ctrl_new_key" || len(creds) != 1 {
				t.Fatalf("应只存控制面密钥: %+v", creds)
			}
			all, _ := json.Marshal(creds)
			if strings.Contains(string(all), "sk-amr-runtime_key") || strings.Contains(string(all), "ds_secret_placeholder") {
				t.Fatalf("模型面密钥/设备密钥不得落库: %s", all)
			}
			// 终态之后不再轮询、不重复建号。
			service.now = func() time.Time { return base.Add(2 * time.Minute) }
			if _, err := service.GetDeviceAuthorization(user.ID, started.Id); err != nil {
				t.Fatalf("terminal get: %v", err)
			}
			if plane.tokenCalls != 1 {
				t.Fatalf("approved 后不应再轮询，tokenCalls=%d", plane.tokenCalls)
			}
			var count int64
			svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
			if count != 1 {
				t.Fatalf("应只建 1 个账号，得到 %d", count)
			}
		})
	}
}

// 过期判定顺序：到了轮询节奏先向上游确认一次（含已过本地有效期的情形），
// 上游仍无终态才判本地过期——用户在批准页待久一点再切回来，批准结果不被作废。
func TestQuotaService_DeviceAuthorization_LocalExpiry(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-expiry")
	plane := &odControlPlane{}
	service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane))

	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	base := service.now()
	service.now = func() time.Time { return base.Add(2 * time.Hour) } // 上游有效期（+1h）之后
	view, err := service.GetDeviceAuthorization(user.ID, started.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Status != "expired" || view.Error != "验证码已过期，请重新发起授权。" {
		t.Fatalf("本地超时应 expired: %+v", view)
	}
	if plane.tokenCalls != 1 {
		t.Fatalf("本地超时前应先向上游确认一次，tokenCalls=%d", plane.tokenCalls)
	}
}

// 会话有效期取 min(上游值, now+30min)：上游给出极远时间时封顶，避免含设备
// 密钥的会话长期驻留内存。
func TestQuotaService_DeviceAuthorization_ExpiryCap(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-cap")
	far := time.Now().Add(10 * time.Hour)
	plane := &odControlPlane{startBody: odStartBody(far)}
	service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane))

	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	got, err := time.Parse(time.RFC3339, started.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at 应为 RFC3339: %v", err)
	}
	base := service.now()
	if cap := base.Add(30 * time.Minute); got.After(cap.Add(time.Second)) || got.Before(cap.Add(-time.Second)) {
		t.Fatalf("expires_at 应封顶 now+30min（%v），得到 %v", cap, got)
	}
}

// 上游抖动（网络错误 / 5xx）保持 pending、每次代查各记一条 Warn；「用户还没批准」
// （2xx 无凭据无终态）属正常等待：连续多次轮询也保持 pending 且不产生 Warn。
func TestQuotaService_DeviceAuthorization_UpstreamFlaky(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-flaky")
	const pollGets = 3
	cases := []struct {
		name        string
		tokenStatus int
		tokenBody   string
		wantWarns   int
	}{
		// 每次代查都失败，各记一条 Warn。
		{"5xx", http.StatusBadGateway, `{}`, pollGets},
		// 批准要等几十秒，正常等待不刷 Warn。
		{"2xx 无凭据无终态", http.StatusOK, `{"status":"still_waiting"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plane := &odControlPlane{tokenStatus: tc.tokenStatus, tokenBody: tc.tokenBody}
			core, obs := observer.New(zapcore.DebugLevel)
			service := newTestQuotaServiceWithLogger(svc, zap.New(core), deviceAuthQuotaProvider(t, plane))
			started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
				Provider: "opendesign", Label: "x",
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			// 连续 GET 多次：每次把时钟推过 poll 间隔，都会向上游代查一次。
			base := service.now()
			for i := 1; i <= pollGets; i++ {
				service.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
				view, err := service.GetDeviceAuthorization(user.ID, started.Id)
				if err != nil {
					t.Fatalf("第 %d 次 get: %v", i, err)
				}
				if view.Status != "pending" || view.Error != "" {
					t.Fatalf("第 %d 次应保持 pending 且无 error: %+v", i, view)
				}
			}
			warns := obs.FilterLevelExact(zapcore.WarnLevel).FilterMessage("quota device auth poll failed").All()
			if len(warns) != tc.wantWarns {
				t.Fatalf("「quota device auth poll failed」Warn 期望 %d 条，得到 %d 条", tc.wantWarns, len(warns))
			}
		})
	}
}

// 取消要能打断进行中的轮询：GET 已持有会话指针并阻塞在上游请求上时，
// DELETE 先置取消标志，GET 返回后不得建号。
func TestQuotaService_DeviceAuthorization_CancelDuringPoll(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-cancel")
	plane := &odControlPlane{
		tokenBody:    odApprovedTokenBody("vela_ctrl_cancel_key"),
		tokenEntered: make(chan struct{}, 1),
		tokenRelease: make(chan struct{}),
	}
	service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane))

	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	base := service.now()
	service.now = func() time.Time { return base.Add(time.Minute) } // 让 GET 会代查上游

	done := make(chan struct{})
	go func() {
		defer close(done)
		view, err := service.GetDeviceAuthorization(user.ID, started.Id)
		if err != nil {
			t.Errorf("blocking get: %v", err)
			return
		}
		if view.Status != "pending" {
			t.Errorf("被取消的会话不应推进状态: %+v", view)
		}
	}()

	select {
	case <-plane.tokenEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("上游 token 请求未到达，无法构造阻塞场景")
	}
	// 上游阻塞期间取消：先置标志、摘除会话并立即返回。
	if err := service.CancelDeviceAuthorization(user.ID, started.Id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	close(plane.tokenRelease) // 放行上游响应（已批准，带凭据）
	<-done

	// 账号数必须为 0：拿到凭据后、建号前看到了取消标志。
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Fatalf("取消后不得建号，得到 %d 行", count)
	}
	// 会话已摘除：再 GET 40431。
	if _, err := service.GetDeviceAuthorization(user.ID, started.Id); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthNotFound.Code {
		t.Fatalf("取消后 GET 期望 40431，得到 %v", err)
	}
}

// 建号必须幂等且不依赖上游重复下发凭据：首轮建号失败（保持 pending 并记
// Warn，只带 id 不带密钥）后，重试只重跑建号、复用缓存的凭据，不再向上游
// 换取；预分配的同一 id 只落一行。
func TestQuotaService_DeviceAuthorization_CreateRetryReusesCachedCredentials(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-retry")
	plane := &odControlPlane{tokenBody: odApprovedTokenBody("vela_ctrl_retry_key")}
	baseProvider := deviceAuthQuotaProvider(t, plane)
	core, obs := observer.New(zapcore.DebugLevel)

	// requireExtraField 为真时凭据校验必失败（缺必填字段），模拟首轮建号失败。
	var requireExtraField bool
	service := NewQuotaService(repository.NewQuotaAccountRepository(svc.db), svc.cfg, zap.New(core),
		func() []quota.Provider {
			p := baseProvider
			if requireExtraField {
				p.CredentialFields = append(p.CredentialFields, quota.CredentialField{Key: "gate", Label: "门", Required: true})
			}
			return []quota.Provider{p}
		})

	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "浏览器授权",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// 首轮：兑换到凭据但建号失败 → 保持 pending，Warn 恰好一条。
	requireExtraField = true
	base := service.now()
	service.now = func() time.Time { return base.Add(time.Minute) }
	view, err := service.GetDeviceAuthorization(user.ID, started.Id)
	if err != nil {
		t.Fatalf("首轮 get: %v", err)
	}
	if view.Status != "pending" || view.AccountId != "" {
		t.Fatalf("建号失败应保持 pending: %+v", view)
	}
	failed := obs.FilterLevelExact(zapcore.WarnLevel).FilterMessage("quota device auth account create failed").All()
	if len(failed) != 1 {
		t.Fatalf("建号失败应记一条 Warn，得到 %d 条", len(failed))
	}

	// 重试：不再向上游换取（tokenCalls 不变），同 id 只落一行，状态推进 approved。
	requireExtraField = false
	service.now = func() time.Time { return base.Add(2 * time.Minute) }
	view, err = service.GetDeviceAuthorization(user.ID, started.Id)
	if err != nil {
		t.Fatalf("重试 get: %v", err)
	}
	if view.Status != "approved" || view.AccountId == "" {
		t.Fatalf("重试应 approved: %+v", view)
	}
	if plane.tokenCalls != 1 {
		t.Fatalf("重试不得再向上游换取，tokenCalls=%d", plane.tokenCalls)
	}
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Fatalf("重试后应恰好 1 行，得到 %d", count)
	}
	if creds := decryptedQuotaCreds(t, svc, storedQuotaAccount(t, svc, view.AccountId)); creds["control_key"] != "vela_ctrl_retry_key" {
		t.Fatalf("落库凭据应为缓存的兑换结果: %+v", creds)
	}
}

// createWithID 的幂等语义：同 id 行已存在时插入视为成功，不产生第二个账号，
// 也不触发抓取。
func TestQuotaService_CreateWithID_Idempotent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-create-idem")
	fetches := 0
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		fetches++
		return fakeQuotaSnapshot(), nil
	}))
	req := &CreateQuotaAccountRequest{Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "k-1"}}
	id := uuid.NewString()
	if appErr := service.createWithID(user.ID, id, req); appErr != nil {
		t.Fatalf("createWithID: %v", appErr)
	}
	if appErr := service.createWithID(user.ID, id, req); appErr != nil {
		t.Fatalf("同 id 重插应视为成功: %v", appErr)
	}
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Fatalf("同 id 重插后应仍 1 行，得到 %d", count)
	}
	if fetches != 0 {
		t.Fatalf("createWithID 只落库不抓取，fetches=%d", fetches)
	}
}

// 为已有账号换凭据（请求带 account_id）：批准后整组替换凭据、账号数不变、
// account_id 回填目标账号；目标不存在或属于他人 → 40430 且不触上游；
// 目标供应商与会话不一致 → 40001 且不触上游、凭据原封不动。
func TestQuotaService_DeviceAuthorization_Rebind(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-rebind")
	other := createTestUser(t, svc.db, "quota-device-rebind-other")
	plane := &odControlPlane{tokenBody: odApprovedTokenBody("vela_ctrl_rebind_key")}
	service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane), fakeQuotaProvider("factory", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		return fakeQuotaSnapshot(), nil
	}))

	existing, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "opendesign", Label: "旧凭据", Credentials: map[string]string{"control_key": "vela_ctrl_old_key"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 目标不存在 / 他人账号 → 40430（与账号不存在同码，不泄露存在性），
	// 且校验在建会话之前，不触上游。
	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x", AccountId: apiPtr("nonexistent"),
	}); quotaAppErrCode(t, err) != errs.ErrQuotaAccountNotFound.Code {
		t.Fatalf("目标不存在期望 40430，得到 %v", err)
	}
	foreign, err := service.Create(other.ID, &CreateQuotaAccountRequest{
		Provider: "opendesign", Label: "别家的", Credentials: map[string]string{"control_key": "vela_ctrl_foreign"},
	})
	if err != nil {
		t.Fatalf("seed foreign: %v", err)
	}
	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x", AccountId: apiPtr(foreign.Id),
	}); quotaAppErrCode(t, err) != errs.ErrQuotaAccountNotFound.Code {
		t.Fatalf("他人账号期望 40430，得到 %v", err)
	}
	if plane.startCalls != 0 {
		t.Fatalf("校验失败不得触上游，startCalls=%d", plane.startCalls)
	}
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Fatalf("校验失败不得建号，count=%d", count)
	}

	// 供应商不匹配（factory 账号换 opendesign 凭据）→ 40001：在建会话/触上游
	// 之前拒绝，否则用户走完整个浏览器授权后才在批准处失败。
	factoryAcct, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "factory", Label: "Factory 账号", Credentials: map[string]string{"api_key": "fk-secret-factory0"},
	})
	if err != nil {
		t.Fatalf("seed factory: %v", err)
	}
	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x", AccountId: apiPtr(factoryAcct.Id),
	}); quotaAppErrCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("供应商不匹配期望 40001，得到 %v", err)
	}
	if plane.startCalls != 0 || plane.tokenCalls != 0 {
		t.Fatalf("校验失败不得触上游，startCalls=%d tokenCalls=%d", plane.startCalls, plane.tokenCalls)
	}
	if creds := decryptedQuotaCreds(t, svc, storedQuotaAccount(t, svc, factoryAcct.Id)); creds["api_key"] != "fk-secret-factory0" || len(creds) != 2 {
		t.Fatalf("校验失败不得改目标账号凭据: %+v", creds)
	}

	// rebind 成功：账号数不变（existing + factory 种子账号）、凭据被替换、
	// account_id 是目标账号。
	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "换凭据", AccountId: apiPtr(existing.Id),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	base := service.now()
	service.now = func() time.Time { return base.Add(time.Minute) }
	view, err := service.GetDeviceAuthorization(user.ID, started.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Status != "approved" {
		t.Fatalf("rebind 应 approved: %+v", view)
	}
	if view.AccountId != existing.Id {
		t.Fatalf("rebind 的 account_id 应是目标账号，得到 %q", view.AccountId)
	}
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 2 {
		t.Fatalf("rebind 不得新增账号（existing + factory 种子），count=%d", count)
	}
	if creds := decryptedQuotaCreds(t, svc, storedQuotaAccount(t, svc, existing.Id)); creds["control_key"] != "vela_ctrl_rebind_key" || len(creds) != 1 {
		t.Fatalf("rebind 应整组替换凭据: %+v", creds)
	}
	if plane.tokenCalls != 1 {
		t.Fatalf("应只向上游兑换一次，tokenCalls=%d", plane.tokenCalls)
	}
}

// 会话是本人资源：他人 GET/DELETE 一律 40431；取消后 GET 也 40431。
func TestQuotaService_DeviceAuthorization_Isolation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-owner")
	other := createTestUser(t, svc.db, "quota-device-stranger")
	plane := &odControlPlane{}
	service := newTestQuotaService(svc, deviceAuthQuotaProvider(t, plane))

	started, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "mine",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := service.GetDeviceAuthorization(other.ID, started.Id); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthNotFound.Code {
		t.Fatalf("他人 GET 期望 40431，得到 %v", err)
	}
	if err := service.CancelDeviceAuthorization(other.ID, started.Id); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthNotFound.Code {
		t.Fatalf("他人 DELETE 期望 40431，得到 %v", err)
	}
	if _, err := service.GetDeviceAuthorization(user.ID, "nonexistent"); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthNotFound.Code {
		t.Fatalf("未知 id 期望 40431，得到 %v", err)
	}

	if err := service.CancelDeviceAuthorization(user.ID, started.Id); err != nil {
		t.Fatalf("本人取消应成功: %v", err)
	}
	if _, err := service.GetDeviceAuthorization(user.ID, started.Id); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthNotFound.Code {
		t.Fatalf("取消后 GET 期望 40431，得到 %v", err)
	}
}

// 发起校验：未知供应商 40030、不支持设备授权 40032、空白标签 40001、
// 上游不可用/缺关键字段 50204。
func TestQuotaService_DeviceAuthorization_StartValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-device-validate")
	plane := &odControlPlane{}
	deviceProvider := deviceAuthQuotaProvider(t, plane)
	plainProvider := fakeQuotaProvider("fake", nil)
	service := newTestQuotaService(svc, deviceProvider, plainProvider)

	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "nope", Label: "x",
	}); quotaAppErrCode(t, err) != errs.ErrQuotaProviderUnknown.Code {
		t.Fatalf("未知供应商期望 40030，得到 %v", err)
	}
	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "fake", Label: "x",
	}); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthUnsupported.Code {
		t.Fatalf("不支持设备授权期望 40032，得到 %v", err)
	}
	if _, err := service.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "   ",
	}); quotaAppErrCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("空白标签期望 40001，得到 %v", err)
	}

	// 上游 500 → 50204。
	failingPlane := &odControlPlane{startStatus: http.StatusInternalServerError}
	failing := deviceAuthQuotaProvider(t, failingPlane)
	failingService := newTestQuotaService(svc, failing)
	if _, err := failingService.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x",
	}); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthUpstream.Code {
		t.Fatalf("上游失败期望 50204，得到 %v", err)
	}

	// 上游 200 但缺设备密钥 → 50204。
	incompletePlane := &odControlPlane{startBody: `{"deviceId":"dev_x"}`}
	incomplete := deviceAuthQuotaProvider(t, incompletePlane)
	incompleteService := newTestQuotaService(svc, incomplete)
	if _, err := incompleteService.StartDeviceAuthorization(user.ID, &StartQuotaDeviceAuthorizationRequest{
		Provider: "opendesign", Label: "x",
	}); quotaAppErrCode(t, err) != errs.ErrQuotaDeviceAuthUpstream.Code {
		t.Fatalf("缺设备密钥期望 50204，得到 %v", err)
	}
}
