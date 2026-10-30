package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IntegrationService 部署级集成配置（INT-129）：每套部署一个 GitHub App，整组
// 参数加密落库、保存即生效。来源优先级：deployment_settings 的 App 身份 >
// config.yaml 兜底 > 未配置。数据库行配了 App 身份时永远以它为准——包括
// webhook_secret 为空（webhook 由此拒收）；密文解密失败明确 50000，不回落
// config。加解密只发生在本层；私钥与密钥本体不进日志、不进响应。
type IntegrationService struct {
	repo   *repository.DeploymentSettingsRepository
	cfg    *config.Config
	logger *zap.Logger
	// verify 连接检测入口，main 默认接 githubapp.Verify；测试注入 fake。
	verify func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection
	// publicURLSrc 部署 public_url 读取入口（INT-129）：main 注入
	// DeploymentSettingsService；缺省只读 config，保持既有构造与测试行为。
	publicURLSrc PublicURLSource
}

// GitHubAppConfig 集成配置的当前生效明文身份。只在服务层内存里短暂存在。
type GitHubAppConfig struct {
	AppID         string
	PrivateKeyPEM []byte
	WebhookSecret string
	// AppSlug 部署机器人句柄（不含 [bot] 后缀）：评论唤醒把它也当唤醒串。
	AppSlug string
}

var (
	errGitHubAppIDInvalid     = errs.New(40001, "App ID 必须是数字")
	errGitHubAppKeyInvalid    = errs.New(40001, "私钥不是有效的 RSA PEM")
	errGitHubAppKeyRequired   = errs.New(40001, "首次配置必须提供 App 私钥")
	errGitHubAppNotConfigured = errs.New(40006, "尚未配置 GitHub App")
	errGitHubAppDecrypt       = errs.New(errs.ErrInternal.Code, "GitHub App 配置解密失败")
	errGitHubConfigKeyRead    = errs.New(errs.ErrInternal.Code, "读取部署配置的 App 私钥失败")
)

// 当前生效来源，与 api.GitHubAppDeploymentSource 一致。
const (
	githubAppSourceDatabase = "database"
	githubAppSourceConfig   = "config"
	githubAppSourceNone     = "none"
)

func NewIntegrationService(repo *repository.DeploymentSettingsRepository, cfg *config.Config, logger *zap.Logger) *IntegrationService {
	return &IntegrationService{
		repo: repo, cfg: cfg, logger: logger, verify: githubapp.Verify,
		publicURLSrc: configPublicURLSource{cfg: cfg},
	}
}

// SetVerify 注入连接检测函数（测试用）。
func (s *IntegrationService) SetVerify(fn func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection) {
	s.verify = fn
}

// SetPublicURLSource 注入部署 public_url 读取入口（INT-129，main 调用一次）。
func (s *IntegrationService) SetPublicURLSource(src PublicURLSource) {
	s.publicURLSrc = src
}

// githubAppSecretPayload 加密落库的连接参数明文结构（与 PUT 请求字段对应）。
type githubAppSecretPayload struct {
	AppID         string `json:"app_id"`
	PrivateKeyPEM string `json:"private_key_pem"`
	WebhookSecret string `json:"webhook_secret"`
}

// resolveConfig 读侧的唯一入口：按「部署设置行的 App 身份 > config.yaml >
// 未配置」解析当前生效身份。行存在且写了 App 身份（app_id 非空）时永远以它为
// 准——webhook 密钥为空也不回落 config；解密失败 50000。行只存过 public_url
// （app_id 为空）时对 App 身份一无所知，回落 config。usable 表示 app_id 与私钥
// 齐备、可用于签发 App JWT。config 来源的私钥每次现读文件（换 key 文件同样免
// 重启）；读不到时身份不可用但不报错——页面必须仍能打开，让部署者改存数据库
// 配置救场。
func (s *IntegrationService) resolveConfig() (source string, row *model.DeploymentSettings, payload githubAppSecretPayload, usable bool, err error) {
	dbRow, dbErr := s.repo.Get()
	if dbErr != nil && !errors.Is(dbErr, gorm.ErrRecordNotFound) {
		return githubAppSourceNone, nil, githubAppSecretPayload{}, false, errs.ErrInternal
	}
	if dbErr == nil && dbRow.AppID != "" {
		payload, decErr := decryptAppPayload(dbRow.ConfigEncrypted, []byte(s.cfg.Encryption.Key))
		if decErr != nil {
			return githubAppSourceDatabase, dbRow, githubAppSecretPayload{}, false, errGitHubAppDecrypt
		}
		usable = payload.PrivateKeyPEM != ""
		return githubAppSourceDatabase, dbRow, payload, usable, nil
	}

	if s.cfg.GitHubApp.AppID != "" && s.cfg.GitHubApp.PrivateKeyPath != "" {
		pem, readErr := os.ReadFile(s.cfg.GitHubApp.PrivateKeyPath)
		if readErr != nil {
			return githubAppSourceConfig, dbRow, githubAppSecretPayload{
				AppID:         s.cfg.GitHubApp.AppID,
				WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret),
			}, false, nil
		}
		// 行可能只存了 public_url / app_slug（网页保存过设置或检测记过 slug）：
		// 一并带出供投影拼安装链接，但检测三列与 updated_at 仍以 config 为准。
		return githubAppSourceConfig, dbRow, githubAppSecretPayload{
			AppID:         s.cfg.GitHubApp.AppID,
			PrivateKeyPEM: string(pem),
			WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret),
		}, true, nil
	}
	// 部署未配置 App 身份；config 的 webhook 密钥仍保留给 webhook 验签（它不
	// 依赖 App 身份），保持 INT-93 以来仅配 webhook 的部署可用。
	return githubAppSourceNone, nil, githubAppSecretPayload{WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret)}, false, nil
}

// Current 返回当前部署生效的 GitHub App 明文身份（githubapp.Clients 的唯一
// 入口）；ok=false 表示未配置。解密失败返回 50000，不回落 config。
func (s *IntegrationService) Current(ctx context.Context) (GitHubAppConfig, bool, error) {
	_, row, payload, usable, err := s.resolveConfig()
	if err != nil {
		return GitHubAppConfig{}, false, err
	}
	slug := ""
	if row != nil {
		slug = row.AppSlug
	}
	return GitHubAppConfig{
		AppID:         payload.AppID,
		PrivateKeyPEM: []byte(payload.PrivateKeyPEM),
		WebhookSecret: payload.WebhookSecret,
		// AppSlug 部署机器人句柄（<slug>[bot]）：评论唤醒把它也当唤醒串
		// （INT-179——字面量 @fast-ship-bot 与真实句柄不同）。
		AppSlug: slug,
	}, usable, nil
}

// WebhookSecret 返回当前生效的 webhook 验签密钥（trim 后；未配置为空串）：数据库
// 行配了 App 身份时以行内密钥为准（为空即空，不回落 config），否则取 config。
// 解密失败返回 50000。
func (s *IntegrationService) WebhookSecret(ctx context.Context) (string, error) {
	_, _, payload, _, err := s.resolveConfig()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.WebhookSecret), nil
}

// Get GET /api/deployment/github-app 的投影：只给「是否已配置」与安装/回调
// 链接，不触发任何 GitHub 请求。
func (s *IntegrationService) Get(ctx context.Context) (*api.GitHubAppDeployment, error) {
	source, row, payload, _, err := s.resolveConfig()
	if err != nil {
		return nil, err
	}
	return s.project(ctx, source, row, payload, nil), nil
}

// Update PUT /api/deployment/github-app：整组保存（app_id 必填、私钥省略=
// 保留、webhook_secret 省略=保留/空串=清空），保存后同步检测一次；检测失败
// 或凭证无效仍然 200，状态落库。
//
// 「读旧行 → 合并省略字段 → 加密 → 写行」在一个短事务内完成（闭包内只用
// tx 句柄 + 纯 CPU 的 JSON/AES，config 私钥文件进事务前读好）——并发保存按
// 到达顺序串行化，后到者在自己的事务里读到先到者的结果，谁也不会覆盖丢谁；
// 检测在事务提交后执行，并用刚写入的密文做版本比对，慢检测覆盖不了新配置。
func (s *IntegrationService) Update(ctx context.Context, req *api.UpdateGitHubAppDeploymentRequest) (*api.GitHubAppDeployment, error) {
	appID := strings.TrimSpace(req.AppId)
	if !allDigits(appID) {
		return nil, errGitHubAppIDInvalid
	}

	// 请求自带私钥：进事务前解析校验（纯 CPU）。
	reqPEM := ""
	if req.PrivateKeyPem != nil {
		if _, parseErr := githubapp.ParsePrivateKeyPEM([]byte(*req.PrivateKeyPem)); parseErr != nil {
			return nil, errGitHubAppKeyInvalid
		}
		reqPEM = *req.PrivateKeyPem
	}
	// config 私钥兜底也进事务前读好：闭包内只做内存合并与加解密。
	var cfgPEM []byte
	var cfgPEMErr error
	if reqPEM == "" && s.cfg.GitHubApp.PrivateKeyPath != "" {
		cfgPEM, cfgPEMErr = os.ReadFile(s.cfg.GitHubApp.PrivateKeyPath)
	}
	encKey := []byte(s.cfg.Encryption.Key)

	var (
		written   []byte                 // 事务提交后供检测做版本比对
		detectGPL githubAppSecretPayload // 提交后的生效明文，供检测取身份
	)
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		row, err := s.repo.GetTx(tx)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrInternal
		}

		payload := githubAppSecretPayload{AppID: appID}
		haveKey := false
		if reqPEM != "" {
			// PEM 不做 trim：解析器自己容忍周边空白，原样保存保持与用户粘贴一致。
			payload.PrivateKeyPEM = reqPEM
			haveKey = true
		}
		if row != nil && row.AppID != "" {
			prev, decErr := decryptAppPayload(row.ConfigEncrypted, encKey)
			if decErr != nil {
				return decErr
			}
			if !haveKey {
				payload.PrivateKeyPEM = prev.PrivateKeyPEM
				haveKey = prev.PrivateKeyPEM != ""
			}
			payload.WebhookSecret = prev.WebhookSecret
		}
		if !haveKey {
			// 省略私钥 = 保留已有：部署没有数据库私钥时把 config 的私钥收编
			// 进行内，保证数据库行自洽（行配了身份后 config 不再被读取）。
			if s.cfg.GitHubApp.PrivateKeyPath == "" {
				return errGitHubAppKeyRequired
			}
			if cfgPEMErr != nil {
				return errGitHubConfigKeyRead
			}
			payload.PrivateKeyPEM = string(cfgPEM)
		}

		switch {
		case req.WebhookSecret != nil:
			payload.WebhookSecret = strings.TrimSpace(*req.WebhookSecret)
		case row == nil || row.AppID == "":
			// 首次保存省略密钥 = 收编 config 的现值；已有身份行则保持旧值。
			payload.WebhookSecret = strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret)
		}

		raw, err := json.Marshal(payload)
		if err != nil {
			return errs.ErrInternal
		}
		encrypted, err := crypto.Encrypt(raw, encKey)
		if err != nil {
			return errs.ErrInternal
		}
		// 保存即重置检测三列：真实结果由提交后的检测经 UpdateDetectionIfCurrent
		// 写回；app_slug 同步清空，等检测结果带回。
		if err := s.repo.UpsertTx(tx, map[string]any{
			"app_id":           appID,
			"app_slug":         "",
			"config_encrypted": encrypted,
			"detection_status": model.DeploymentDetectionUnchecked,
			"detection_detail": "",
			"detected_at":      nil,
		}); err != nil {
			return err
		}
		written = encrypted
		detectGPL = payload
		return nil
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		s.logger.Error("保存部署集成配置失败", zap.Error(err))
		return nil, errs.ErrInternal
	}

	// 保存后同步检测（与 POST test 同一路径）；失败/无效也落库并返回 200。
	s.runDetection(ctx, written, detectGPL)
	return s.Get(ctx)
}

// Test POST /api/deployment/github-app/test：对当前生效配置做一次只读检测。
// source=config 时没有数据库身份可落库，结果只随本次响应返回、完全不写库；
// 未配置报 40006。
func (s *IntegrationService) Test(ctx context.Context) (*api.GitHubAppDeployment, error) {
	source, row, payload, usable, err := s.resolveConfig()
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, errGitHubAppNotConfigured
	}
	var version []byte
	if source == githubAppSourceDatabase {
		version = row.ConfigEncrypted
	}
	detection := s.runDetection(ctx, version, payload)
	if version == nil {
		return s.project(ctx, source, row, payload, &detection), nil
	}
	return s.Get(ctx)
}

// runDetection 执行一次检测并按密文版本条件落库（version 非 nil 时；nil 表示
// config/none 来源，没有可做版本比对的密文）。检测结果带回的 App slug 一并落库
// （手填或 config 配置的 App 也能生成安装链接）。结果过期即丢弃，不影响调用方
// 成败。
func (s *IntegrationService) runDetection(ctx context.Context, version []byte, payload githubAppSecretPayload) githubapp.Detection {
	detection := s.verify(ctx, payload.AppID, []byte(payload.PrivateKeyPEM), s.cfg.GitHubApp.APIBaseURL)
	if version == nil {
		// config/none 来源没有数据库 App 身份可落，但检测成功带回的 slug 仍要
		// 记进行里单行的 app_slug（不写 app_id/config_encrypted，来源仍是
		// config）：安装链接由 slug 拼出，config 来源的部署也要能给出。
		if detection.Status == githubapp.DetectionOK && detection.Slug != "" {
			if err := s.repo.Upsert(map[string]any{"app_slug": detection.Slug}); err != nil {
				s.logger.Warn("记录 config 来源的 App slug 失败", zap.Error(err))
			}
		}
		return detection
	}
	ok, err := s.repo.UpdateDetectionIfCurrent(version, detection.Status, detection.Detail, detection.Slug, time.Now().UTC())
	if err != nil {
		s.logger.Warn("写入部署集成检测结果失败", zap.Error(err))
		return detection
	}
	if !ok {
		// 检测期间配置被再次保存：本次结果过期，丢弃（新配置有自己的检测）。
		s.logger.Info("部署集成检测结果已过期丢弃", zap.String("app_id", payload.AppID))
	}
	return detection
}

// decryptAppPayload 解密整组连接参数（纯内存，无池依赖，事务闭包内可用）。
func decryptAppPayload(configEncrypted []byte, encKey []byte) (githubAppSecretPayload, error) {
	raw, err := crypto.Decrypt(configEncrypted, encKey)
	var payload githubAppSecretPayload
	if err == nil {
		err = json.Unmarshal(raw, &payload)
	}
	if err != nil {
		return githubAppSecretPayload{}, errGitHubAppDecrypt
	}
	return payload, nil
}

// project 组装 GET/PUT/test 统一的响应投影。dbRow 在 source=database 时携带身份与
// 检测信息；source=config 时可能仍非空（行里只存了 public_url / app_slug），此时只取
// app_slug 拼安装链接，检测三列与 updated_at 不归该来源所有。fresh 非空时覆盖检测
// 三字段（没有数据库身份可落库时结果只随本次响应返回）。webhook URL 的基址经注入的
// publicURLSrc 取（INT-129：网页配置优先于 config）。
func (s *IntegrationService) project(ctx context.Context, source string, dbRow *model.DeploymentSettings, payload githubAppSecretPayload, fresh *githubapp.Detection) *api.GitHubAppDeployment {
	status := model.DeploymentDetectionUnchecked
	detail := ""
	slug := ""
	var detectedAt *time.Time
	var updatedAt *string
	if dbRow != nil {
		slug = dbRow.AppSlug
		if source == githubAppSourceDatabase {
			status = dbRow.DetectionStatus
			if status == "" {
				status = model.DeploymentDetectionUnchecked
			}
			detail = dbRow.DetectionDetail
			detectedAt = dbRow.DetectedAt
			updatedAt = api.JSONTimePtr(&dbRow.UpdatedAt)
		}
	}
	if fresh != nil {
		now := time.Now().UTC()
		status = fresh.Status
		detail = fresh.Detail
		detectedAt = &now
		if fresh.Slug != "" {
			slug = fresh.Slug
		}
	}

	// 私钥是否真的在读到的生效明文里：config 来源读不到文件时就是 false，
	// 不能以「配置里写了路径」冒充可用。
	privateKeySet := payload.PrivateKeyPEM != ""

	webhookURL := ""
	if base := s.publicURLSrc.PublicURL(ctx); base != "" {
		webhookURL = base + "/api/github/webhooks"
	}
	installURL := ""
	if slug != "" {
		installURL = "https://github.com/apps/" + slug + "/installations/new"
	}

	return &api.GitHubAppDeployment{
		Configured:       source != githubAppSourceNone,
		Source:           githubAppSourceAPI(source),
		AppId:            payload.AppID,
		AppSlug:          slug,
		PrivateKeySet:    privateKeySet,
		WebhookSecretSet: payload.WebhookSecret != "",
		WebhookUrl:       webhookURL,
		InstallUrl:       installURL,
		DetectionStatus:  api.GitHubAppDeploymentDetectionStatus(status),
		DetectionDetail:  detail,
		DetectedAt:       api.JSONTimePtr(detectedAt),
		UpdatedAt:        updatedAt,
	}
}

func githubAppSourceAPI(source string) api.GitHubAppDeploymentSource {
	switch source {
	case githubAppSourceDatabase:
		return api.GitHubAppDeploymentSourceDatabase
	case githubAppSourceConfig:
		return api.GitHubAppDeploymentSourceConfig
	default:
		return api.GitHubAppDeploymentSourceNone
	}
}

// allDigits 非空且全是 ASCII 数字。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
