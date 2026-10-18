package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IntegrationService（INT-119）定向测试：用户级来源优先级、PUT 三态、并发保存
// 不丢更新、检测映射与条件落库、密文损坏不回落 config、webhook 密钥按 App ID
// 定位。

func integrationTestService(t *testing.T) (*IntegrationService, *repository.IntegrationRepository, *config.Config, *gorm.DB) {
	t.Helper()
	return integrationTestServiceDSN(t, "file:"+uuid.NewString()+"?mode=memory&cache=shared")
}

// 并发用例的库带 busy_timeout：两个写事务相遇时等待而非立即报 locked。
func integrationTestServiceDSN(t *testing.T, dsn string) (*IntegrationService, *repository.IntegrationRepository, *config.Config, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Integration{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com/"},
	}
	repo := repository.NewIntegrationRepository(db)
	svc := NewIntegrationService(repo, cfg, zap.NewNop())
	return svc, repo, cfg, db
}

// stubVerify 注入固定检测序列，并记录每次收到的身份。互斥保护：并发保存的
// 用例里检测与断言并行发生，闭包状态必须串行化。
func stubVerify(svc *IntegrationService, detections ...githubapp.Detection) (appIDs *[]string, keyPEMs *[][]byte) {
	appIDs = &[]string{}
	keyPEMs = &[][]byte{}
	idx := 0
	var mu sync.Mutex
	svc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		mu.Lock()
		defer mu.Unlock()
		*appIDs = append(*appIDs, appID)
		*keyPEMs = append(*keyPEMs, keyPEM)
		d := detections[idx%len(detections)]
		idx++
		return d
	})
	return appIDs, keyPEMs
}

func keyPEMString(t *testing.T) string {
	t.Helper()
	pem, err := githubapp.GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return string(pem)
}

func appErr(t *testing.T, err error) *errs.AppError {
	t.Helper()
	var appErr *errs.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("err = %v, want AppError", err)
	}
	return appErr
}

// TestIntegration_UpdateValidation PUT 的参数校验与首次必须给私钥。
func TestIntegration_UpdateValidation(t *testing.T) {
	svc, _, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	// App ID 非数字 / 为空。
	_, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "12a45", PrivateKeyPem: &key})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "App ID 必须是数字" {
		t.Fatalf("non-numeric = [%d %s]", got.Code, got.Message)
	}
	_, err = svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "  ", PrivateKeyPem: &key})
	if got := appErr(t, err); got.Code != 40001 {
		t.Fatalf("blank = [%d %s]", got.Code, got.Message)
	}

	// 私钥给的不是 RSA PEM。
	bad := "definitely not pem"
	_, err = svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "123", PrivateKeyPem: &bad})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "私钥不是有效的 RSA PEM" {
		t.Fatalf("bad pem = [%d %s]", got.Code, got.Message)
	}

	// 首次保存（无数据库私钥也无 config 私钥）省略私钥。
	_, err = svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "123"})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "首次配置必须提供 App 私钥" {
		t.Fatalf("first save omitted = [%d %s]", got.Code, got.Message)
	}
}

// TestIntegration_UpdateSavesEncrypted 首次带私钥保存：落库密文、检测 ok 状态
// 落库、响应投影齐全且不含任何明文。
func TestIntegration_UpdateSavesEncrypted(t *testing.T) {
	svc, repo, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	_, keyPEMs := stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：fast-ship"})

	out, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "123456", PrivateKeyPem: &key, WebhookSecret: api.Ptr("whsec-value"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Provider != api.GithubApp || out.Source != api.GitHubAppIntegrationSourceDatabase || !out.Configured {
		t.Fatalf("head = %s %s %v", out.Provider, out.Source, out.Configured)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if out.AppId != "123456" {
		t.Fatalf("app_id = %s", out.AppId)
	}
	// PublicURL 尾部斜杠被裁掉。
	if out.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("webhook_url = %s", out.WebhookUrl)
	}
	if out.DetectionStatus != api.GitHubAppIntegrationDetectionStatusOk || out.DetectionDetail != "已连接 GitHub App：fast-ship" {
		t.Fatalf("detection = %s %s", out.DetectionStatus, out.DetectionDetail)
	}
	if out.DetectedAt == nil || out.UpdatedAt == nil {
		t.Fatalf("timestamps nil: %v %v", out.DetectedAt, out.UpdatedAt)
	}
	if len(*keyPEMs) != 1 || string((*keyPEMs)[0]) != key {
		t.Fatal("verify did not receive the saved PEM")
	}

	// 落库的是密文：行里翻不到 PEM 明文与 webhook 密钥明文。
	row, err := repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	blob := string(row.ConfigEncrypted)
	if strings.Contains(blob, "PRIVATE KEY") || strings.Contains(blob, "whsec-value") {
		t.Fatal("plaintext leaked into stored blob")
	}
	plain, err := crypto.Decrypt(row.ConfigEncrypted, []byte("12345678901234567890123456789012"))
	if err != nil || !strings.Contains(string(plain), "123456") {
		t.Fatalf("blob not decryptable: %v", err)
	}
	if row.DetectionStatus != model.IntegrationDetectionOK || row.DetectedAt == nil {
		t.Fatalf("detection not persisted: %s %v", row.DetectionStatus, row.DetectedAt)
	}
	if row.AppID != "123456" {
		t.Fatalf("app_id column = %s", row.AppID)
	}

	// 响应体投影不含明文（handler 层再断言一次 HTTP 层面）。
	outJSON, _ := json.Marshal(out)
	if strings.Contains(string(outJSON), "PRIVATE KEY") || strings.Contains(string(outJSON), "whsec-value") {
		t.Fatal("plaintext leaked into projection")
	}
}

// TestIntegration_UpdateThreeStates 省略=保留、空串=清空；保留的私钥原样传给检测。
func TestIntegration_UpdateThreeStates(t *testing.T) {
	svc, repo, _, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubs := []githubapp.Detection{
		{Status: githubapp.DetectionOK, Detail: "ok"},
		{Status: githubapp.DetectionInvalid, Detail: "GitHub 拒绝该 App 身份（401）"},
		{Status: githubapp.DetectionFailed, Detail: "网络错误，未能连接 GitHub"},
	}
	_, keyPEMs := stubVerify(svc, stubs...)

	if _, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "123", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-1"),
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// 省略私钥与密钥 = 全保留。
	out, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "123"})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("retained sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if len(*keyPEMs) != 2 || string((*keyPEMs)[1]) != key {
		t.Fatal("retained PEM not passed to detection")
	}
	// 第二轮检测 invalid 也落库。
	row, _ := repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if row.DetectionStatus != model.IntegrationDetectionInvalid {
		t.Fatalf("status = %s, want invalid", row.DetectionStatus)
	}

	// webhook_secret 空串 = 清空；私钥仍保留。
	out, err = svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "123", WebhookSecret: api.Ptr("  ")})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if out.WebhookSecretSet {
		t.Fatal("webhook_secret_set should be false after clearing")
	}
	if !out.PrivateKeySet {
		t.Fatal("private key should survive secret clearing")
	}
	// 第三轮 failed 同样落库。
	row, _ = repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if row.DetectionStatus != model.IntegrationDetectionFailed {
		t.Fatalf("status = %s, want failed", row.DetectionStatus)
	}
	// 同一用户重复保存仍只有一行（复合唯一键覆盖）。
	var n int64
	db.Model(&model.Integration{}).Count(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// TestIntegration_UserIsolation 用户级隔离：A 保存后 B 仍是 none/config，A 的
// 改动不影响 B；各自读各自行。
func TestIntegration_UserIsolation(t *testing.T) {
	svc, _, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	if _, err := svc.Update(ctx, "alice", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-a"),
	}); err != nil {
		t.Fatalf("alice save: %v", err)
	}

	// bob 尚未配置：source=none、无身份。
	bob, err := svc.Get(ctx, "bob")
	if err != nil {
		t.Fatalf("bob get: %v", err)
	}
	if bob.Source != api.GitHubAppIntegrationSourceNone || bob.Configured || bob.AppId != "" {
		t.Fatalf("bob = %s %v %q", bob.Source, bob.Configured, bob.AppId)
	}
	if _, ok, err := svc.Current(ctx, "bob"); ok || err != nil {
		t.Fatalf("bob current = %v %v", ok, err)
	}

	// alice 的行对 bob 不可见；alice 自己读得到。
	alice, err := svc.Get(ctx, "alice")
	if err != nil || alice.Source != api.GitHubAppIntegrationSourceDatabase || alice.AppId != "111" {
		t.Fatalf("alice = %v %s %s", err, alice.Source, alice.AppId)
	}

	// bob 保存自己的配置；alice 不受影响。
	if _, err := svc.Update(ctx, "bob", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "222", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-b"),
	}); err != nil {
		t.Fatalf("bob save: %v", err)
	}
	alice, _ = svc.Get(ctx, "alice")
	bob, _ = svc.Get(ctx, "bob")
	if alice.AppId != "111" || alice.WebhookSecretSet != true {
		t.Fatalf("alice changed by bob: %+v", alice)
	}
	if bob.AppId != "222" {
		t.Fatalf("bob = %s", bob.AppId)
	}
	curA, _, _ := svc.Current(ctx, "alice")
	curB, _, _ := svc.Current(ctx, "bob")
	if curA.WebhookSecret != "sec-a" || curB.WebhookSecret != "sec-b" {
		t.Fatalf("secrets crossed: %q %q", curA.WebhookSecret, curB.WebhookSecret)
	}

	// alice 清空密钥，bob 的仍在。
	if _, err := svc.Update(ctx, "alice", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", WebhookSecret: api.Ptr(""),
	}); err != nil {
		t.Fatalf("alice clear: %v", err)
	}
	curA, _, _ = svc.Current(ctx, "alice")
	curB, _, _ = svc.Current(ctx, "bob")
	if curA.WebhookSecret != "" || curB.WebhookSecret != "sec-b" {
		t.Fatalf("clear leaked: %q %q", curA.WebhookSecret, curB.WebhookSecret)
	}
}

// TestIntegration_ConcurrentSaveKeepsBothChanges S1：两个并发保存（一个只轮换
// 私钥、一个只清空 webhook 密钥，app_id 相同）结束后两组改动都在——读旧行在
// 事务内完成，后到者基于先到者的结果合并，谁也不覆盖谁。测试库固定单连接：
// 两个写事务在池级串行，不存在 SQLite 锁竞争，也就不需要任何错误重试。
func TestIntegration_ConcurrentSaveKeepsBothChanges(t *testing.T) {
	svc, repo, _, db := integrationTestServiceDSN(t,
		"file:"+uuid.NewString()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)")
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	ctx := context.Background()
	oldKey := keyPEMString(t)
	newKey := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	if _, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "100", PrivateKeyPem: &oldKey, WebhookSecret: api.Ptr("sec-0"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errsCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		// 只轮换私钥（webhook_secret 省略 = 保留当前值）。
		_, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{
			AppId: "100", PrivateKeyPem: &newKey,
		})
		errsCh <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		// 只清空 webhook 密钥（私钥省略 = 保留当前值）。
		_, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{
			AppId: "100", WebhookSecret: api.Ptr(""),
		})
		errsCh <- err
	}()
	close(start)
	wg.Wait()
	close(errsCh)
	for err := range errsCh {
		if err != nil {
			t.Fatalf("concurrent update: %v", err)
		}
	}

	row, err := repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	payload, err := crypto.Decrypt(row.ConfigEncrypted, []byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var got githubAppSecretPayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PrivateKeyPEM != newKey || got.WebhookSecret != "" {
		t.Fatalf("lost update: pem_kept=%v secret=%q, want new pem + empty secret", got.PrivateKeyPEM == newKey, got.WebhookSecret)
	}
	if row.AppID != "100" {
		t.Fatalf("app_id column = %q, want 100", row.AppID)
	}
	var n int64
	db.Model(&model.Integration{}).Count(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// TestIntegration_ConfigSourceFallback 无数据库行时回落 config；保存数据库行后
// 永远以它为准（含 Current 与 webhook secret）。
func TestIntegration_ConfigSourceFallback(t *testing.T) {
	svc, _, cfg, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = keyPath
	cfg.GitHubApp.WebhookSecret = "cfg-secret"
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	// source=config：身份来自 config，webhook_url 正常，检测未做。
	out, err := svc.Get(ctx, "u-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Source != api.GitHubAppIntegrationSourceConfig || !out.Configured || out.AppId != "777" {
		t.Fatalf("config projection = %s %v %s", out.Source, out.Configured, out.AppId)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("config sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if out.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("webhook_url = %s", out.WebhookUrl)
	}
	if out.DetectionStatus != api.GitHubAppIntegrationDetectionStatusUnchecked || out.UpdatedAt != nil {
		t.Fatalf("config detection/updated = %s %v", out.DetectionStatus, out.UpdatedAt)
	}
	cur, ok, err := svc.Current(ctx, "u-1")
	if err != nil || !ok || cur.AppID != "777" || string(cur.PrivateKeyPEM) != key || cur.WebhookSecret != "cfg-secret" {
		t.Fatalf("current(config) = %+v %v %v", cur, ok, err)
	}

	// 保存数据库行（webhook_secret 省略 = 保留 config 现值收编进行内）。
	if _, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "888", PrivateKeyPem: &key}); err != nil {
		t.Fatalf("save row: %v", err)
	}
	out, err = svc.Get(ctx, "u-1")
	if err != nil {
		t.Fatalf("get after save: %v", err)
	}
	if out.Source != api.GitHubAppIntegrationSourceDatabase || out.AppId != "888" || !out.WebhookSecretSet {
		t.Fatalf("db projection = %s %s %v", out.Source, out.AppId, out.WebhookSecretSet)
	}
	cur, ok, err = svc.Current(ctx, "u-1")
	if err != nil || !ok || cur.AppID != "888" || cur.WebhookSecret != "cfg-secret" {
		t.Fatalf("current(db) = %+v %v %v", cur, ok, err)
	}

	// 清空后：行内为空也以行为准（不回落 config 的 cfg-secret）→ webhook 由此 503。
	if _, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "888", WebhookSecret: api.Ptr("")}); err != nil {
		t.Fatalf("clear secret: %v", err)
	}
	out, err = svc.Get(ctx, "u-1")
	if err != nil || out.WebhookSecretSet {
		t.Fatalf("cleared projection = %v %v", err, out.WebhookSecretSet)
	}
	cands, err := svc.WebhookCandidates(ctx, "888")
	if err != nil || len(cands) != 1 || cands[0].Secret != "" || cands[0].AppUserID != "u-1" {
		t.Fatalf("webhook candidates = %v %v, want single empty-secret user candidate", cands, err)
	}
	cur, _, _ = svc.Current(ctx, "u-1")
	if cur.WebhookSecret != "" {
		t.Fatalf("current secret = %q, want db empty", cur.WebhookSecret)
	}

	// 未知 App ID → 40107；config 的 App ID 命中 → config 密钥（config 身份）。
	_, err = svc.WebhookCandidates(ctx, "31337")
	if got := appErr(t, err); got.Code != 40107 || got.Message != "未知的 GitHub App" {
		t.Fatalf("unknown app = [%d %s]", got.Code, got.Message)
	}
	cands, err = svc.WebhookCandidates(ctx, "777")
	if err != nil || len(cands) != 1 || cands[0].AppUserID != "" || cands[0].Secret != "cfg-secret" {
		t.Fatalf("config app candidates = %v %v", cands, err)
	}
	// 该用户有行（App 888）后无头调用：行 888 与 config 777 构成两份身份，
	// 无法确定 → 40107。
	_, err = svc.WebhookCandidates(ctx, "")
	if got := appErr(t, err); got.Code != 40107 {
		t.Fatalf("no-header with db+config identities = [%d %s], want 40107", got.Code, got.Message)
	}
}

// TestIntegration_WebhookCandidates 无头时多份身份 → 40107；唯一身份 → 行内
// 密钥（config 身份在场时一并返回）；带头时同 App ID 的多份候选逐行列出；
// config 兜底对未见行的用户同样生效。
func TestIntegration_WebhookCandidates(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	if _, err := svc.Update(ctx, "alice", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-a"),
	}); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if _, err := svc.Update(ctx, "bob", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "222", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-b"),
	}); err != nil {
		t.Fatalf("bob: %v", err)
	}

	// 两行无头 → 无法确定身份。
	_, err := svc.WebhookCandidates(ctx, "")
	if got := appErr(t, err); got.Code != 40107 || got.Message != "缺少 X-GitHub-Hook-Installation-Target-ID，无法确定集成身份" {
		t.Fatalf("ambiguous = [%d %s]", got.Code, got.Message)
	}
	// 带头各自命中（身份解析到各自用户）。
	if cands, err := svc.WebhookCandidates(ctx, "111"); err != nil || len(cands) != 1 ||
		cands[0].AppUserID != "alice" || cands[0].Secret != "sec-a" {
		t.Fatalf("alice candidates = %v %v", cands, err)
	}
	if cands, err := svc.WebhookCandidates(ctx, "222"); err != nil || len(cands) != 1 ||
		cands[0].AppUserID != "bob" || cands[0].Secret != "sec-b" {
		t.Fatalf("bob candidates = %v %v", cands, err)
	}

	// 只删到剩 alice 一行：无头恢复可用。
	db.Where("user_id = ?", "bob").Delete(&model.Integration{})
	if cands, err := svc.WebhookCandidates(ctx, ""); err != nil || len(cands) != 1 ||
		cands[0].AppUserID != "alice" || cands[0].Secret != "sec-a" {
		t.Fatalf("single identity = %v %v", cands, err)
	}

	// 一行都没有、config 只有 webhook 密钥：回落 config（可为空）。
	db.Where("1 = 1").Delete(&model.Integration{})
	cfg.GitHubApp.WebhookSecret = "cfg-only"
	if cands, err := svc.WebhookCandidates(ctx, ""); err != nil || len(cands) != 1 ||
		cands[0].AppUserID != "" || cands[0].Secret != "cfg-only" {
		t.Fatalf("config-only = %v %v", cands, err)
	}

	// config 只有密钥、无 App 身份：带头投递（真实 GitHub App 的 App ID 头）
	// 也以 config 密钥兜底（INT-93 旧部署兼容）。
	cfg.GitHubApp.AppID = ""
	if cands, err := svc.WebhookCandidates(ctx, "424242"); err != nil || len(cands) != 1 ||
		cands[0].AppUserID != "" || cands[0].Secret != "cfg-only" {
		t.Fatalf("legacy header fallback = %v %v", cands, err)
	}
	// config 连密钥也没有：未知 App 40107。
	cfg.GitHubApp.WebhookSecret = ""
	_, err = svc.WebhookCandidates(ctx, "424242")
	if got := appErr(t, err); got.Code != 40107 {
		t.Fatalf("no secret at all = [%d %s], want 40107", got.Code, got.Message)
	}
}

// TestIntegration_FirstSaveAdoptsConfigSecret 首次保存省略私钥/密钥时收编
// config 的现值（私钥原样、webhook 密钥 trim）；显式 JSON null 与省略等价。
func TestIntegration_FirstSaveAdoptsConfigSecret(t *testing.T) {
	svc, repo, cfg, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	cfg.GitHubApp.WebhookSecret = "  cfg-sec  "
	appIDs, keyPEMs := stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	// 省略（omitempty 请求体）：私钥与密钥都从 config 收编。
	out, err := svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "888"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("adopted sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if len(*keyPEMs) != 1 || string((*keyPEMs)[0]) != key {
		t.Fatal("config PEM not adopted")
	}
	if (*appIDs)[0] != "888" {
		t.Fatalf("verify app = %s", (*appIDs)[0])
	}
	row, err := repo.GetByProvider("u-1", model.IntegrationProviderGitHubApp)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	payload, err := decryptAppPayload(row.ConfigEncrypted, []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if payload.PrivateKeyPEM != key || payload.WebhookSecret != "cfg-sec" {
		t.Fatalf("adopted payload = pem(%v) secret(%q)", payload.PrivateKeyPEM == key, payload.WebhookSecret)
	}
	cur, ok, _ := svc.Current(ctx, "u-1")
	if !ok || cur.WebhookSecret != "cfg-sec" {
		t.Fatalf("current = %+v %v", cur, ok)
	}
}

// TestIntegration_DecryptFailureNoFallback 密文损坏 → 50000，且不回落 config。
func TestIntegration_DecryptFailureNoFallback(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = keyPath
	cfg.GitHubApp.WebhookSecret = "cfg-secret"

	now := time.Now().UTC()
	if err := db.Create(&model.Integration{
		ID: uuid.NewString(), UserID: "u-1", Provider: model.IntegrationProviderGitHubApp,
		AppID:           "555",
		ConfigEncrypted: []byte("corrupted-ciphertext"), DetectionStatus: model.IntegrationDetectionUnchecked,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	_, err := svc.Get(ctx, "u-1")
	if got := appErr(t, err); got.Code != 50000 || got.Message != "GitHub App 配置解密失败" {
		t.Fatalf("get = [%d %s]", got.Code, got.Message)
	}
	_, _, err = svc.Current(ctx, "u-1")
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("current = %v", err)
	}
	// webhook 按该 App ID 定位到损坏行 → 50000。
	_, err = svc.WebhookCandidates(ctx, "555")
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("webhook candidates = %v", err)
	}
	// 去掉 config 的 App 身份后无头调用：唯一身份是该损坏行 → 解密失败 50000。
	cfgAppID := cfg.GitHubApp.AppID
	cfg.GitHubApp.AppID = ""
	_, err = svc.WebhookCandidates(ctx, "")
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("webhook candidates no-header unique corrupt = %v", err)
	}
	// config 身份在场时无头是两份身份 → 40107 无法定位。
	cfg.GitHubApp.AppID = cfgAppID
	_, err = svc.WebhookCandidates(ctx, "")
	if got := appErr(t, err); got.Code != 40107 {
		t.Fatalf("webhook candidates no-header ambiguous = [%d %s], want 40107", got.Code, got.Message)
	}
	// 其他用户不受损行影响（仍是 config 兜底）。
	if cands, err := svc.WebhookCandidates(ctx, "777"); err != nil || len(cands) != 1 || cands[0].Secret != "cfg-secret" {
		t.Fatalf("other user = %v %v", cands, err)
	}
	// PUT 也拒绝：不在损坏的行上继续写。
	_, err = svc.Update(ctx, "u-1", &api.UpdateGitHubAppIntegrationRequest{AppId: "999"})
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("update = %v", err)
	}
}

// TestIntegration_TestEndpoint 未配置 40006；config 来源的检测结果只随本次
// 响应返回、不建数据库行；数据库来源按保存时密文条件落库。
func TestIntegration_TestEndpoint(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	_, err := svc.Test(ctx, "u-1")
	if got := appErr(t, err); got.Code != 40006 || got.Message != "尚未配置 GitHub App" {
		t.Fatalf("unconfigured = [%d %s]", got.Code, got.Message)
	}

	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：fast-ship"})

	out, err := svc.Test(ctx, "u-1")
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if out.DetectionStatus != api.GitHubAppIntegrationDetectionStatusOk || out.DetectedAt == nil {
		t.Fatalf("test projection = %s %v", out.DetectionStatus, out.DetectedAt)
	}
	var n int64
	db.Model(&model.Integration{}).Count(&n)
	if n != 0 {
		t.Fatalf("rows = %d, want 0 (config source must not persist)", n)
	}
	// GET 仍是 unchecked。
	out, err = svc.Get(ctx, "u-1")
	if err != nil || out.DetectionStatus != api.GitHubAppIntegrationDetectionStatusUnchecked {
		t.Fatalf("get after test = %v %s", err, out.DetectionStatus)
	}
}

// TestIntegration_CurrentUnconfigured 完全未配置时 Current 报不可用、webhook
// 密钥回落 config（无 App 身份的部署仍能验签）。
func TestIntegration_CurrentUnconfigured(t *testing.T) {
	svc, _, cfg, _ := integrationTestService(t)
	ctx := context.Background()
	cfg.GitHubApp.WebhookSecret = "only-secret"

	if _, ok, err := svc.Current(ctx, "u-1"); ok || err != nil {
		t.Fatalf("current = %v %v, want not ok", ok, err)
	}
	cands, err := svc.WebhookCandidates(ctx, "")
	if err != nil || len(cands) != 1 || cands[0].AppUserID != "" || cands[0].Secret != "only-secret" {
		t.Fatalf("webhook candidates = %v %v", cands, err)
	}
	out, err := svc.Get(ctx, "u-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// webhook_url 只随 PublicURL 走（未配置也给出该填的 URL）；PublicURL 有值。
	if out.Source != api.GitHubAppIntegrationSourceNone || out.Configured ||
		out.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("none projection = %s %v %q", out.Source, out.Configured, out.WebhookUrl)
	}
}

// TestIntegration_AppIDUniqueAcrossUsers App ID 全库唯一（同 provider 下别的
// 用户已占用 → 40929）：第二个用户保存同 App ID 被拒、库里仍只有第一用户的行；
// 自己重复保存（覆盖自己的行）不受影响；两用户不同 App ID 无头 → 401；
// 单一 App ID（含可选同名 config）无头 → 接受且候选首位是用户行。
func TestIntegration_AppIDUniqueAcrossUsers(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})

	if _, err := svc.Update(ctx, "alice", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-a"),
	}); err != nil {
		t.Fatalf("alice save: %v", err)
	}

	// bob 保存同一 App ID → 40929，库里只有 alice 一行。
	_, err := svc.Update(ctx, "bob", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-b"),
	})
	if got := appErr(t, err); got.Code != errs.ErrIntegrationAppIDTaken.Code || got.Message != "该 App ID 已被其他用户配置" {
		t.Fatalf("bob duplicate = [%d %s], want [%d 该 App ID 已被其他用户配置]",
			got.Code, got.Message, errs.ErrIntegrationAppIDTaken.Code)
	}
	var rows []model.Integration
	db.Find(&rows)
	if len(rows) != 1 || rows[0].UserID != "alice" {
		t.Fatalf("rows = %+v, want single alice row", rows)
	}

	// alice 自己重复保存（覆盖自己的行）不受唯一性影响。
	if _, err := svc.Update(ctx, "alice", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "111", WebhookSecret: api.Ptr("sec-a2"),
	}); err != nil {
		t.Fatalf("alice re-save: %v", err)
	}

	// bob 换自己的 App ID 后：两份不同身份，无头调用无法定位 → 401。
	if _, err := svc.Update(ctx, "bob", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "222", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-b"),
	}); err != nil {
		t.Fatalf("bob own app: %v", err)
	}
	_, err = svc.WebhookCandidates(ctx, "")
	if got := appErr(t, err); got.Code != 40107 {
		t.Fatalf("headless two distinct app ids = [%d %s], want 40107", got.Code, got.Message)
	}

	// 删到唯一身份、config 配了同一个 App ID：无头仍接受（数据库行在前、
	// config 在后），带头时用户行与 config 候选并存。
	db.Where("user_id = ?", "bob").Delete(&model.Integration{})
	cfg.GitHubApp.AppID = "111"
	cfg.GitHubApp.WebhookSecret = "cfg-secret"
	cands, err := svc.WebhookCandidates(ctx, "")
	if err != nil || len(cands) != 2 {
		t.Fatalf("headless single app id (+config) = %v %v, want accepted", cands, err)
	}
	if cands[0].AppUserID != "alice" || cands[0].Secret != "sec-a2" ||
		cands[1].AppUserID != "" || cands[1].Secret != "cfg-secret" {
		t.Fatalf("candidates = %+v, want [alice row, config]", cands)
	}
}

// TestIntegration_UserSavesConfigAppID 用户保存与 config 相同的 App ID 允许
// （config → 个人配置的正常迁移路径）：候选首位是该用户的行。
func TestIntegration_UserSavesConfigAppID(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok"})
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	cfg.GitHubApp.WebhookSecret = "cfg-secret"

	if _, err := svc.Update(ctx, "carol", &api.UpdateGitHubAppIntegrationRequest{
		AppId: "777", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-carol"),
	}); err != nil {
		t.Fatalf("carol save config-same app id: %v", err)
	}
	cands, err := svc.WebhookCandidates(ctx, "777")
	if err != nil || len(cands) != 2 {
		t.Fatalf("candidates = %v %v, want [carol row, config]", cands, err)
	}
	if cands[0].AppUserID != "carol" || cands[0].Secret != "sec-carol" ||
		cands[1].AppUserID != "" || cands[1].Secret != "cfg-secret" {
		t.Fatalf("candidates = %+v, want user row first then config", cands)
	}
	var n int64
	db.Model(&model.Integration{}).Count(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func writeTempKey(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}
