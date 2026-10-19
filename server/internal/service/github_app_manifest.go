package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// GitHub App Manifest 一键创建（INT-129 B 轮）：管理员在网页点「在 GitHub 上
// 创建 App」，服务端按部署 public_url 组装 manifest 并签发一次性 state；GitHub
// 创建完成回跳后用 code 换 App 凭证、加密落库为部署级身份。凭证形状与手动
// PUT 完全一致（{private_key_pem, webhook_secret} 的 AES-256-GCM），后续读取
// /webhook 验签路径不感知来源。

const (
	// manifestStateTTL state 的有效期：从发起创建到 GitHub 上填完表单回来，
	// 10 分钟足够；过期即作废重发。
	manifestStateTTL = 10 * time.Minute

	// manifestConvertTimeout conversions 换凭证请求的兜底超时。
	manifestConvertTimeout = 30 * time.Second
)

var errManifestResponseInvalid = errs.New(errs.ErrGitHubManifestFailed.Code, "GitHub App Manifest 转换失败")

// manifestStateEntry state 的登记内容：签发给哪位管理员、何时过期。
type manifestStateEntry struct {
	adminUserID string
	expiresAt   time.Time
}

// GitHubAppManifestService Manifest 创建流程的协调面。state 是一次性、进程内
// 的（map + 互斥锁，不落库）：使用即删、过期即弃；多副本部署不适用（A 副本
// 签发、回跳落到 B 副本时校验不到），单进程部署是当前形态。
type GitHubAppManifestService struct {
	settings *DeploymentSettingsService
	repo     *repository.DeploymentSettingsRepository
	cfg      *config.Config
	logger   *zap.Logger
	// verify 落库后紧接一次连接检测，main 默认接 githubapp.Verify；测试注入 fake。
	verify func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection

	mu     sync.Mutex
	states map[string]manifestStateEntry
	// now 可注入时钟（测试控制过期）。
	now func() time.Time
}

func NewGitHubAppManifestService(
	repo *repository.DeploymentSettingsRepository,
	settings *DeploymentSettingsService,
	cfg *config.Config,
	logger *zap.Logger,
) *GitHubAppManifestService {
	return &GitHubAppManifestService{
		settings: settings, repo: repo, cfg: cfg, logger: logger,
		verify: githubapp.Verify,
		states: map[string]manifestStateEntry{},
		now:    time.Now,
	}
}

// SetVerify 注入连接检测函数（测试用）。
func (s *GitHubAppManifestService) SetVerify(fn func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection) {
	s.verify = fn
}

// Create POST /api/deployment/github-app/manifest：组装 manifest 并签发一次性
// state。public_url 未配置 41202——没有对外地址就收不到 webhook，创建无意义。
func (s *GitHubAppManifestService) Create(ctx context.Context, adminUserID string, req *api.GitHubAppManifestRequest) (*api.GitHubAppManifestResponse, error) {
	publicURL := s.settings.PublicURL(ctx)
	if publicURL == "" {
		return nil, errs.ErrPublicURLMissing
	}

	actionURL := "https://github.com/settings/apps/new"
	if req != nil && req.Org != nil && strings.TrimSpace(*req.Org) != "" {
		actionURL = "https://github.com/organizations/" + url.PathEscape(strings.TrimSpace(*req.Org)) + "/settings/apps/new"
	}

	// App 名称带 host 尾注：多套部署（本地/生产）在同一账号下创建时肉眼可分。
	name := manifestAppName(publicURL)

	state := uuid.NewString()
	now := s.now()
	s.mu.Lock()
	s.pruneStatesLocked(now)
	s.states[state] = manifestStateEntry{adminUserID: adminUserID, expiresAt: now.Add(manifestStateTTL)}
	s.mu.Unlock()

	return &api.GitHubAppManifestResponse{
		ActionUrl: actionURL,
		State:     state,
		Manifest: api.GitHubAppManifest{
			Name: name,
			Url:  publicURL,
			HookAttributes: api.GitHubAppManifest_HookAttributes{
				Url:    publicURL + "/api/github/webhooks",
				Active: true,
			},
			RedirectUrl: publicURL + "/settings/integrations/github/callback",
			SetupUrl:    publicURL + "/settings/integrations",
			Public:      false,
			DefaultEvents: []api.GitHubAppManifestDefaultEvents{
				api.GitHubAppManifestDefaultEvents("pull_request"),
			},
			DefaultPermissions: api.GitHubAppManifest_DefaultPermissions{
				PullRequests: api.GitHubAppManifestDefaultPermissionsPullRequests("write"),
				Issues:       api.GitHubAppManifestDefaultPermissionsIssues("write"),
				Checks:       api.GitHubAppManifestDefaultPermissionsChecks("write"),
				Contents:     api.GitHubAppManifestDefaultPermissionsContents("read"),
				Metadata:     api.GitHubAppManifestDefaultPermissionsMetadata("read"),
			},
		},
	}, nil
}

// Complete POST /api/deployment/github-app/manifest/complete：校验并消费 state
// （不存在/过期/换管理员一律 40009），用 code 向 GitHub 换凭证后加密落库为
// 部署级身份，紧接一次连接检测写回 app_slug 与检测三列。GitHub 404/422（code
// 已用过或已过期）→ 40010；网络/其他非 2xx / 响应缺字段（id、slug、
// webhook_secret、合法私钥 PEM 任一缺失）→ 50205，此时不落库，既有部署身份
// 保持原样。
func (s *GitHubAppManifestService) Complete(ctx context.Context, adminUserID string, req *api.CompleteGitHubAppManifestRequest) error {
	entry, ok := s.consumeState(req.State)
	if !ok || s.now().After(entry.expiresAt) || entry.adminUserID != adminUserID {
		return errs.ErrGitHubManifestStateInvalid
	}

	converted, err := s.convertCode(ctx, req.Code)
	if err != nil {
		return err
	}

	payload := githubAppSecretPayload{
		AppID:         strconv.FormatInt(converted.AppID, 10),
		PrivateKeyPEM: converted.PrivateKeyPEM,
		WebhookSecret: strings.TrimSpace(converted.WebhookSecret),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return errs.ErrInternal
	}
	encrypted, err := crypto.Encrypt(raw, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return errs.ErrInternal
	}

	// 身份落库（app_slug 一并写入）并重置检测三列；真实检测在提交后跑，经
	// 密文版本比对写回，慢检测覆盖不了并发的新保存。
	if err := s.repo.Upsert(map[string]any{
		"app_id":           payload.AppID,
		"app_slug":         converted.Slug,
		"config_encrypted": encrypted,
		"detection_status": model.DeploymentDetectionUnchecked,
		"detection_detail": "",
		"detected_at":      nil,
	}); err != nil {
		s.logger.Error("保存 Manifest 创建的部署身份失败", zap.Error(err))
		return errs.ErrInternal
	}

	detection := s.verify(ctx, payload.AppID, []byte(payload.PrivateKeyPEM), s.cfg.GitHubApp.APIBaseURL)
	if _, err := s.repo.UpdateDetectionIfCurrent(encrypted, detection.Status, detection.Detail, detection.Slug, time.Now().UTC()); err != nil {
		s.logger.Warn("写入 Manifest 创建的检测结果失败", zap.Error(err))
	}
	return nil
}

// consumeState 取走一次性 state：存在与否都删除（重放、过期、换管理员统一
// 40009，不向调用方区分具体原因）。
func (s *GitHubAppManifestService) consumeState(state string) (manifestStateEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.states[state]
	delete(s.states, state)
	return entry, ok
}

// pruneStatesLocked 清理过期 state（Create 时顺带；锁必须已持有）。
func (s *GitHubAppManifestService) pruneStatesLocked(now time.Time) {
	for k, v := range s.states {
		if now.After(v.expiresAt) {
			delete(s.states, k)
		}
	}
}

// manifestConversion 换凭证结果里我们保留的字段（client_id/client_secret 用
// 不到：Fast Ship 以 App JWT + 安装令牌访问 GitHub，不走 OAuth）。
type manifestConversion struct {
	AppID         int64  `json:"id"`
	Slug          string `json:"slug"`
	PrivateKeyPEM string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
}

// convertCode 调 POST {apiBase}/app-manifests/{code}/conversions 换 App 凭证。
func (s *GitHubAppManifestService) convertCode(ctx context.Context, code string) (manifestConversion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := githubapp.APIBase(s.cfg.GitHubApp.APIBaseURL) + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return manifestConversion{}, errs.ErrInternal
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: manifestConvertTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return manifestConversion{}, errs.ErrGitHubManifestFailed
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return manifestConversion{}, errs.ErrGitHubManifestFailed
	}

	// code 是一次性授权码：404（不存在/已转换）与 422（校验失败）都是「这个
	// code 不能再用了」，回 40010 让管理员重新走创建流程。
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
		return manifestConversion{}, errs.ErrGitHubManifestCodeInvalid
	}
	if resp.StatusCode >= 300 {
		s.logger.Warn("GitHub Manifest conversions 返回异常状态",
			zap.Int("status", resp.StatusCode))
		return manifestConversion{}, errs.ErrGitHubManifestFailed
	}

	var out manifestConversion
	if err := json.Unmarshal(raw, &out); err != nil {
		s.logger.Warn("GitHub Manifest conversions 响应形状异常", zap.Error(err))
		return manifestConversion{}, errManifestResponseInvalid
	}
	// 逐项校验：任一缺失都不得落库——webhook 密钥为空会让此后所有 webhook 503，
	// 私钥不可解析则 App 无法签 JWT，这些都要在覆盖既有部署身份之前拦下。
	// 日志只记缺哪个字段，不打印字段内容。
	if out.AppID <= 0 {
		s.logger.Warn("GitHub Manifest conversions 响应缺少有效 id")
		return manifestConversion{}, errManifestResponseInvalid
	}
	if strings.TrimSpace(out.Slug) == "" {
		s.logger.Warn("GitHub Manifest conversions 响应缺少 slug")
		return manifestConversion{}, errManifestResponseInvalid
	}
	if strings.TrimSpace(out.WebhookSecret) == "" {
		s.logger.Warn("GitHub Manifest conversions 响应缺少 webhook_secret")
		return manifestConversion{}, errManifestResponseInvalid
	}
	if _, err := githubapp.ParsePrivateKeyPEM([]byte(out.PrivateKeyPEM)); err != nil {
		s.logger.Warn("GitHub Manifest conversions 响应的私钥 PEM 无法解析")
		return manifestConversion{}, errManifestResponseInvalid
	}
	return out, nil
}

// hostOf 取 URL 的 host（解析失败为空串）；manifest 的 App 名称用。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// githubAppNameMaxRunes GitHub 对 App 名称的长度上限（注册文档：34 个字符）。
// 超长时 GitHub 的预填表单会直接不合规，管理员得手动改名——一键创建就断了。
const githubAppNameMaxRunes = 34

// manifestAppName 拼「Fast Ship (<host>)」。超过长度上限就从 host 尾部截断
// 并加 `~` 标记，保留前缀（子域从左向左读更有区分度）。
func manifestAppName(publicURL string) string {
	const base = "Fast Ship"
	host := hostOf(publicURL)
	if host == "" {
		return base
	}
	build := func(h string, truncated bool) string {
		if truncated {
			return base + " (" + h + "~)"
		}
		return base + " (" + h + ")"
	}
	if name := build(host, false); utf8.RuneCountInString(name) <= githubAppNameMaxRunes {
		return name
	}
	runes := []rune(host)
	for len(runes) > 0 && utf8.RuneCountInString(build(string(runes), true)) > githubAppNameMaxRunes {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return base
	}
	return build(string(runes), true)
}
