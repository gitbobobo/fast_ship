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
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IntegrationService 用户级集成配置（INT-119）：每个登录用户配置自己的外部服务
// 连接（当前 GitHub App），整组参数加密落库、保存即生效。来源优先级按用户解析：
// 该用户的数据库行 > config.yaml 兜底（该用户没有行时，保留单用户部署行为）>
// 未配置。用户自己的行存在时永远以它为准——包括 webhook_secret 为空（webhook
// 由此拒收）；密文解密失败明确 50000，不回落 config。加解密只发生在本层；私钥
// 与密钥本体不进日志、不进响应。
type IntegrationService struct {
	repo   *repository.IntegrationRepository
	cfg    *config.Config
	logger *zap.Logger
	// verify 连接检测入口，main 默认接 githubapp.Verify；测试注入 fake。
	verify func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection
}

// GitHubAppConfig 集成配置的当前生效明文身份。只在服务层内存里短暂存在。
type GitHubAppConfig struct {
	AppID         string
	PrivateKeyPEM []byte
	WebhookSecret string
}

var (
	errGitHubAppIDInvalid     = errs.New(40001, "App ID 必须是数字")
	errGitHubAppKeyInvalid    = errs.New(40001, "私钥不是有效的 RSA PEM")
	errGitHubAppKeyRequired   = errs.New(40001, "首次配置必须提供 App 私钥")
	errGitHubAppNotConfigured = errs.New(40006, "尚未配置 GitHub App")
	errGitHubAppDecrypt       = errs.New(errs.ErrInternal.Code, "GitHub App 配置解密失败")
	errGitHubConfigKeyRead    = errs.New(errs.ErrInternal.Code, "读取部署配置的 App 私钥失败")
)

// 当前生效来源，与 api.GitHubAppIntegrationSource 一致。
const (
	githubAppSourceDatabase = "database"
	githubAppSourceConfig   = "config"
	githubAppSourceNone     = "none"
)

func NewIntegrationService(repo *repository.IntegrationRepository, cfg *config.Config, logger *zap.Logger) *IntegrationService {
	return &IntegrationService{repo: repo, cfg: cfg, logger: logger, verify: githubapp.Verify}
}

// SetVerify 注入连接检测函数（测试用）。
func (s *IntegrationService) SetVerify(fn func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection) {
	s.verify = fn
}

// githubAppSecretPayload 加密落库的连接参数明文结构（与 PUT 请求字段对应）。
type githubAppSecretPayload struct {
	AppID         string `json:"app_id"`
	PrivateKeyPEM string `json:"private_key_pem"`
	WebhookSecret string `json:"webhook_secret"`
}

// resolveConfig 单个用户读侧的唯一入口：按「该用户的数据库行 > config.yaml >
// 未配置」解析当前生效身份。payload 是生效明文（config 兜底的 webhook 密钥已
// trim）；row 为数据库行（来源 config/none 时为 nil）；usable 表示 app_id 与
// 私钥齐备、可用于签发 App JWT。config 来源的私钥每次现读文件（换 key 文件同样
// 免重启）；读不到时身份不可用但不报错——页面必须仍能打开，让部署者改存数据库
// 配置救场。
func (s *IntegrationService) resolveConfig(userID string) (source string, row *model.Integration, payload githubAppSecretPayload, usable bool, err error) {
	dbRow, dbErr := s.repo.GetByProvider(userID, model.IntegrationProviderGitHubApp)
	if dbErr != nil && !errors.Is(dbErr, gorm.ErrRecordNotFound) {
		return githubAppSourceNone, nil, githubAppSecretPayload{}, false, errs.ErrInternal
	}
	if dbErr == nil {
		payload, decErr := decryptAppPayload(dbRow.ConfigEncrypted, []byte(s.cfg.Encryption.Key))
		if decErr != nil {
			return githubAppSourceDatabase, dbRow, githubAppSecretPayload{}, false, errGitHubAppDecrypt
		}
		usable = payload.AppID != "" && payload.PrivateKeyPEM != ""
		return githubAppSourceDatabase, dbRow, payload, usable, nil
	}

	if s.cfg.GitHubApp.AppID != "" && s.cfg.GitHubApp.PrivateKeyPath != "" {
		pem, readErr := os.ReadFile(s.cfg.GitHubApp.PrivateKeyPath)
		if readErr != nil {
			return githubAppSourceConfig, nil, githubAppSecretPayload{
				AppID:         s.cfg.GitHubApp.AppID,
				WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret),
			}, false, nil
		}
		return githubAppSourceConfig, nil, githubAppSecretPayload{
			AppID:         s.cfg.GitHubApp.AppID,
			PrivateKeyPEM: string(pem),
			WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret),
		}, true, nil
	}
	// 该用户未配置 App 身份；config 的 webhook 密钥仍保留给 webhook 验签（它不
	// 依赖 App 身份），保持 INT-93 以来仅配 webhook 的部署可用。
	return githubAppSourceNone, nil, githubAppSecretPayload{WebhookSecret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret)}, false, nil
}

// Current 返回该用户当前生效的 GitHub App 明文身份（githubapp.Clients 的唯一
// 入口）；ok=false 表示未配置。解密失败返回 50000，不回落 config。
func (s *IntegrationService) Current(ctx context.Context, userID string) (GitHubAppConfig, bool, error) {
	_, _, payload, usable, err := s.resolveConfig(userID)
	if err != nil {
		return GitHubAppConfig{}, false, err
	}
	return GitHubAppConfig{
		AppID:         payload.AppID,
		PrivateKeyPEM: []byte(payload.PrivateKeyPEM),
		WebhookSecret: payload.WebhookSecret,
	}, usable, nil
}

// WebhookCandidate webhook 验签的一个候选身份：AppUserID 为空串表示部署
// config 身份（config 密钥只有部署者知道，不可被用户伪造），非空是配置了该
// GitHub App 的用户。验签命中的候选身份必须贯穿后续的项目/规则匹配、取消与
// 评论/check 回写，不得跨身份串用。
type WebhookCandidate struct {
	AppUserID string
	Secret    string // 已 TrimSpace
}

// WebhookCandidates webhook 验签候选的现取入口（每次请求调用）：始终返回全部
// 身份候选——全部数据库行（逐行解密）+ config 兜底身份——由调用方逐个验签并
// 收集全部命中。targetAppID（X-GitHub-Hook-Installation-Target-ID 头）只当
// 排序提示，不承担正确性：GitHub 文档对该头只说「创建 webhook 的资源 ID」，
// 未保证 App 级投递上等于 App ID，所以身份归属只能靠验签本身；命中某行
// app_id 时把该 App 的候选排到最前（真实投递几乎总能第一把验中，也保证确定性）。
//
// config 身份在 config 配了 App ID、或 INT-93 以来只配了 webhook 密钥的旧部署
// （两者密钥都空时无候选可给）时在场；行内密钥以行为准（含为空，不回落
// config）。任何一行解密失败原样返回 50000。
func (s *IntegrationService) WebhookCandidates(ctx context.Context, targetAppID string) ([]WebhookCandidate, error) {
	targetAppID = strings.TrimSpace(targetAppID)
	rows, err := s.repo.ListByProvider(model.IntegrationProviderGitHubApp)
	if err != nil {
		return nil, errs.ErrInternal
	}
	matched := make([]WebhookCandidate, 0, len(rows)+1)
	rest := make([]WebhookCandidate, 0, len(rows)+1)
	for _, row := range rows {
		payload, decErr := s.decryptRow(row)
		if decErr != nil {
			return nil, decErr
		}
		cand := WebhookCandidate{AppUserID: row.UserID, Secret: strings.TrimSpace(payload.WebhookSecret)}
		if targetAppID != "" && row.AppID == targetAppID {
			matched = append(matched, cand)
		} else {
			rest = append(rest, cand)
		}
	}
	cfgID := strings.TrimSpace(s.cfg.GitHubApp.AppID)
	if cfgID != "" || strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret) != "" {
		cand := WebhookCandidate{AppUserID: "", Secret: strings.TrimSpace(s.cfg.GitHubApp.WebhookSecret)}
		if targetAppID != "" && cfgID == targetAppID {
			matched = append(matched, cand)
		} else {
			rest = append(rest, cand)
		}
	}
	return append(matched, rest...), nil
}

// Get GET /api/integrations/github-app 的投影：只给「是否已配置」，不触发任何
// GitHub 请求。
func (s *IntegrationService) Get(ctx context.Context, userID string) (*api.GitHubAppIntegration, error) {
	source, row, payload, _, err := s.resolveConfig(userID)
	if err != nil {
		return nil, err
	}
	return s.project(source, row, payload, nil), nil
}

// Update PUT /api/integrations/github-app：整组保存（app_id 必填、私钥省略=
// 保留、webhook_secret 省略=保留/空串=清空），保存后同步检测一次；检测失败
// 或凭证无效仍然 200，状态落库。
//
// 「读旧行 → 合并省略字段 → 加密 → 写新行」在一个短事务内完成（闭包内只用
// tx 句柄 + 纯 CPU 的 JSON/AES，config 私钥文件进事务前读好）——并发保存按
// 到达顺序串行化，后到者在自己的事务里读到先到者的结果，谁也不会覆盖丢谁；
// 检测在事务提交后执行，并用刚写入的密文做版本比对，慢检测覆盖不了新配置。
func (s *IntegrationService) Update(ctx context.Context, userID string, req *api.UpdateGitHubAppIntegrationRequest) (*api.GitHubAppIntegration, error) {
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
		// App ID 全库唯一（同 provider 下别的用户已占用 → 409）：webhook 验签
		// 候选逐行验签，重复行会让创建顺序靠后者永远收不到投递。与 config 同名
		// 不在此列——config 不是数据库行，用户收编 config 身份是正常迁移路径。
		taken, takenErr := s.repo.AppIDTakenByOtherTx(tx, model.IntegrationProviderGitHubApp, appID, userID)
		if takenErr != nil {
			return errs.ErrInternal
		}
		if taken {
			return errs.ErrIntegrationAppIDTaken
		}

		row, err := s.repo.GetByProviderTx(tx, userID, model.IntegrationProviderGitHubApp)
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
		if row != nil {
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
			// 省略私钥 = 保留已有：该用户没有数据库私钥时把 config 的私钥收编
			// 进行内，保证数据库行自洽（行存在后 config 不再被读取）。
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
		case row == nil:
			// 首次保存省略密钥 = 收编 config 的现值；已有行则保持旧值。
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
		now := time.Now().UTC()
		next := &model.Integration{
			ID:              uuid.NewString(),
			UserID:          userID,
			Provider:        model.IntegrationProviderGitHubApp,
			AppID:           appID,
			ConfigEncrypted: encrypted,
			DetectionStatus: model.IntegrationDetectionUnchecked,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if row != nil {
			next.CreatedAt = row.CreatedAt
		}
		if err := s.repo.UpsertTx(tx, next); err != nil {
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
		s.logger.Error("保存集成配置失败", zap.Error(err))
		return nil, errs.ErrInternal
	}

	// 保存后同步检测（与 POST test 同一路径）；失败/无效也落库并返回 200。
	s.runDetection(ctx, userID, written, detectGPL)
	return s.Get(ctx, userID)
}

// Test POST /api/integrations/github-app/test：对该用户当前生效配置做一次只读
// 检测。source=config 时没有数据库行可落库，结果只随本次响应返回、完全不写库；
// 未配置报 40006。
func (s *IntegrationService) Test(ctx context.Context, userID string) (*api.GitHubAppIntegration, error) {
	source, row, payload, usable, err := s.resolveConfig(userID)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, errGitHubAppNotConfigured
	}
	var version []byte
	if row != nil {
		version = row.ConfigEncrypted
	}
	detection := s.runDetection(ctx, userID, version, payload)
	if row == nil {
		return s.project(source, nil, payload, &detection), nil
	}
	return s.Get(ctx, userID)
}

// runDetection 执行一次检测并按密文版本条件落库（version 非 nil 时；nil 表示
// config 来源，没有行可落）。检测结果过期即丢弃，不影响调用方成败。
func (s *IntegrationService) runDetection(ctx context.Context, userID string, version []byte, payload githubAppSecretPayload) githubapp.Detection {
	detection := s.verify(ctx, payload.AppID, []byte(payload.PrivateKeyPEM), "")
	if version == nil {
		return detection
	}
	ok, err := s.repo.UpdateDetectionIfCurrent(userID, model.IntegrationProviderGitHubApp, version, detection.Status, detection.Detail, time.Now().UTC())
	if err != nil {
		s.logger.Warn("写入集成检测结果失败", zap.Error(err))
		return detection
	}
	if !ok {
		// 检测期间配置被再次保存：本次结果过期，丢弃（新配置有自己的检测）。
		s.logger.Info("集成检测结果已过期丢弃", zap.String("user_id", userID))
	}
	return detection
}

func (s *IntegrationService) decryptRow(row *model.Integration) (githubAppSecretPayload, error) {
	return decryptAppPayload(row.ConfigEncrypted, []byte(s.cfg.Encryption.Key))
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

// project 组装 GET/PUT/test 统一的响应投影。fresh 非空时覆盖检测三字段
// （source=config 的 test 结果没有数据库行可落库，只随本次响应返回）。
func (s *IntegrationService) project(source string, row *model.Integration, payload githubAppSecretPayload, fresh *githubapp.Detection) *api.GitHubAppIntegration {
	status := model.IntegrationDetectionUnchecked
	detail := ""
	var detectedAt *time.Time
	var updatedAt *string
	if row != nil {
		status = row.DetectionStatus
		if status == "" {
			status = model.IntegrationDetectionUnchecked
		}
		detail = row.DetectionDetail
		detectedAt = row.DetectedAt
		updatedAt = api.JSONTimePtr(&row.UpdatedAt)
	}
	if fresh != nil {
		now := time.Now().UTC()
		status = fresh.Status
		detail = fresh.Detail
		detectedAt = &now
	}

	// 私钥是否真的在读到的生效明文里：config 来源读不到文件时就是 false，
	// 不能以「配置里写了路径」冒充可用。
	privateKeySet := payload.PrivateKeyPEM != ""

	webhookURL := ""
	if base := strings.TrimRight(s.cfg.Server.PublicURL, "/"); base != "" {
		webhookURL = base + "/api/github/webhooks"
	}

	return &api.GitHubAppIntegration{
		Provider:         api.GithubApp,
		Configured:       source != githubAppSourceNone,
		Source:           githubAppSourceAPI(source),
		AppId:            payload.AppID,
		PrivateKeySet:    privateKeySet,
		WebhookSecretSet: payload.WebhookSecret != "",
		WebhookUrl:       webhookURL,
		DetectionStatus:  api.GitHubAppIntegrationDetectionStatus(status),
		DetectionDetail:  detail,
		DetectedAt:       api.JSONTimePtr(detectedAt),
		UpdatedAt:        updatedAt,
	}
}

func githubAppSourceAPI(source string) api.GitHubAppIntegrationSource {
	switch source {
	case githubAppSourceDatabase:
		return api.GitHubAppIntegrationSourceDatabase
	case githubAppSourceConfig:
		return api.GitHubAppIntegrationSourceConfig
	default:
		return api.GitHubAppIntegrationSourceNone
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
