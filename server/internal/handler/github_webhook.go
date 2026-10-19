package handler

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// WebhookSecretSource 每次请求现取部署唯一的 webhook 验签密钥（INT-129：每套
// 部署一个 GitHub App，验签不再有候选集）；trim 后为空表示未配置（503 拒收）。
type WebhookSecretSource interface {
	WebhookSecret(ctx context.Context) (string, error)
}

// StaticWebhookSecret 固定密钥来源（config.yaml / fake 自测模式）。
type StaticWebhookSecret string

func (s StaticWebhookSecret) WebhookSecret(context.Context) (string, error) {
	return strings.TrimSpace(string(s)), nil
}

// GitHubWebhookHandler GitHub App webhook 入站（INT-93）。公开端点（无 JWT），
// 以 X-Hub-Signature-256 按部署唯一密钥验签；密钥未配置一律 503（不接受未签名
// 请求），签名对不上 401。
type GitHubWebhookHandler struct {
	prReview *service.PRReviewService
	// secrets 每次请求现取密钥（部署设置行或 config 兜底），保存后立即生效。
	secrets WebhookSecretSource
	// fake 自测模式注入的假 GitHub（server.mode=debug 且 github_app.fake 时非空，
	// 供 /api/debug/github-review-log 观察调用记录）。
	fake *githubapp.Fake
}

func NewGitHubWebhookHandler(prReview *service.PRReviewService, secrets WebhookSecretSource, fake *githubapp.Fake) *GitHubWebhookHandler {
	return &GitHubWebhookHandler{prReview: prReview, secrets: secrets, fake: fake}
}

// Handle POST /api/github/webhooks
func (h *GitHubWebhookHandler) Handle(c *gin.Context) {
	secret, err := h.secrets.WebhookSecret(c.Request.Context())
	if err != nil {
		// 数据库密文解密失败（50000）等：原样映射，不回落 config。
		middleware.HandleAppError(c, err)
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

	if !githubapp.VerifySignature(secret, body, c.GetHeader("X-Hub-Signature-256")) {
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
