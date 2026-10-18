package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/quota"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeQuotaProvider 是测试用供应商：单必填密文凭据 + 可注入的 fetch。
func fakeQuotaProvider(id string, fetch quota.Fetcher) quota.Provider {
	return quota.Provider{
		ID:             id,
		Name:           "Fake " + id,
		PlanHint:       "Pro",
		DefaultBaseURL: "https://fake.example.com",
		CredentialFields: []quota.CredentialField{
			{Key: "api_key", Label: "API Key", Secret: true, Required: true},
			{Key: "region", Label: "Region", Secret: false, Required: false},
		},
		Fetch: fetch,
	}
}

func fakeQuotaSnapshot() quota.Snapshot {
	return quota.Snapshot{
		PlanName: "Pro",
		Windows: []quota.Window{
			{Kind: quota.WindowFiveHour, Label: "5 小时", UsedPercent: quota.PercentPtr(38.0), Unit: quota.UnitPercent},
			{Kind: quota.WindowMonthly, Label: "每月", UsedPercent: quota.PercentPtr(12.5), Unit: quota.UnitPercent},
		},
		Balance:   &quota.Balance{Label: "剩余额度", Amount: 12.5, Currency: "USD"},
		FetchedAt: time.Now().UTC(),
	}
}

func newTestQuotaService(svc *testServices, providers ...quota.Provider) *QuotaService {
	return newTestQuotaServiceWithLogger(svc, zap.NewNop(), providers...)
}

// newTestQuotaServiceWithLogger 允许注入带观察核的 logger（如 zaptest/observer），
// 用于断言服务写没写某条日志。
func newTestQuotaServiceWithLogger(svc *testServices, logger *zap.Logger, providers ...quota.Provider) *QuotaService {
	repo := repository.NewQuotaAccountRepository(svc.db)
	return NewQuotaService(repo, svc.cfg, logger, func() []quota.Provider { return providers })
}

func storedQuotaAccount(t *testing.T, svc *testServices, id string) *model.QuotaAccount {
	t.Helper()
	var account model.QuotaAccount
	if err := svc.db.First(&account, "id = ?", id).Error; err != nil {
		t.Fatalf("load quota account %q: %v", id, err)
	}
	return &account
}

func decryptedQuotaCreds(t *testing.T, svc *testServices, account *model.QuotaAccount) map[string]string {
	t.Helper()
	raw, err := crypto.Decrypt(account.CredentialsEncrypted, []byte(svc.cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("decrypt creds: %v", err)
	}
	var creds map[string]string
	if err := json.Unmarshal(raw, &creds); err != nil {
		t.Fatalf("unmarshal creds json: %v", err)
	}
	return creds
}

func quotaAppErrCode(t *testing.T, err error) int {
	t.Helper()
	var appErr *errs.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %v", err)
	}
	return appErr.Code
}

// Create：凭据整组加密落库（JSON 密文），响应只回末 4 位尾号，正文无明文；
// 同步抓取成功后快照与套餐名落库。
func TestQuotaService_Create_EncryptsAndFetches(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-create")
	var sawCreds quota.Credentials
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(ctx context.Context, client *http.Client, baseURL string, creds quota.Credentials) (quota.Snapshot, error) {
		sawCreds = creds
		if baseURL != "https://fake.example.com" {
			t.Fatalf("baseURL = %q", baseURL)
		}
		return fakeQuotaSnapshot(), nil
	}))

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "我的 Pro",
		Credentials: map[string]string{"api_key": "fk-secret-ab12", "region": "  us  ", "extra": "ignored"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Status != "ok" || resp.PlanName != "Pro" || resp.CredentialHint != "…ab12" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.Windows) != 2 || resp.Balance == nil || resp.Balance.Amount != 12.5 {
		t.Fatalf("expected snapshot data in response, got %+v", resp)
	}
	if sawCreds["api_key"] != "fk-secret-ab12" || sawCreds["region"] != "us" {
		t.Fatalf("fetcher must see trimmed declared creds, got %+v", sawCreds)
	}
	if _, extra := sawCreds["extra"]; extra {
		t.Fatal("undeclared credential keys must not reach the fetcher")
	}

	stored := storedQuotaAccount(t, svc, resp.Id)
	if len(stored.CredentialsEncrypted) == 0 || strings.Contains(string(stored.CredentialsEncrypted), "fk-secret") {
		t.Fatal("credentials must be stored encrypted")
	}
	creds := decryptedQuotaCreds(t, svc, stored)
	if creds["api_key"] != "fk-secret-ab12" || creds["region"] != "us" || len(creds) != 2 {
		t.Fatalf("stored creds mismatch: %+v", creds)
	}
	if stored.Status != model.QuotaStatusOK || stored.SnapshotJSON == "" || stored.LastFetchedAt == nil || stored.LastSuccessAt == nil {
		t.Fatalf("expected successful fetch state, got %+v", stored)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "fk-secret-ab12") {
		t.Fatalf("response leaks credential plaintext: %s", body)
	}
}

// Create：供应商未知 40030；必填凭据缺失/空白 40031；可选字段可空。
func TestQuotaService_Create_Validation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-validate")
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", nil))

	if _, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "nope", Label: "x", Credentials: map[string]string{"api_key": "k"},
	}); quotaAppErrCode(t, err) != errs.ErrQuotaProviderUnknown.Code {
		t.Fatalf("expected 40030, got %v", err)
	}
	if _, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "x", Credentials: map[string]string{},
	}); quotaAppErrCode(t, err) != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("expected 40031, got %v", err)
	}
	if _, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "   "},
	}); quotaAppErrCode(t, err) != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("expected 40031 for blank cred, got %v", err)
	}
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Fatal("validation failures must not persist rows")
	}
}

// List：只返回本人账号；响应不含密文。
func TestQuotaService_List_Isolation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-list")
	other := createTestUser(t, svc.db, "quota-list-other")
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		return fakeQuotaSnapshot(), nil
	}))

	mine, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "mine", Credentials: map[string]string{"api_key": "mine-key"}})
	if err != nil {
		t.Fatalf("create mine: %v", err)
	}
	foreign, err := service.Create(other.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "foreign", Credentials: map[string]string{"api_key": "foreign-key"}})
	if err != nil {
		t.Fatalf("create foreign: %v", err)
	}

	items, err := service.List(user.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].Id != mine.Id {
		t.Fatalf("expected only own account, got %+v", items)
	}
	body, _ := json.Marshal(items)
	if strings.Contains(string(body), "mine-key") || strings.Contains(string(body), "foreign-key") {
		t.Fatalf("list leaks credentials: %s", body)
	}

	// 他人账号读/改/删/刷新一律 40430
	if _, err := service.Update(user.ID, foreign.Id, &UpdateQuotaAccountRequest{Label: apiPtr("hijack")}); quotaAppErrCode(t, err) != errs.ErrQuotaAccountNotFound.Code {
		t.Fatalf("expected 40430 on foreign update, got %v", err)
	}
	if err := service.Delete(user.ID, foreign.Id); quotaAppErrCode(t, err) != errs.ErrQuotaAccountNotFound.Code {
		t.Fatalf("expected 40430 on foreign delete, got %v", err)
	}
	if _, err := service.Refresh(user.ID, foreign.Id); quotaAppErrCode(t, err) != errs.ErrQuotaAccountNotFound.Code {
		t.Fatalf("expected 40430 on foreign refresh, got %v", err)
	}
}

// 失败保留上一次成功快照与数据时间；退避 next_retry_at = now + min(2^n,30) 分钟。
func TestQuotaService_FetchFailureKeepsSnapshot(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-fail")
	// fakeNow 取未来一年，保证手动刷新不被冷却拦截（数据时间来自 fetcher 的真实时钟）。
	fakeNow := time.Now().UTC().AddDate(1, 0, 0)
	failWith := error(quota.ProviderError("供应商接口异常", 502))
	calls := 0
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		calls++
		if calls == 1 {
			return fakeQuotaSnapshot(), nil
		}
		return quota.Snapshot{}, failWith
	}))
	service.now = func() time.Time { return fakeNow }

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "k-1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	firstFetched := *resp.FetchedAt

	// 连续两次失败：failure_count 递增、2^n 退避、快照与数据时间不动。
	for i, wantMinutes := range []int{2, 4} {
		service.now = func() time.Time { return fakeNow.Add(time.Duration(i+1) * time.Hour) }
		if _, err := service.Refresh(user.ID, resp.Id); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
		stored := storedQuotaAccount(t, svc, resp.Id)
		if stored.Status != model.QuotaStatusProviderError || stored.LastError != "供应商接口异常" {
			t.Fatalf("expected provider_error with message, got %+v", stored)
		}
		if stored.FailureCount != i+1 {
			t.Fatalf("failure_count = %d, want %d", stored.FailureCount, i+1)
		}
		wantNext := service.now().Add(time.Duration(wantMinutes) * time.Minute)
		if stored.NextRetryAt == nil || stored.NextRetryAt.Sub(wantNext).Abs() > time.Second {
			t.Fatalf("next_retry_at = %v, want ~%v", stored.NextRetryAt, wantNext)
		}
		if stored.LastSuccessAt == nil || !strings.HasPrefix(firstFetched, stored.LastSuccessAt.Format("2006-01-02")) {
			t.Fatalf("last_success_at moved on failure: %v vs %v", stored.LastSuccessAt, firstFetched)
		}
		var snap quota.Snapshot
		if err := json.Unmarshal([]byte(stored.SnapshotJSON), &snap); err != nil || snap.PlanName != "Pro" || len(snap.Windows) != 2 {
			t.Fatalf("success snapshot must be preserved, got %q err=%v", stored.SnapshotJSON, err)
		}
	}

	// 凭据失效（401）→ credential_invalid。
	failWith = quota.CredentialError("凭据已失效", 401)
	service.now = func() time.Time { return fakeNow.Add(3 * time.Hour) }
	if _, err := service.Refresh(user.ID, resp.Id); err != nil {
		t.Fatalf("refresh credential: %v", err)
	}
	stored := storedQuotaAccount(t, svc, resp.Id)
	if stored.Status != model.QuotaStatusCredentialInvalid {
		t.Fatalf("expected credential_invalid, got %q", stored.Status)
	}
}

// 退避上限：failure_count 到 5 后 next_retry_at 封顶 30 分钟。
func TestQuotaService_BackoffCap(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-backoff")
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", nil))
	now := time.Now().UTC()
	account := &model.QuotaAccount{
		ID: uuid.NewString(), UserID: user.ID, Provider: "fake", Label: "x",
		CredentialHint: "…x", Status: model.QuotaStatusProviderError,
		FailureCount: 8, SnapshotJSON: "",
	}
	enc, _ := crypto.Encrypt([]byte(`{"api_key":"k"}`), []byte(svc.cfg.Encryption.Key))
	account.CredentialsEncrypted = enc
	if err := svc.db.Create(account).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	service.storeFailure(account, model.QuotaStatusProviderError, "err", now)
	stored := storedQuotaAccount(t, svc, account.ID)
	if stored.FailureCount != 9 {
		t.Fatalf("failure_count = %d", stored.FailureCount)
	}
	want := now.Add(30 * time.Minute)
	if stored.NextRetryAt == nil || stored.NextRetryAt.Sub(want).Abs() > time.Second {
		t.Fatalf("next_retry_at = %v, want ~%v (30min cap)", stored.NextRetryAt, want)
	}
}

// 手动刷新冷却：距上次成功抓取 < cooldown 返回 42900。
func TestQuotaService_Refresh_Cooldown(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-cooldown")
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		return fakeQuotaSnapshot(), nil
	}))
	svc.cfg.Quota.ManualRefreshCooldownSeconds = 30

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "k"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 创建时刚成功抓过 → 立即刷新命中冷却。
	_, err = service.Refresh(user.ID, resp.Id)
	if code := quotaAppErrCode(t, err); code != errs.ErrQuotaRefreshTooOften.Code {
		t.Fatalf("expected 42900, got %v", err)
	}
	var appErr *errs.AppError
	_ = errors.As(err, &appErr)
	if !strings.Contains(appErr.Message, "秒") {
		t.Fatalf("42900 message must carry refresh-after hint, got %q", appErr.Message)
	}
}

// Probe：不落库、不改账号；成功/失败都走 QuotaProbe 形状。
func TestQuotaService_Probe(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-probe")
	shouldFail := false
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		if shouldFail {
			return quota.Snapshot{}, quota.CredentialError("token 失效", 401)
		}
		return fakeQuotaSnapshot(), nil
	}))

	probe, err := service.Probe(&TestQuotaCredentialsRequest{Provider: "fake", Credentials: map[string]string{"api_key": "k"}})
	if err != nil {
		t.Fatalf("probe ok: %v", err)
	}
	if probe.Status != "ok" || probe.PlanName != "Pro" || len(probe.Windows) != 2 {
		t.Fatalf("unexpected probe: %+v", probe)
	}

	shouldFail = true
	probe, err = service.Probe(&TestQuotaCredentialsRequest{Provider: "fake", Credentials: map[string]string{"api_key": "k"}})
	if err != nil {
		t.Fatalf("probe fail: %v", err)
	}
	if probe.Status != "credential_invalid" || probe.Error != "token 失效" || probe.Detail != "HTTP 401" {
		t.Fatalf("unexpected failed probe: %+v", probe)
	}

	if _, err := service.Probe(&TestQuotaCredentialsRequest{Provider: "nope", Credentials: map[string]string{}}); quotaAppErrCode(t, err) != errs.ErrQuotaProviderUnknown.Code {
		t.Fatalf("expected 40030, got %v", err)
	}
	if _, err := service.Probe(&TestQuotaCredentialsRequest{Provider: "fake", Credentials: map[string]string{}}); quotaAppErrCode(t, err) != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("expected 40031, got %v", err)
	}

	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Fatal("probe must not persist accounts")
	}
}

// Update：只改标签不触发抓取；整组替换凭据触发重抓并更新尾号。
func TestQuotaService_Update(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-update")
	calls := 0
	var lastCreds quota.Credentials
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(ctx context.Context, c *http.Client, b string, creds quota.Credentials) (quota.Snapshot, error) {
		calls++
		lastCreds = creds
		return fakeQuotaSnapshot(), nil
	}))
	svc.cfg.Quota.ManualRefreshCooldownSeconds = 0

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "old", Credentials: map[string]string{"api_key": "key-v1-xx"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if calls != 1 {
		t.Fatalf("create must fetch once, got %d", calls)
	}

	renamed, err := service.Update(user.ID, resp.Id, &UpdateQuotaAccountRequest{Label: apiPtr("new-label")})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Label != "new-label" || calls != 1 {
		t.Fatalf("label-only update must not refetch (calls=%d): %+v", calls, renamed)
	}

	newCreds := api.QuotaCredentials{"api_key": "key-v2-zz99"}
	updated, err := service.Update(user.ID, resp.Id, &UpdateQuotaAccountRequest{Credentials: &newCreds})
	if err != nil {
		t.Fatalf("update creds: %v", err)
	}
	if calls != 2 || lastCreds["api_key"] != "key-v2-zz99" {
		t.Fatalf("credential update must refetch with new creds, calls=%d creds=%+v", calls, lastCreds)
	}
	if updated.CredentialHint != "…zz99" || updated.Status != "ok" {
		t.Fatalf("expected new hint + ok status, got %+v", updated)
	}
	stored := storedQuotaAccount(t, svc, resp.Id)
	if creds := decryptedQuotaCreds(t, svc, stored); creds["api_key"] != "key-v2-zz99" {
		t.Fatalf("stored creds not replaced: %+v", creds)
	}
}

// nextRetryAfterSuccess：窗口 reset_at 落在 interval 内 → next_retry_at 提前到 reset+30s。
func TestQuotaService_PredictiveRetry(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-predict")
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	svc.cfg.Quota.IntervalMinutes = 10
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		reset := now.Add(3 * time.Minute)
		snap := fakeQuotaSnapshot()
		snap.Windows[0].ResetAt = &reset
		return snap, nil
	}))
	service.now = func() time.Time { return now }

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "k"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := storedQuotaAccount(t, svc, resp.Id)
	want := now.Add(3*time.Minute + 30*time.Second)
	if stored.NextRetryAt == nil || stored.NextRetryAt.Sub(want).Abs() > time.Second {
		t.Fatalf("next_retry_at = %v, want ~%v (reset+30s)", stored.NextRetryAt, want)
	}
}

// 契约数值字段必须保持 float64（openapi format: double）——金额/百分比降成
// float32 是静默精度损失，用编译期断言钉住生成类型。
var (
	_ float64  = api.QuotaBalance{}.Amount
	_ *float64 = api.QuotaWindow{}.UsedPercent
	_ *float64 = api.QuotaWindow{}.Used
	_ *float64 = api.QuotaWindow{}.Limit
)

// B1：健康账号的 next_retry_at 成功后必须落绝对时间（now+interval），
// 不能是 NULL——否则失败账号每次退避唤醒都会把它一起重抓。
func TestQuotaScheduler_HealthyNotAmplifiedByFailures(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-amp")
	svc.cfg.Quota.IntervalMinutes = 10
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	fetches := map[string]int{}
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(_ context.Context, _ *http.Client, _ string, creds quota.Credentials) (quota.Snapshot, error) {
		key := creds["api_key"]
		fetches[key]++
		if key == "bad" {
			return quota.Snapshot{}, quota.ProviderError("接口 502", 502)
		}
		return fakeQuotaSnapshot(), nil
	}))
	service.now = func() time.Time { return t0 }

	good, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "good", Credentials: map[string]string{"api_key": "good"}})
	if err != nil {
		t.Fatalf("create good: %v", err)
	}
	if _, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "bad", Credentials: map[string]string{"api_key": "bad"}}); err != nil {
		t.Fatalf("create bad: %v", err)
	}

	// 健康账号下一次抓取 = t0 + interval（绝对时间，非 NULL）。
	stored := storedQuotaAccount(t, svc, good.Id)
	wantNext := t0.Add(10 * time.Minute)
	if stored.NextRetryAt == nil || stored.NextRetryAt.Sub(wantNext).Abs() > time.Second {
		t.Fatalf("healthy next_retry_at = %v, want ~%v", stored.NextRetryAt, wantNext)
	}

	// 失败账号的退避（t0+2min）到期唤醒一轮：只有它该被重抓。
	sched := NewQuotaScheduler(service, repository.NewQuotaAccountRepository(svc.db), svc.cfg, zap.NewNop())
	service.now = func() time.Time { return t0.Add(2 * time.Minute) }
	sched.runRound(t0.Add(2 * time.Minute))
	if fetches["good"] != 1 {
		t.Fatalf("healthy account refetched on failing account's backoff wake: fetches=%d", fetches["good"])
	}
	if fetches["bad"] != 2 {
		t.Fatalf("failing account must be retried at backoff expiry: fetches=%d", fetches["bad"])
	}
}

// B2：fetcher 时钟偏快产出未来的 fetched_at → last_fetched_at 夹到 now，
// 否则手动刷新冷却 now-last 恒负，永久 42900 且每次成功都自我续期。
func TestQuotaService_FutureFetchedAtClamped(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-future")
	svc.cfg.Quota.ManualRefreshCooldownSeconds = 30
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		snap := fakeQuotaSnapshot()
		snap.FetchedAt = t0.Add(time.Hour) // 比服务端时钟快 1 小时
		return snap, nil
	}))
	service.now = func() time.Time { return t0 }

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{Provider: "fake", Label: "x", Credentials: map[string]string{"api_key": "k"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := storedQuotaAccount(t, svc, resp.Id)
	if stored.LastFetchedAt == nil || stored.LastFetchedAt.Sub(t0).Abs() > time.Second {
		t.Fatalf("last_fetched_at must be clamped to now (%v), got %v", t0, stored.LastFetchedAt)
	}

	// 冷却过后刷新必须能放行——不被那个未来数据时间锁死。
	service.now = func() time.Time { return t0.Add(time.Minute) }
	if _, err := service.Refresh(user.ID, resp.Id); err != nil {
		t.Fatalf("refresh after cooldown must not be locked by future fetched_at: %v", err)
	}
	stored = storedQuotaAccount(t, svc, resp.Id)
	if stored.LastFetchedAt == nil || stored.LastFetchedAt.Sub(t0.Add(time.Minute)).Abs() > time.Second {
		t.Fatalf("refetched last_fetched_at must clamp to new now, got %v", stored.LastFetchedAt)
	}
}

// B3：label 显式传入但 trim 后为空 → 40001；Create 只存 trim 后的值。
// binding 的 min=1 按字符数计，纯空白能漏过，service 必须兜底。
func TestQuotaService_LabelValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-label")
	fetches := 0
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		fetches++
		return fakeQuotaSnapshot(), nil
	}))

	if _, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "   ", Credentials: map[string]string{"api_key": "k"},
	}); quotaAppErrCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("blank label create = %v, want 40001", err)
	}
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 || fetches != 0 {
		t.Fatalf("blank label must not persist or fetch (count=%d fetches=%d)", count, fetches)
	}

	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "  我的 Pro  ", Credentials: map[string]string{"api_key": "k"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Label != "我的 Pro" {
		t.Fatalf("label must be stored trimmed, got %q", resp.Label)
	}

	// 显式空白/空串都拒绝；校验在凭据替换之前，不产生副作用（不重抓）。
	newCreds := api.QuotaCredentials{"api_key": "key-v2"}
	if _, err := service.Update(user.ID, resp.Id, &UpdateQuotaAccountRequest{Label: apiPtr("   "), Credentials: &newCreds}); quotaAppErrCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("blank label update = %v, want 40001", err)
	}
	if _, err := service.Update(user.ID, resp.Id, &UpdateQuotaAccountRequest{Label: apiPtr("")}); quotaAppErrCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("empty label update = %v, want 40001", err)
	}
	if fetches != 1 {
		t.Fatalf("rejected label update must not trigger credential refetch (fetches=%d)", fetches)
	}
	if stored := storedQuotaAccount(t, svc, resp.Id); stored.Label != "我的 Pro" {
		t.Fatalf("label must stay %q, got %q", "我的 Pro", stored.Label)
	}
}

// runRound 只抓到期账号；nextWait 会被最早的未来 next_retry_at 提前。
func TestQuotaScheduler_Round(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-sched")
	fetches := 0
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		fetches++
		return fakeQuotaSnapshot(), nil
	}))
	repo := repository.NewQuotaAccountRepository(svc.db)
	cfg := svc.cfg
	cfg.Quota.IntervalMinutes = 10
	sched := NewQuotaScheduler(service, repo, cfg, zap.NewNop())
	now := time.Now().UTC()
	sched.now = func() time.Time { return now }

	enc, _ := crypto.Encrypt([]byte(`{"api_key":"k"}`), []byte(cfg.Encryption.Key))
	due := &model.QuotaAccount{ID: uuid.NewString(), UserID: user.ID, Provider: "fake", Label: "due", CredentialHint: "…x", Status: model.QuotaStatusPending, SnapshotJSON: "", CredentialsEncrypted: enc}
	future := now.Add(time.Hour)
	later := &model.QuotaAccount{ID: uuid.NewString(), UserID: user.ID, Provider: "fake", Label: "later", CredentialHint: "…x", Status: model.QuotaStatusPending, SnapshotJSON: "", CredentialsEncrypted: enc, NextRetryAt: &future}
	if err := svc.db.Create(due).Error; err != nil {
		t.Fatalf("seed due: %v", err)
	}
	if err := svc.db.Create(later).Error; err != nil {
		t.Fatalf("seed later: %v", err)
	}

	sched.runRound(now)
	if fetches != 1 {
		t.Fatalf("expected 1 due fetch, got %d", fetches)
	}
	stored := storedQuotaAccount(t, svc, due.ID)
	if stored.Status != model.QuotaStatusOK {
		t.Fatalf("due account must be ok after round, got %q", stored.Status)
	}
	if s := storedQuotaAccount(t, svc, later.ID); s.Status != model.QuotaStatusPending {
		t.Fatalf("future account must stay pending, got %q", s.Status)
	}

	// later 的 next_retry_at（+1h）比常规 10min 更晚 → 按 interval 走；
	// 把它改成 +5min 后 nextWait 应提前到 ~5min。
	earlier := now.Add(5 * time.Minute)
	svc.db.Model(&model.QuotaAccount{}).Where("id = ?", later.ID).Update("next_retry_at", earlier)
	if wait := sched.nextWait(now); wait != 5*time.Minute {
		t.Fatalf("nextWait = %v, want 5m", wait)
	}
}

// 短凭据不得把整段（或大半段）明文当尾号回显：credential_hint 会随账号列表
// 返回（含 API Key 只读路径），1–7 位凭据的「末 4 位」就是明文或大半个明文。
func TestQuotaService_CredentialHintMasksShortSecrets(t *testing.T) {
	provider := fakeQuotaProvider("fake", func(ctx context.Context, client *http.Client, baseURL string, creds quota.Credentials) (quota.Snapshot, error) {
		return fakeQuotaSnapshot(), nil
	})

	cases := []struct {
		secret string
		want   string
	}{
		{"a", credentialHintMasked},
		{"abcd", credentialHintMasked},
		{"abcdefg", credentialHintMasked},
		{"abcdefgh", "…efgh"},
		{"fk-secret-ab12", "…ab12"},
		{"凭据凭据凭据凭据凭", "…据凭据凭"},
	}
	for _, tc := range cases {
		if got := credentialHint(provider, quota.Credentials{"api_key": tc.secret}); got != tc.want {
			t.Fatalf("credentialHint(%q) = %q, want %q", tc.secret, got, tc.want)
		}
	}

	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-short-hint")
	service := newTestQuotaService(svc, provider)

	// 3 位凭据：响应与账号列表里都不能出现明文（用 'q' 避开 UUID 的十六进制字符）。
	resp, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "fake", Label: "短凭据", Credentials: map[string]string{"api_key": "qqq"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.CredentialHint != credentialHintMasked {
		t.Fatalf("short secret hint = %q, want %q", resp.CredentialHint, credentialHintMasked)
	}
	accounts, err := service.List(user.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	raw, err := json.Marshal(accounts)
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}
	if strings.Contains(string(raw), "qqq") {
		t.Fatalf("short secret leaked into account list: %s", raw)
	}
	// 数据库里存的仍是完整密文，可正常抓取。
	if creds := decryptedQuotaCreds(t, svc, storedQuotaAccount(t, svc, resp.Id)); creds["api_key"] != "qqq" {
		t.Fatalf("stored creds = %+v, want api_key qqq", creds)
	}
}

// HTTP 侧在调度器已经睡下之后写入更早的 next_retry_at（新建账号抓取失败 →
// 2 分钟后重试），Kick 必须让循环重算等待，而不是等满原来的 interval。
func TestQuotaScheduler_KickWakesEarlierRetry(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-kick")
	fetched := make(chan struct{}, 4)
	service := newTestQuotaService(svc, fakeQuotaProvider("fake", func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		select {
		case fetched <- struct{}{}:
		default:
		}
		return fakeQuotaSnapshot(), nil
	}))
	cfg := svc.cfg
	cfg.Quota.IntervalMinutes = 60
	cfg.Quota.RunOnStartup = false
	repo := repository.NewQuotaAccountRepository(svc.db)
	sched := NewQuotaScheduler(service, repo, cfg, zap.NewNop())
	sched.Start()
	defer sched.Stop()

	// 先让循环按「没有任何账号」算出 60 分钟的等待并睡下。
	time.Sleep(100 * time.Millisecond)

	enc, err := crypto.Encrypt([]byte(`{"api_key":"k"}`), []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt creds: %v", err)
	}
	retryAt := time.Now().UTC().Add(50 * time.Millisecond)
	account := &model.QuotaAccount{
		ID: uuid.NewString(), UserID: user.ID, Provider: "fake", Label: "backoff",
		CredentialHint: "…x", Status: model.QuotaStatusProviderError,
		CredentialsEncrypted: enc, NextRetryAt: &retryAt,
	}
	if err := svc.db.Create(account).Error; err != nil {
		t.Fatalf("seed account: %v", err)
	}
	sched.Kick()

	select {
	case <-fetched:
	case <-time.After(3 * time.Second):
		t.Fatal("Kick 后 3s 内没有重抓——调度器仍在睡原来的 60 分钟定时器")
	}
	// 抓取协程在 fake provider 返回后才落库；收到 fetched 只是开始抓取，立刻
	// 断言会和落库竞态。有界轮询到状态写完。
	deadline := time.Now().Add(3 * time.Second)
	for {
		stored := storedQuotaAccount(t, svc, account.ID)
		if stored.Status == model.QuotaStatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("重抓后账号应为 ok，得到 %q", stored.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 网页「API Key」页的 sk-amr-… 模型面密钥在 create/update/probe 上都明确 40031（不发请求）。
func TestQuotaService_SkAmrKeyRejectedOnCreateUpdateAndProbe(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "quota-skamr")
	realProvider, ok := quota.Lookup(nil, "opendesign")
	if !ok {
		t.Fatalf("真实注册表应含 opendesign")
	}
	// 保留真实的 Validate 钩子，但取数与地址换成假的：校验失败在发请求前，
	// 而种子账号的有效凭据不应触网。
	realProvider.Fetch = func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		return fakeQuotaSnapshot(), nil
	}
	realProvider.DefaultBaseURL = "https://opendesign.test.invalid"
	service := newTestQuotaService(svc, realProvider)

	_, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "opendesign", Label: "x", Credentials: map[string]string{"control_key": "sk-amr-wrong"},
	})
	if code := quotaAppErrCode(t, err); code != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("create 期望 40031，得到 %v", err)
	}
	var appErr *errs.AppError
	_ = errors.As(err, &appErr)
	if !strings.Contains(appErr.Message, "调模型") {
		t.Fatalf("create 错误消息应说明 sk-amr- 是模型面密钥: %q", appErr.Message)
	}

	probe, err := service.Probe(&TestQuotaCredentialsRequest{
		Provider: "opendesign", Credentials: map[string]string{"control_key": "sk-amr-wrong"},
	})
	if code := quotaAppErrCode(t, err); code != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("probe 期望 40031，得到 %v", err)
	}
	if probe != nil {
		t.Fatalf("校验失败不应返回探测结果: %+v", probe)
	}

	// update 路径（换凭据）：同样的密钥同样拦下，已有账号不被改动。
	existing, err := service.Create(user.ID, &CreateQuotaAccountRequest{
		Provider: "opendesign", Label: "x", Credentials: map[string]string{"control_key": "vela_ctrl_good"},
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	badCreds := api.QuotaCredentials{"control_key": "sk-amr-wrong"}
	if _, err := service.Update(user.ID, existing.Id, &UpdateQuotaAccountRequest{Credentials: &badCreds}); quotaAppErrCode(t, err) != errs.ErrQuotaCredentialsInvalid.Code {
		t.Fatalf("update 期望 40031，得到 %v", err)
	}
	if creds := decryptedQuotaCreds(t, svc, storedQuotaAccount(t, svc, existing.Id)); creds["control_key"] != "vela_ctrl_good" {
		t.Fatalf("被拒绝的换凭据不得改动已有账号: %+v", creds)
	}

	// 除种子账号外零副作用。
	var count int64
	svc.db.Model(&model.QuotaAccount{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Fatalf("被拒绝的凭据不得落库，count=%d", count)
	}
}
