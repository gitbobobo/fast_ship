package service

import (
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
)

// requiredProjectGitHubToken 解密项目必须持有的 GitHub Token。
// 项目未配置 GitHub（owner/repo/token 任一为空）时返回 ErrProjectGitHubNotConfigured；
// 解密失败（密文损坏或密钥不匹配）记日志并返回 ErrInternal。
func requiredProjectGitHubToken(project *model.Project, cfg *config.Config, logger *zap.Logger) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	return decryptProjectGitHubToken(project, cfg, logger)
}

// optionalProjectGitHubToken 解密项目可选的 GitHub Token。
// 项目未配置 GitHub 时返回 (nil, nil)，由调用方决定降级路径（如 PR 关联的匿名访问）；
// 已配置但解密失败同样记日志并返回 ErrInternal——不静默退回匿名。
func optionalProjectGitHubToken(project *model.Project, cfg *config.Config, logger *zap.Logger) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, nil
	}
	return decryptProjectGitHubToken(project, cfg, logger)
}

// decryptProjectGitHubToken 是两者的公共实现：只做解密与失败日志，凭证是否必需由外层函数表达。
// 日志带 project_id 与解密错误，绝不包含 token 明文或密文。
func decryptProjectGitHubToken(project *model.Project, cfg *config.Config, logger *zap.Logger) ([]byte, *errs.AppError) {
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(cfg.Encryption.Key))
	if err != nil {
		logger.Error("decrypt github token failed", zap.String("project_id", project.ID), zap.Error(err))
		return nil, errs.ErrInternal
	}
	return tokenBytes, nil
}

// pullRequestCredentialKind 标记一次 PR 访问选用的凭证来源。
// 错误文案需要区分本次用的是哪种凭证，用户排查权限问题时才知道该检查哪个 Token。
type pullRequestCredentialKind string

const (
	pullRequestCredentialPR        pullRequestCredentialKind = "pr"
	pullRequestCredentialProject   pullRequestCredentialKind = "project"
	pullRequestCredentialAnonymous pullRequestCredentialKind = "anonymous"
)

// name 是写入错误文案的凭证名称。
func (k pullRequestCredentialKind) name() string {
	switch k {
	case pullRequestCredentialPR:
		return "PR 访问 Token"
	case pullRequestCredentialProject:
		return "项目 Token"
	default:
		return "匿名访问"
	}
}

// usage 是 name 的谓语形式（「使用 X」）；匿名时没有 Token 可用，直接是「匿名访问」。
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

// resolvePullRequestCredential 决定一次 PR 读取（attach/sync）使用的凭证并解密。
// 项目已配独立 PR Token 时优先且只使用它：解密失败记日志返回 50000 且不静默回退
// 项目 Token 或匿名；未配 PR Token 时回退 optional 项目 Token 语义（已配项目 Token
// 用之，未配置则匿名）。PR Token 是否可用不依赖项目是否配置了反馈仓库。
// 解密失败的错误文案写明是哪种凭证——否则用户不知道该去检查哪个 Token；
// 日志只带 project_id 与解密错误，绝不包含 token 明文或密文。
// 返回的 kind 标识本次凭证来源，供调用方在错误文案里标明。
func resolvePullRequestCredential(project *model.Project, cfg *config.Config, logger *zap.Logger) ([]byte, pullRequestCredentialKind, *errs.AppError) {
	if len(project.GithubPRTokenEncrypted) > 0 {
		tokenBytes, err := crypto.Decrypt(project.GithubPRTokenEncrypted, []byte(cfg.Encryption.Key))
		if err != nil {
			logger.Error("decrypt github pr token failed", zap.String("project_id", project.ID), zap.Error(err))
			return nil, pullRequestCredentialPR, errs.New(errs.ErrInternal.Code, "PR 访问 Token 解密失败，请在项目设置中重新配置")
		}
		return tokenBytes, pullRequestCredentialPR, nil
	}
	tokenBytes, appErr := optionalProjectGitHubToken(project, cfg, logger)
	if appErr != nil {
		return nil, pullRequestCredentialProject, errs.New(errs.ErrInternal.Code, "项目 Token 解密失败，请在项目设置中重新配置")
	}
	if tokenBytes == nil {
		return nil, pullRequestCredentialAnonymous, nil
	}
	return tokenBytes, pullRequestCredentialProject, nil
}
