package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// IntegrationService（INT-129 部署级）定向测试：来源优先级、PUT 三态、并发保存
// 不丢更新、检测映射与条件落库、密文损坏不回落 config、webhook 单密钥解析。

func integrationTestService(t *testing.T) (*IntegrationService, *repository.DeploymentSettingsRepository, *config.Config, *gorm.DB) {
	t.Helper()
	return integrationTestServiceDSN(t, "file:"+uuid.NewString()+"?mode=memory&cache=shared")
}

// 并发用例的库带 busy_timeout：两个写事务相遇时等待而非立即报 locked。
func integrationTestServiceDSN(t *testing.T, dsn string) (*IntegrationService, *repository.DeploymentSettingsRepository, *config.Config, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.DeploymentSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com/"},
	}
	repo := repository.NewDeploymentSettingsRepository(db)
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

// seedDeploymentRow 直接落一张部署设置行（可带损坏密文或指定 payload）。
func seedDeploymentRow(t *testing.T, db *gorm.DB, mutate func(row *model.DeploymentSettings)) *model.DeploymentSettings {
	t.Helper()
	now := time.Now().UTC()
	row := &model.DeploymentSettings{
		ID: model.DeploymentSettingsID, PublicURL: "",
		AppID: "", AppSlug: "",
		DetectionStatus: model.DeploymentDetectionUnchecked,
		CreatedAt:       now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(row)
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return row
}

// encryptDeploymentPayload 按服务层同款结构加密一份连接参数。
func encryptDeploymentPayload(t *testing.T, payload githubAppSecretPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc, err := crypto.Encrypt(raw, []byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

// TestIntegration_UpdateValidation PUT 的参数校验与首次必须给私钥。
func TestIntegration_UpdateValidation(t *testing.T) {
	svc, _, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	// App ID 非数字 / 为空。
	_, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "12a45", PrivateKeyPem: &key})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "App ID 必须是数字" {
		t.Fatalf("non-numeric = [%d %s]", got.Code, got.Message)
	}
	_, err = svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "  ", PrivateKeyPem: &key})
	if got := appErr(t, err); got.Code != 40001 {
		t.Fatalf("blank = [%d %s]", got.Code, got.Message)
	}

	// 私钥给的不是 RSA PEM。
	bad := "definitely not pem"
	_, err = svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "123", PrivateKeyPem: &bad})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "私钥不是有效的 RSA PEM" {
		t.Fatalf("bad pem = [%d %s]", got.Code, got.Message)
	}

	// 首次保存（无数据库私钥也无 config 私钥）省略私钥。
	_, err = svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "123"})
	if got := appErr(t, err); got.Code != 40001 || got.Message != "首次配置必须提供 App 私钥" {
		t.Fatalf("first save omitted = [%d %s]", got.Code, got.Message)
	}
}

// TestIntegration_UpdateSavesEncrypted 首次带私钥保存：落库密文、检测 ok 状态
// 与 slug 落库、响应投影齐全且不含任何明文。
func TestIntegration_UpdateSavesEncrypted(t *testing.T) {
	svc, repo, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	_, keyPEMs := stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：fast-ship", Slug: "fast-ship-bot"})

	out, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
		AppId: "123456", PrivateKeyPem: &key, WebhookSecret: api.Ptr("whsec-value"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Source != api.GitHubAppDeploymentSourceDatabase || !out.Configured {
		t.Fatalf("head = %s %v", out.Source, out.Configured)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if out.AppId != "123456" {
		t.Fatalf("app_id = %s", out.AppId)
	}
	// 检测带回的 slug 落库并生成安装链接；PublicURL 尾部斜杠被裁掉。
	if out.AppSlug != "fast-ship-bot" || out.InstallUrl != "https://github.com/apps/fast-ship-bot/installations/new" {
		t.Fatalf("slug/install = %s %s", out.AppSlug, out.InstallUrl)
	}
	if out.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("webhook_url = %s", out.WebhookUrl)
	}
	if out.DetectionStatus != api.GitHubAppDeploymentDetectionStatusOk || out.DetectionDetail != "已连接 GitHub App：fast-ship" {
		t.Fatalf("detection = %s %s", out.DetectionStatus, out.DetectionDetail)
	}
	if out.DetectedAt == nil || out.UpdatedAt == nil {
		t.Fatalf("timestamps nil: %v %v", out.DetectedAt, out.UpdatedAt)
	}
	if len(*keyPEMs) != 1 || string((*keyPEMs)[0]) != key {
		t.Fatal("verify did not receive the saved PEM")
	}

	// 落库的是密文：行里翻不到 PEM 明文与 webhook 密钥明文。
	row, err := repo.Get()
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
	if row.DetectionStatus != model.DeploymentDetectionOK || row.DetectedAt == nil {
		t.Fatalf("detection not persisted: %s %v", row.DetectionStatus, row.DetectedAt)
	}
	if row.AppID != "123456" || row.AppSlug != "fast-ship-bot" {
		t.Fatalf("app columns = %s/%s", row.AppID, row.AppSlug)
	}

	// 响应体投影不含明文（handler 层再断言一次 HTTP 层面）。
	outJSON, _ := json.Marshal(out)
	if strings.Contains(string(outJSON), "PRIVATE KEY") || strings.Contains(string(outJSON), "whsec-value") {
		t.Fatal("plaintext leaked into projection")
	}
}

// TestIntegration_UpdateThreeStates 省略=保留、空串=清空；保留的私钥原样传给
// 检测；重复保存仍只有一行。
func TestIntegration_UpdateThreeStates(t *testing.T) {
	svc, repo, _, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubs := []githubapp.Detection{
		{Status: githubapp.DetectionOK, Detail: "ok", Slug: "bot"},
		{Status: githubapp.DetectionInvalid, Detail: "GitHub 拒绝该 App 身份（401）"},
		{Status: githubapp.DetectionFailed, Detail: "网络错误，未能连接 GitHub"},
	}
	_, keyPEMs := stubVerify(svc, stubs...)

	if _, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
		AppId: "123", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-1"),
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// 省略私钥与密钥 = 全保留。
	out, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "123"})
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
	row, _ := repo.Get()
	if row.DetectionStatus != model.DeploymentDetectionInvalid {
		t.Fatalf("status = %s, want invalid", row.DetectionStatus)
	}

	// webhook_secret 空串 = 清空；私钥仍保留。
	out, err = svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "123", WebhookSecret: api.Ptr("  ")})
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
	row, _ = repo.Get()
	if row.DetectionStatus != model.DeploymentDetectionFailed {
		t.Fatalf("status = %s, want failed", row.DetectionStatus)
	}
	// 重复保存仍只有一行（单行表）。
	var n int64
	db.Model(&model.DeploymentSettings{}).Count(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// TestIntegration_SourcePriority 来源优先级：无任何配置 → none；config 配了
// 身份 → config；保存数据库行后永远以它为准；行只存过 public_url（无 App 身份）
// 时不遮蔽 config。
func TestIntegration_SourcePriority(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok", Slug: "cfg-bot"})

	// source=none。
	out, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("get(none): %v", err)
	}
	if out.Source != api.GitHubAppDeploymentSourceNone || out.Configured || out.AppId != "" || out.InstallUrl != "" {
		t.Fatalf("none projection = %s %v %q %q", out.Source, out.Configured, out.AppId, out.InstallUrl)
	}

	// config 兜底：身份与私钥文件都配了 → source=config。
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = keyPath
	cfg.GitHubApp.WebhookSecret = "cfg-secret"
	out, err = svc.Get(ctx)
	if err != nil {
		t.Fatalf("get(config): %v", err)
	}
	if out.Source != api.GitHubAppDeploymentSourceConfig || !out.Configured || out.AppId != "777" {
		t.Fatalf("config projection = %s %v %s", out.Source, out.Configured, out.AppId)
	}
	if !out.PrivateKeySet || !out.WebhookSecretSet {
		t.Fatalf("config sets = %v %v", out.PrivateKeySet, out.WebhookSecretSet)
	}
	if out.UpdatedAt != nil || out.DetectionStatus != api.GitHubAppDeploymentDetectionStatusUnchecked {
		t.Fatalf("config detection/updated = %s %v", out.DetectionStatus, out.UpdatedAt)
	}

	// 行只存过 public_url：对 App 身份一无所知，仍回落 config。
	seedDeploymentRow(t, db, func(row *model.DeploymentSettings) { row.PublicURL = "https://web.example.com" })
	out, err = svc.Get(ctx)
	if err != nil || out.Source != api.GitHubAppDeploymentSourceConfig || out.AppId != "777" {
		t.Fatalf("public-url-only row = %v %s %s", err, out.Source, out.AppId)
	}

	// 保存数据库行（webhook_secret 省略 = 收编 config 现值）后永远以行为准。
	if _, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "888", PrivateKeyPem: &key}); err != nil {
		t.Fatalf("save row: %v", err)
	}
	out, err = svc.Get(ctx)
	if err != nil {
		t.Fatalf("get after save: %v", err)
	}
	if out.Source != api.GitHubAppDeploymentSourceDatabase || out.AppId != "888" || !out.WebhookSecretSet {
		t.Fatalf("db projection = %s %s %v", out.Source, out.AppId, out.WebhookSecretSet)
	}
	cur, ok, err := svc.Current(ctx)
	if err != nil || !ok || cur.AppID != "888" || cur.WebhookSecret != "cfg-secret" {
		t.Fatalf("current(db) = %+v %v %v", cur, ok, err)
	}

	// config 私钥文件读不到时身份来源仍是 config（页面要能打开），只是
	// private_key_set=false 暴露「写了路径但读不到」。
	cfg.GitHubApp.PrivateKeyPath = filepath.Join(t.TempDir(), "missing.pem")
	cfg.GitHubApp.AppID = "999"
	db.Where("1 = 1").Delete(&model.DeploymentSettings{})
	out, err = svc.Get(ctx)
	if err != nil || out.Source != api.GitHubAppDeploymentSourceConfig || !out.Configured || out.PrivateKeySet || out.AppId != "999" {
		t.Fatalf("unreadable key file = %v %s %v %v %s", err, out.Source, out.Configured, out.PrivateKeySet, out.AppId)
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

	seedDeploymentRow(t, db, func(row *model.DeploymentSettings) {
		row.AppID = "555"
		row.ConfigEncrypted = []byte("corrupted-ciphertext")
	})

	_, err := svc.Get(ctx)
	if got := appErr(t, err); got.Code != 50000 || got.Message != "GitHub App 配置解密失败" {
		t.Fatalf("get = [%d %s]", got.Code, got.Message)
	}
	_, _, err = svc.Current(ctx)
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("current = %v", err)
	}
	// webhook 密钥解析同一路径 → 50000。
	if _, err = svc.WebhookSecret(ctx); !errors.As(err, new(*errs.AppError)) || appErr(t, err).Code != 50000 {
		t.Fatalf("webhook secret = %v, want 50000", err)
	}
	// PUT 也拒绝：不在损坏的行上继续写。
	_, err = svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "999"})
	if got := appErr(t, err); got.Code != 50000 {
		t.Fatalf("update = %v", err)
	}
}

// TestIntegration_WebhookSecretPrecedence webhook 单密钥：数据库行配了身份后以
// 行内密钥为准（为空也不回落 config）；行没有身份时回落 config；完全未配置为
// 空串。
func TestIntegration_WebhookSecretPrecedence(t *testing.T) {
	svc, _, cfg, db := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	// 未配置：config 只有 webhook 密钥的旧部署仍能验签。
	cfg.GitHubApp.WebhookSecret = "only-secret"
	if secret, err := svc.WebhookSecret(ctx); err != nil || secret != "only-secret" {
		t.Fatalf("config-only secret = %q %v", secret, err)
	}

	// 数据库行配了身份：行内密钥为空也以行为准（不回落 config）。
	seedDeploymentRow(t, db, func(row *model.DeploymentSettings) {
		row.AppID = "555"
		row.ConfigEncrypted = encryptDeploymentPayload(t, githubAppSecretPayload{AppID: "555", PrivateKeyPEM: key})
	})
	if secret, err := svc.WebhookSecret(ctx); err != nil || secret != "" {
		t.Fatalf("row empty secret = %q %v, want empty (no config fallback)", secret, err)
	}

	// 行内密钥非空：原样返回。
	db.Where("1 = 1").Delete(&model.DeploymentSettings{})
	seedDeploymentRow(t, db, func(row *model.DeploymentSettings) {
		row.AppID = "556"
		row.ConfigEncrypted = encryptDeploymentPayload(t, githubAppSecretPayload{AppID: "556", PrivateKeyPEM: key, WebhookSecret: "  row-secret  "})
	})
	if secret, err := svc.WebhookSecret(ctx); err != nil || secret != "row-secret" {
		t.Fatalf("row secret = %q %v, want trimmed row-secret", secret, err)
	}

	// 行没写 App 身份（只存 public_url）：回落 config。
	db.Where("1 = 1").Delete(&model.DeploymentSettings{})
	seedDeploymentRow(t, db, func(row *model.DeploymentSettings) { row.PublicURL = "https://web.example.com" })
	if secret, err := svc.WebhookSecret(ctx); err != nil || secret != "only-secret" {
		t.Fatalf("public-url-only row secret = %q %v, want config fallback", secret, err)
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

	if _, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
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
		_, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
			AppId: "100", PrivateKeyPem: &newKey,
		})
		errsCh <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		// 只清空 webhook 密钥（私钥省略 = 保留当前值）。
		_, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
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

	row, err := repo.Get()
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
	out, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{AppId: "888"})
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
	row, err := repo.Get()
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
	cur, ok, _ := svc.Current(ctx)
	if !ok || cur.WebhookSecret != "cfg-sec" {
		t.Fatalf("current = %+v %v", cur, ok)
	}
}

// TestIntegration_TestEndpoint 未配置 40006；config 来源的检测结果只随本次
// 响应返回（检测三列不落库），带回的 slug 记进行里单行供安装链接用；
// 数据库来源按保存时密文条件落库。
func TestIntegration_TestEndpoint(t *testing.T) {
	svc, repo, cfg, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	_, err := svc.Test(ctx)
	if got := appErr(t, err); got.Code != 40006 || got.Message != "尚未配置 GitHub App" {
		t.Fatalf("unconfigured = [%d %s]", got.Code, got.Message)
	}

	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：fast-ship", Slug: "cfg-bot"})

	out, err := svc.Test(ctx)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if out.DetectionStatus != api.GitHubAppDeploymentDetectionStatusOk || out.DetectedAt == nil {
		t.Fatalf("test projection = %s %v", out.DetectionStatus, out.DetectedAt)
	}
	// config 来源的检测带回 slug 落进行里的单行（app_id/密文仍空），安装链接可用。
	if out.AppSlug != "cfg-bot" || !strings.Contains(out.InstallUrl, "cfg-bot") {
		t.Fatalf("config test slug/install = %s %s", out.AppSlug, out.InstallUrl)
	}
	row, err := repo.Get()
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.AppSlug != "cfg-bot" || row.AppID != "" || len(row.ConfigEncrypted) != 0 {
		t.Fatalf("config row = slug(%q) id(%q) enc(%d)", row.AppSlug, row.AppID, len(row.ConfigEncrypted))
	}
	// GET 仍是 unchecked（检测三列不归 config 来源所有），但行里记下的 slug 要
	// 出现在投影里——成员不触发检测也要能看到安装链接。
	out, err = svc.Get(ctx)
	if err != nil || out.DetectionStatus != api.GitHubAppDeploymentDetectionStatusUnchecked {
		t.Fatalf("get after test = %v %s", err, out.DetectionStatus)
	}
	if out.AppSlug != "cfg-bot" || !strings.Contains(out.InstallUrl, "cfg-bot") {
		t.Fatalf("get after test slug/install = %q %q", out.AppSlug, out.InstallUrl)
	}
	if out.Source != api.GitHubAppDeploymentSourceConfig {
		t.Fatalf("get after test source = %s", out.Source)
	}
}

// TestIntegration_ConfigDetectionPersistsSlug config 来源 + 检测成功带回 slug：
// 行里 app_slug 落库、app_id 仍为空、来源仍是 config；检测失败不写任何行。
func TestIntegration_ConfigDetectionPersistsSlug(t *testing.T) {
	ctx := context.Background()
	key := keyPEMString(t)

	// 检测失败（invalid）：什么都不落。
	svc, _, cfg, db := integrationTestService(t)
	cfg.GitHubApp.AppID = "777"
	cfg.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	stubVerify(svc, githubapp.Detection{Status: githubapp.DetectionInvalid, Detail: "GitHub 拒绝该 App 身份（401）"})
	if _, err := svc.Test(ctx); err != nil {
		t.Fatalf("test: %v", err)
	}
	var n int64
	db.Model(&model.DeploymentSettings{}).Count(&n)
	if n != 0 {
		t.Fatalf("rows = %d, want 0 (failed detection must not persist)", n)
	}

	// 检测成功带回 slug：slug 落库，身份三列不动，来源仍是 config。
	svc2, repo2, cfg2, _ := integrationTestService(t)
	cfg2.GitHubApp.AppID = "777"
	cfg2.GitHubApp.PrivateKeyPath = writeTempKey(t, key)
	stubVerify(svc2, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：cfg-bot", Slug: "cfg-bot"})
	if _, err := svc2.Test(ctx); err != nil {
		t.Fatalf("test 2: %v", err)
	}
	row, err := repo2.Get()
	if err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.AppSlug != "cfg-bot" || row.AppID != "" || len(row.ConfigEncrypted) != 0 {
		t.Fatalf("row = slug(%q) id(%q) enc(%d)", row.AppSlug, row.AppID, len(row.ConfigEncrypted))
	}
	out, err := svc2.Get(ctx)
	if err != nil || !out.Configured || out.Source != api.GitHubAppDeploymentSourceConfig {
		t.Fatalf("projection = %v %s %v", err, out.Source, out.Configured)
	}
}

// TestIntegration_DetectionVersionGuard 检测期间配置被再次保存：针对旧密文的
// 迟到结果按密文版本比对丢弃，不覆盖新配置的检测结果。
func TestIntegration_DetectionVersionGuard(t *testing.T) {
	svc, repo, _, _ := integrationTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)

	// 每次检测返回可区分的结果：第 1/2/3 次分别是 d1/d2/d3。
	idx := 0
	svc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		idx++
		return githubapp.Detection{Status: githubapp.DetectionOK, Detail: fmt.Sprintf("d%d", idx), Slug: fmt.Sprintf("bot-%d", idx)}
	})

	if _, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
		AppId: "100", PrivateKeyPem: &key, WebhookSecret: api.Ptr("sec-0"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	row, err := repo.Get()
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	staleVersion := append([]byte(nil), row.ConfigEncrypted...)

	// 配置被再次保存：密文变化；本次保存自己的检测（第 2 次）正常写回。
	if _, err := svc.Update(ctx, &api.UpdateGitHubAppDeploymentRequest{
		AppId: "100", WebhookSecret: api.Ptr("sec-1"),
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// 针对旧密文的慢检测（第 3 次）此刻才回来：按版本比对被丢弃。
	svc.runDetection(ctx, staleVersion, githubAppSecretPayload{AppID: "100", PrivateKeyPEM: key, WebhookSecret: "sec-0"})

	row, err = repo.Get()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.DetectionDetail != "d2" || row.AppSlug != "bot-2" {
		t.Fatalf("detection = %q/%q, want d2/bot-2 (stale d3 discarded)", row.DetectionDetail, row.AppSlug)
	}
}

// TestIntegration_PublicURLMissingWebhookURL public_url 未配置时 webhook_url 为
// 空串；配置后拼出回调地址。
func TestIntegration_PublicURLMissingWebhookURL(t *testing.T) {
	svc, _, cfg, _ := integrationTestService(t)
	ctx := context.Background()
	cfg.Server.PublicURL = ""

	out, err := svc.Get(ctx)
	if err != nil || out.WebhookUrl != "" {
		t.Fatalf("no public_url = %v %q", err, out.WebhookUrl)
	}
	cfg.Server.PublicURL = "https://late.example.com/"
	out, err = svc.Get(ctx)
	if err != nil || out.WebhookUrl != "https://late.example.com/api/github/webhooks" {
		t.Fatalf("with public_url = %v %q", err, out.WebhookUrl)
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
