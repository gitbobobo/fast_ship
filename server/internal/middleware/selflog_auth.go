package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// RequireSelfLogAuth 是 Fast Ship 自身日志查询端点（INT-94）的鉴权：
// JWT（Web 管理员）与 fsk_ API Key（代理）均可；fsr_ 运行/刷新令牌是
// 作用域受限凭证，无权读取系统日志，返回 40301；其余走 JWT 校验。
func RequireSelfLogAuth(cfg *config.Config, apiKeyRepo *repository.ApiKeyRepository, authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearerToken(c)
		if strings.HasPrefix(token, "fsr_") {
			response.Forbidden(c, errs.ErrApiKeyForbidden.Code, "运行令牌/刷新令牌无权查询 Fast Ship 自身日志")
			c.Abort()
			return
		}
		requireAuth(cfg, apiKeyRepo, authService, extractBearerToken)(c)
	}
}
