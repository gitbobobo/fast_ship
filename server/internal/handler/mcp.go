package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/godbobo/fast_ship/server/internal/service"
)

// MCPHandler /mcp Streamable HTTP 协议端点（INT-222）。
//
// 无状态模式（Stateless）：不分配 Mcp-Session-Id，每个 POST 请求经 per-request
// factory 构造绑定当前用户 scope 的 server（授权隔离，令牌吊销即时生效），
// GET/DELETE 按 SEP-2567 返回 405。JSONResponse 让工具调用以 application/json
// 返回，不经 SSE。
//
// DisableLocalhostProtection 关闭 SDK 的 DNS rebinding 防护：生产部署通常由
// 同机反代经回环地址转发，Host 是公网域名，SDK 默认防护会整体 403；本端点
// 强制 Bearer 令牌，无令牌请求只得到 401 挑战，浏览器侧 rebinding 拿不到
// 任何数据（见 docs/internal/oauth-mcp.md）。
type MCPHandler struct {
	streamable *mcpsdk.StreamableHTTPHandler
}

// NewMCPHandler 构造 /mcp 端点；svc.NewServer 作为 per-request server factory。
func NewMCPHandler(svc *service.MCPService) *MCPHandler {
	return &MCPHandler{
		streamable: mcpsdk.NewStreamableHTTPHandler(
			func(r *http.Request) *mcpsdk.Server {
				return svc.NewServer(service.MCPAuthFromContext(r.Context()))
			},
			&mcpsdk.StreamableHTTPOptions{
				Stateless:                  true,
				JSONResponse:               true,
				DisableLocalhostProtection: true,
			},
		),
	}
}

// Handle gin 适配：请求 context 已由 middleware.RequireMCPToken 注入令牌信息。
func (h *MCPHandler) Handle(c *gin.Context) {
	h.streamable.ServeHTTP(c.Writer, c.Request)
}
