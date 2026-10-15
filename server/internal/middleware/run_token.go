package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

const (
	ContextKeyRunID  = "run_id"
	AuthTypeRunToken = "run_token"
)

// RequireRunToken 自动化 Agent 端点专用：仅接受 Bearer 运行令牌（fsr_）。
// 不支持 query 凭证（令牌出现在 URL 里会被代理/日志记录）；fsk_ API Key 明确
// 40304（API Key 不因此获得任何 Agent 能力），其余一律 40106。
//
// requireLive=true 的端点在运行结束后立即拒绝（令牌已消费）；
// requireLive=false 仅供回报端点：结束后仍允许完全相同的结束回报重放。
func RequireRunToken(agent *service.AutomationAgentService, requireLive bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := runTokenFromHeader(c)
		if raw == "" {
			response.Unauthorized(c, errs.ErrRunTokenInvalid.Code, "未提供运行令牌")
			c.Abort()
			return
		}
		if strings.HasPrefix(raw, "fsk_") {
			response.Forbidden(c, errs.ErrCredentialNotAllowed.Code, errs.ErrCredentialNotAllowed.Message)
			c.Abort()
			return
		}
		identity, err := agent.ResolveRunToken(c.Request.Context(), raw, requireLive)
		if err != nil {
			HandleAppError(c, err)
			c.Abort()
			return
		}
		c.Set(ContextKeyUserID, identity.UserID)
		c.Set(ContextKeyRunID, identity.RunID)
		c.Set(ContextKeyAuthType, AuthTypeRunToken)
		BindRequestUser(c)
		c.Next()
	}
}

// runTokenFromHeader 只解析 Authorization: Bearer <token>，大小写不敏感；
// 缺失或格式不对返回空串。
func runTokenFromHeader(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// GetRunID 从上下文获取运行令牌对应的运行 ID。
func GetRunID(c *gin.Context) string {
	return c.GetString(ContextKeyRunID)
}
