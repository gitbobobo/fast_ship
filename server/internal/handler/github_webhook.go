package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// GitHubWebhookHandler GitHub App webhook 入站（INT-93）。公开端点（无 JWT），
// 以 X-Hub-Signature-256 验签；密钥未配置一律 503（不接受未签名请求）。

type GitHubWebhookHandler struct {
	prReview *service.PRReviewService
	// secret 每次请求现读（构造时来自 config）。
	secret string
	// fake 自测模式注入的假 GitHub（server.mode=debug 且 github_app.fake 时非空，
	// 供 /api/debug/github-review-log 观察调用记录）。
	fake *githubapp.Fake
}

func NewGitHubWebhookHandler(prReview *service.PRReviewService, secret string, fake *githubapp.Fake) *GitHubWebhookHandler {
	return &GitHubWebhookHandler{prReview: prReview, secret: secret, fake: fake}
}

// Handle POST /api/github/webhooks
func (h *GitHubWebhookHandler) Handle(c *gin.Context) {
	secret := strings.TrimSpace(h.secret)
	if secret == "" {
		response.Error(c, 503, 50300, "Webhook 验签密钥未配置，拒绝接受请求")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookBodyMaxBytes))
	if err != nil {
		response.BadRequest(c, 40001, "读取请求体失败")
		return
	}

	signature := c.GetHeader("X-Hub-Signature-256")
	if !githubapp.VerifySignature(secret, body, signature) {
		response.Unauthorized(c, 40107, "Webhook 签名无效或缺失")
		return
	}

	delivery := strings.TrimSpace(c.GetHeader("X-GitHub-Delivery"))
	if delivery == "" {
		response.BadRequest(c, 40001, "缺少 X-GitHub-Delivery 头")
		return
	}
	event := strings.TrimSpace(c.GetHeader("X-GitHub-Event"))

	if h.fake != nil {
		githubapp.SeedFakeFromWebhook(h.fake, body)
	}

	if err := h.prReview.HandleGitHubWebhook(c.Request.Context(), event, delivery, body); err != nil {
		if errors.Is(err, service.ErrDeliveryDuplicated) {
			// 幂等重复投递：受理但不重复处理。
			c.Status(202)
			return
		}
		response.InternalError(c, "处理 webhook 失败")
		return
	}
	// 202：已受理（含 draft/fork/未关联仓库等忽略场景，不向 GitHub 区分）。
	c.Status(202)
}

// webhookBodyMaxBytes webhook payload 上限（GitHub PR 事件远小于此）。
const webhookBodyMaxBytes = 4 << 20

// DebugReviewLog GET /api/debug/github-review-log（fake + debug 模式专用，
// 不进 OpenAPI）：返回假 GitHub 的调用记录，本地自测评审链路用。
func (h *GitHubWebhookHandler) DebugReviewLog(c *gin.Context) {
	if h.fake == nil {
		response.Error(c, 404, 40400, "仅 github_app.fake=true 且 server.mode=debug 时可用")
		return
	}
	response.Success(c, gin.H{"calls": h.fake.CallCounts(), "total": h.fake.TotalCalls()})
}
