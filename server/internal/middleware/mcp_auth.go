package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// OAuthWellKnownPath 受保护资源元数据发现路径（RFC 9728）；挑战里的
// resource_metadata 指向 {BaseURL} + 此路径。
const OAuthWellKnownPath = "/.well-known/oauth-protected-resource"

// RequireMCPToken MCP 资源服务器的 Bearer 鉴权中间件（INT-222）：仅接受
// `fsm_` 前缀的 OAuth access token，校验通过后把令牌信息（user_id、scopes）
// 注入请求 context，供 /mcp 工具层做 scope 判定。
//
// 401 响应按 RFC 6750 挑战格式：WWW-Authenticate 始终带 resource_metadata
// 指向受保护资源元数据（{PublicURL}/.well-known/oauth-protected-resource）；
// 令牌无效时附加 error="invalid_token"。不套 Fast Ship envelope；令牌明文
// 不落日志。
func RequireMCPToken(oauthService *service.OAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractBearerToken(c)
		if raw == "" {
			mcpChallenge(c, oauthService, false)
			return
		}
		// 令牌族隔离：非 fsm_ 前缀一律按无效处理，不进入哈希查询，也不落日志。
		if !strings.HasPrefix(raw, service.OAuthAccessTokenPrefix) {
			mcpChallenge(c, oauthService, true)
			return
		}

		info, err := oauthService.ValidateAccessToken(c.Request.Context(), raw)
		if err != nil {
			mcpChallenge(c, oauthService, true)
			return
		}

		c.Request = c.Request.WithContext(service.ContextWithMCPAuth(c.Request.Context(), info))
		c.Next()
	}
}

// mcpChallenge 返回 401 + WWW-Authenticate 挑战。invalid 为 true 时按 RFC 6750
// §3 附加 error="invalid_token"；body 用简短文本，响应不套 Fast Ship envelope。
func mcpChallenge(c *gin.Context, oauthService *service.OAuthService, invalid bool) {
	base := oauthService.BaseURL(c.Request.Context())
	// 与 well-known / token 端点同语义：未配置 public_url 时 OAuth 面整体不可用，
	// 且挑战里写不出绝对 resource_metadata（RFC 9728 要求绝对 URL），按 503 拒绝。
	if base == "" {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":             "temporarily_unavailable",
			"error_description": "尚未配置 public_url，请先在「设置 → 部署」填写对外访问地址",
		})
		c.Abort()
		return
	}
	params := []string{`resource_metadata="` + base + OAuthWellKnownPath + `"`}
	if invalid {
		params = append(params, `error="invalid_token"`)
	}
	c.Header("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusUnauthorized, "Unauthorized")
	c.Abort()
}
