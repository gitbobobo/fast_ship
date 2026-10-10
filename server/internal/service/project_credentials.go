package service

import (
	"errors"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ProjectCredentials 拥有项目 GitHub 凭证的存取、解密与种类选择：
// 写侧负责明文加密与源项目密文复用（create/update 的 token 输入解析），
// 读侧负责解密与「本次操作用哪把钥匙」的判定（required/optional/PR 凭证选择）。
// 调用方只问「这个项目做 X 用哪把钥匙」，不再直接碰 crypto 与密文列。
// 无状态——依赖在构造时固化，方法只读 project 行不持久化（持久化归调用方）。
// 日志只带 project_id 与底层错误，绝不出现 token 明文、密文或请求结构体。
type ProjectCredentials struct {
	projectRepo *repository.ProjectRepository
	cfg         *config.Config
	logger      *zap.Logger
}

func newProjectCredentials(projectRepo *repository.ProjectRepository, cfg *config.Config, logger *zap.Logger) *ProjectCredentials {
	return &ProjectCredentials{
		projectRepo: projectRepo,
		cfg:         cfg,
		logger:      logger,
	}
}

// requiredGitHubToken 解密项目必须持有的 GitHub Token。
// 项目未配置 GitHub（owner/repo/token 任一为空）时返回 ErrProjectGitHubNotConfigured；
// 解密失败（密文损坏或密钥不匹配）记日志并返回 ErrInternal。
func (c *ProjectCredentials) requiredGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	return c.decryptGitHubToken(project)
}

// optionalGitHubToken 解密项目可选的 GitHub Token。
// 项目未配置 GitHub 时返回 (nil, nil)，由调用方决定降级路径（如 PR 关联的匿名访问）；
// 已配置但解密失败同样记日志并返回 ErrInternal——不静默退回匿名。
func (c *ProjectCredentials) optionalGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, nil
	}
	return c.decryptGitHubToken(project)
}

// decryptGitHubToken 是两者的公共实现：只做解密与失败日志，凭证是否必需由外层函数表达。
// 日志带 project_id 与解密错误，绝不包含 token 明文或密文。
func (c *ProjectCredentials) decryptGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(c.cfg.Encryption.Key))
	if err != nil {
		c.logger.Error("decrypt github token failed", zap.String("project_id", project.ID), zap.Error(err))
		return nil, errs.ErrInternal
	}
	return tokenBytes, nil
}

// pullRequestCredentialKind 标记一次 PR 访问选用的凭证来源。
// 错误文案需要区分本次用的是哪种凭证，用户排查权限问题时才知道该检查哪个 Token。
// 与写侧 api.PrTokenSourceKind（access/pr，复制源项目哪一列）是两个概念：
// 这里描述的是本次请求的凭证来源，Access Token 复制进 PR 字段后来源仍记为 pr。
type pullRequestCredentialKind string

const (
	pullRequestCredentialPR        pullRequestCredentialKind = "pr"
	pullRequestCredentialProject   pullRequestCredentialKind = "project"
	pullRequestCredentialAnonymous pullRequestCredentialKind = "anonymous"
)

// usage 是凭证来源的谓语形式（「使用 X」）；匿名时没有 Token 可用，直接是「匿名访问」。
func (k pullRequestCredentialKind) usage() string {
	switch k {
	case pullRequestCredentialPR:
		return "使用 PR 访问 Token"
	case pullRequestCredentialProject:
		return "使用项目 Token"
	default:
		return "匿名访问"
	}
}

// pullRequestCredential 决定一次 PR 读取（attach/sync）使用的凭证并解密。
// 项目已配独立 PR Token 时优先且只使用它：解密失败记日志返回 50000 且不静默回退
// 项目 Token 或匿名；未配 PR Token 时回退 optional 项目 Token 语义（已配项目 Token
// 用之，未配置则匿名）。PR Token 是否可用不依赖项目是否配置了反馈仓库。
// 解密失败的错误文案写明是哪种凭证——否则用户不知道该去检查哪个 Token；
// 日志只带 project_id 与解密错误，绝不包含 token 明文或密文。
// 返回的 kind 标识本次凭证来源，供调用方在错误文案里标明。
func (c *ProjectCredentials) pullRequestCredential(project *model.Project) ([]byte, pullRequestCredentialKind, *errs.AppError) {
	if len(project.GithubPRTokenEncrypted) > 0 {
		tokenBytes, err := crypto.Decrypt(project.GithubPRTokenEncrypted, []byte(c.cfg.Encryption.Key))
		if err != nil {
			c.logger.Error("decrypt github pr token failed", zap.String("project_id", project.ID), zap.Error(err))
			return nil, pullRequestCredentialPR, errs.New(errs.ErrInternal.Code, "PR 访问 Token 解密失败，请在项目设置中重新配置")
		}
		return tokenBytes, pullRequestCredentialPR, nil
	}
	tokenBytes, appErr := c.optionalGitHubToken(project)
	if appErr != nil {
		return nil, pullRequestCredentialProject, errs.New(errs.ErrInternal.Code, "项目 Token 解密失败，请在项目设置中重新配置")
	}
	if tokenBytes == nil {
		return nil, pullRequestCredentialAnonymous, nil
	}
	return tokenBytes, pullRequestCredentialProject, nil
}

// projectPRTokenInput 是 resolvePRToken 的输入四元组，与 create/update 请求的
// PR Token 字段一一对应。指针语义：nil 表示字段未提供（区别于指向空串）。
type projectPRTokenInput struct {
	plaintext       *string
	sourceProjectID *string
	sourceKind      *api.PrTokenSourceKind
	clear           *bool
}

// resolveGitHubToken 从 Token 字符串或源项目解析加密后的 Token，优先使用 sourceProjectID。
// 源项目不存在返回 ErrProjectNotFound；源项目未配置 Access Token 返回 40001——
// 与 resolvePRTokenFromSource 一致，不允许静默写入空密文。
func (c *ProjectCredentials) resolveGitHubToken(userID, plaintext, sourceProjectID string) ([]byte, error) {
	if sourceProjectID != "" {
		sourceProject, err := c.projectRepo.FindByID(sourceProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		if len(sourceProject.GithubTokenEncrypted) == 0 {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 所选项目未配置 GitHub Access Token")
		}
		return sourceProject.GithubTokenEncrypted, nil
	}
	if plaintext != "" {
		encryptedToken, err := crypto.Encrypt([]byte(plaintext), []byte(c.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
		}
		return encryptedToken, nil
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 请输入 GitHub Token 或选择复用已有项目的 Token")
}

// resolvePRToken 编排 PR 访问 Token 的最终密文，create/update 共用同一组判定：
// ①clear 与 plaintext/source 同时显式提供（指针非 nil，不论取值）→ 40001；
// ②validatePRTokenSourceKind：kind 单独提供或取值非法 → 40001；
// ③clear=true → 返回 nil（显式清除，恢复沿用项目 Token）；
// ④source 非空 → 复制源项目密文；⑤plaintext 非空 → 加密；
// ⑥否则返回 currentCiphertext（create 传 nil 即不配置，update 传现值即保留）。
func (c *ProjectCredentials) resolvePRToken(userID string, currentCiphertext []byte, input projectPRTokenInput) ([]byte, error) {
	if input.clear != nil && (input.plaintext != nil || input.sourceProjectID != nil) {
		return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": clear_github_pr_token 与 github_pr_token 或 pr_token_source_project_id 不能同时提供")
	}
	kind, err := validatePRTokenSourceKind(input.sourceKind, api.Deref(input.sourceProjectID))
	if err != nil {
		return nil, err
	}
	if api.Deref(input.clear) {
		return nil, nil
	}
	if sourceID := api.Deref(input.sourceProjectID); sourceID != "" {
		return c.resolvePRTokenFromSource(userID, sourceID, kind)
	}
	if plaintext := api.Deref(input.plaintext); plaintext != "" {
		encryptedToken, err := crypto.Encrypt([]byte(plaintext), []byte(c.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
		}
		return encryptedToken, nil
	}
	return currentCiphertext, nil
}

// validatePRTokenSourceKind 归一化 pr_token_source_kind：未提供返回缺省
// api.Pr；单独提供（不带 pr_token_source_project_id）或取值不在 access/pr
// 内均返回 40001。互斥判定（与 clear_github_pr_token）不经过此函数。
func validatePRTokenSourceKind(kind *api.PrTokenSourceKind, sourceProjectID string) (api.PrTokenSourceKind, error) {
	if kind == nil {
		return api.Pr, nil
	}
	if sourceProjectID == "" {
		return "", errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": pr_token_source_kind 需与 pr_token_source_project_id 搭配提供")
	}
	if !kind.Valid() {
		return "", errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": pr_token_source_kind 取值无效（可选 access、pr）")
	}
	return *kind, nil
}

// resolvePRTokenFromSource 复制源项目的凭证密文作为本项目的 PR 访问 Token（与
// resolveGitHubToken 的 source 分支一致，密文直接复用不解密重加密）。
// kind=access 复制源项目的 GitHub Access Token，kind=pr（缺省）复制 PR 访问
// Token。源项目不存在返回 ErrProjectNotFound；未配置对应凭证返回 40001。
func (c *ProjectCredentials) resolvePRTokenFromSource(userID, sourceProjectID string, kind api.PrTokenSourceKind) ([]byte, error) {
	sourceProject, err := c.projectRepo.FindByID(sourceProjectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	if kind == api.Access {
		if len(sourceProject.GithubTokenEncrypted) == 0 {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 所选项目未配置 GitHub Access Token")
		}
		return sourceProject.GithubTokenEncrypted, nil
	}
	if len(sourceProject.GithubPRTokenEncrypted) == 0 {
		return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 所选项目未配置 PR 访问 Token")
	}
	return sourceProject.GithubPRTokenEncrypted, nil
}
