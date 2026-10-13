package service

import (
	"errors"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ProjectCredentials 拥有项目 GitHub 凭证的读侧：按用途决定「这次操作用哪把钥匙」
// 并解密。Token 密文存在用户级 Token 库（user_github_tokens），项目只保存引用；
// 两个用途互不兜底——未配公开仓库 Token 不做 Issue 同步与 GitHub 写，未配代码
// 仓库 Token 匿名读公共 PR。service 层不再各自调 crypto.Decrypt——项目凭证的
// 加解密只允许出现在这个模块里（ai.go 的用户 AI Key 是另一套凭证，不受此约束）。
// 无状态——依赖在构造时固化，方法只读行不持久化（持久化归调用方）。
// 日志只带 project_id/token_id 与底层错误，绝不出现 token 明文、密文或请求结构体。
type ProjectCredentials struct {
	projectRepo *repository.ProjectRepository
	tokenRepo   *repository.UserGitHubTokenRepository
	cfg         *config.Config
	logger      *zap.Logger
}

func newProjectCredentials(projectRepo *repository.ProjectRepository, tokenRepo *repository.UserGitHubTokenRepository, cfg *config.Config, logger *zap.Logger) *ProjectCredentials {
	return &ProjectCredentials{
		projectRepo: projectRepo,
		tokenRepo:   tokenRepo,
		cfg:         cfg,
		logger:      logger,
	}
}

// requiredGitHubToken 解密项目公开仓库用途必须持有的 Token（Issue 同步与写、
// 分支查询、创建 Tag、发布 Release、上传安装包）。
// 未关联仓库返回 ErrProjectGitHubNotConfigured；已关联但未选公开仓库 Token 返回
// ErrProjectPublicGitHubTokenMissing；引用行缺失或解密失败（密文损坏或密钥不匹配）
// 记日志并返回 50000，不换另一把、不降级匿名。
func (c *ProjectCredentials) requiredGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	if project.PublicGitHubTokenID == "" {
		return nil, errs.ErrProjectPublicGitHubTokenMissing
	}
	return c.decryptToken(project, project.PublicGitHubTokenID, "公开仓库 Token")
}

// pullRequestCredential 决定一次 PR 读取（attach/sync）使用的凭证并解密。
// 已配代码仓库 Token 时只用它——解密失败报 50000 且不回退公开仓库 Token 或匿名；
// 未配代码仓库 Token 时匿名（即使公开仓库 Token 已配，也不借来读 PR）。
// 返回的 kind 标识本次凭证来源，供调用方在错误文案里标明。
func (c *ProjectCredentials) pullRequestCredential(project *model.Project) ([]byte, pullRequestCredentialKind, *errs.AppError) {
	if project.CodeGitHubTokenID == "" {
		return nil, pullRequestCredentialAnonymous, nil
	}
	tokenBytes, appErr := c.decryptToken(project, project.CodeGitHubTokenID, "代码仓库 Token")
	if appErr != nil {
		return nil, pullRequestCredentialCode, appErr
	}
	return tokenBytes, pullRequestCredentialCode, nil
}

// decryptToken 加载引用行并解密。错误文案写明用途——否则用户不知道该去检查
// 哪个 Token；日志带 project_id/token_id 与解密错误，绝不包含 token 明文或密文。
func (c *ProjectCredentials) decryptToken(project *model.Project, tokenID, purpose string) ([]byte, *errs.AppError) {
	token, err := c.tokenRepo.FindByID(tokenID, project.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.logger.Error("referenced github token missing",
				zap.String("project_id", project.ID), zap.String("token_id", tokenID))
			return nil, errs.New(errs.ErrInternal.Code, purpose+" 引用失效，请在项目设置中重新选择")
		}
		c.logger.Error("load github token failed",
			zap.String("project_id", project.ID), zap.String("token_id", tokenID), zap.Error(err))
		return nil, errs.ErrInternal
	}
	tokenBytes, err := crypto.Decrypt(token.TokenEncrypted, []byte(c.cfg.Encryption.Key))
	if err != nil {
		c.logger.Error("decrypt github token failed",
			zap.String("project_id", project.ID), zap.String("token_id", tokenID), zap.Error(err))
		return nil, errs.New(errs.ErrInternal.Code, purpose+" 解密失败，请在设置中替换该 Token")
	}
	return tokenBytes, nil
}

// pullRequestCredentialKind 标记一次 PR 访问选用的凭证来源。
// 错误文案需要区分本次用的是哪种凭证，用户排查权限问题时才知道该检查哪个 Token。
type pullRequestCredentialKind string

const (
	pullRequestCredentialCode      pullRequestCredentialKind = "code"
	pullRequestCredentialAnonymous pullRequestCredentialKind = "anonymous"
)

// usage 是凭证来源的谓语形式（「使用 X」）；匿名时没有 Token 可用，直接是「匿名访问」。
func (k pullRequestCredentialKind) usage() string {
	switch k {
	case pullRequestCredentialCode:
		return "使用代码仓库 Token"
	default:
		return "匿名访问"
	}
}

// CheckTokenReference 校验项目引用的 Token 属于当前用户且存在：
// 不存在（含他人 Token，跨用户不可见）返回 ErrGitHubTokenNotFound。
// 空 id 视为「未引用」，合法。create/update 写引用前调用。
func (c *ProjectCredentials) CheckTokenReference(userID, tokenID string) *errs.AppError {
	if tokenID == "" {
		return nil
	}
	if _, err := c.tokenRepo.FindByID(tokenID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrGitHubTokenNotFound
		}
		return errs.ErrInternal
	}
	return nil
}
