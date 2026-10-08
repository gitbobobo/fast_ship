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
