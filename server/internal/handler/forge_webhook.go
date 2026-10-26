package handler

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// ForgeWebhookSecretSource 按集成现取 Forgejo webhook 验签密钥：found=false
// 表示集成不存在（404），密钥 trim 后为空表示未配置（503 拒收）。
type ForgeWebhookSecretSource interface {
	WebhookSecret(ctx context.Context, integrationID string) (secret string, found bool, err error)
}

// ForgeWebhookHandler Forgejo webhook 入站（INT-167 任务 3）。公开端点（无
// JWT），按集成行的 webhook secret 以 X-Forgejo-Signature（纯 hex HMAC-SHA256，
// 无前缀）验签；任何写操作都在验签之后——验签失败路径零写入（含 delivery
// 幂等账）。签名对不上 401，与 GitHub 端点同一组错误码。
// 头名回退 X-Gitea-*：Gitea <1.19 只发旧头，新 Gitea/Forgejo 两套都发，先取
// Forgejo 头、缺省回退旧头。
type ForgeWebhookHandler struct {
	review *service.ReviewService
	// secrets 每次请求现取集成密钥（集成行编辑/换密钥后立即生效）。
	secrets ForgeWebhookSecretSource
}

func NewForgeWebhookHandler(review *service.ReviewService, secrets ForgeWebhookSecretSource) *ForgeWebhookHandler {
	return &ForgeWebhookHandler{review: review, secrets: secrets}
}

// Handle POST /api/forge/:id/webhooks
func (h *ForgeWebhookHandler) Handle(c *gin.Context) {
	integrationID := strings.TrimSpace(c.Param("id"))

	secret, found, err := h.secrets.WebhookSecret(c.Request.Context(), integrationID)
	if err != nil {
		// 数据库密文解密失败（50000）等：原样映射。
		middleware.HandleAppError(c, err)
		return
	}
	if !found {
		// 与验签失败同回 401：验签要用行内密钥，行不存在时无签可验——
		// 回 404 会让未鉴权调用方拿响应码探测集成 id 是否存在。
		response.Unauthorized(c, 40107,
			"Webhook 签名无效或缺失（仅支持 X-Forgejo-Signature / X-Gitea-Signature）")
		return
	}
	if secret == "" {
		response.Error(c, 503, 50300, "Webhook 验签密钥未配置，拒绝接受请求")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookBodyMaxBytes))
	if err != nil {
		response.BadRequest(c, 40001, "读取请求体失败")
		return
	}

	if !forge.VerifyForgejoSignature(secret, body, forgeHeader(c, "Signature")) {
		// 文案点明只认原生头：实例端若错配成 GitHub 兼容格式
		// （X-Hub-Signature-256），401 时管理员能直接看出是头格式问题。
		response.Unauthorized(c, 40107,
			"Webhook 签名无效或缺失（仅支持 X-Forgejo-Signature / X-Gitea-Signature）")
		return
	}

	delivery := strings.TrimSpace(forgeHeader(c, "Delivery"))
	if delivery == "" {
		response.BadRequest(c, 40001, "缺少 X-Forgejo-Delivery 头")
		return
	}
	event := strings.TrimSpace(forgeHeader(c, "Event"))

	if err := h.review.HandleForgejoWebhook(c.Request.Context(), integrationID, event, delivery, body); err != nil {
		if errors.Is(err, service.ErrDeliveryDuplicated) {
			// 幂等重复投递：受理但不重复处理。
			c.Status(202)
			return
		}
		response.InternalError(c, "处理 webhook 失败")
		return
	}
	// 202：已受理（含 draft/fork/未关联仓库等忽略场景，不向平台区分）。
	c.Status(202)
}

// forgeHeader 取 webhook 头：X-Forgejo-{name} 优先，缺省回退 X-Gitea-{name}
// （Gitea <1.19 的旧实例只发后者）。
func forgeHeader(c *gin.Context, name string) string {
	if v := c.GetHeader("X-Forgejo-" + name); v != "" {
		return v
	}
	return c.GetHeader("X-Gitea-" + name)
}
