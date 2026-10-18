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

// headerGitHubAppID GitHub App webhook 携带的 App ID 头：入站没有用户上下文，
// 靠它把验签密钥定位到配置了这个 App 的用户（INT-119 用户级集成）。
const headerGitHubAppID = "X-GitHub-Hook-Installation-Target-ID"

// WebhookSecretSource 每次请求现取 webhook 验签候选（INT-119）：targetAppID
// 取自 App ID 头，可为空（不带头部的自测调用）；实现方按它返回全部候选身份
// （AppUserID 空串=部署 config 身份），未知 App 报 401 类错误。
type WebhookSecretSource interface {
	WebhookCandidates(ctx context.Context, targetAppID string) ([]service.WebhookCandidate, error)
}

// StaticWebhookSecret 固定密钥来源（config.yaml / fake 自测模式）：不区分
// App 身份，任何目标都返回同一候选（密钥 trim 后、身份为部署 config）。
type StaticWebhookSecret string

func (s StaticWebhookSecret) WebhookCandidates(context.Context, string) ([]service.WebhookCandidate, error) {
	return []service.WebhookCandidate{{AppUserID: "", Secret: strings.TrimSpace(string(s))}}, nil
}

// GitHubWebhookHandler GitHub App webhook 入站（INT-93）。公开端点（无 JWT），
// 以 X-Hub-Signature-256 验签；候选全部无密钥一律 503（不接受未签名请求）。

type GitHubWebhookHandler struct {
	prReview *service.PRReviewService
	// secrets 每次请求现取候选：按 App ID 头定位到各用户的行内密钥，逐候选
	// 验签，命中候选的身份贯穿后续处理（含为空 → 503）。
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
	candidates, err := h.secrets.WebhookCandidates(c.Request.Context(), strings.TrimSpace(c.GetHeader(headerGitHubAppID)))
	if err != nil {
		// 未知 App（40107）、数据库密文解密失败（50000）等：原样映射，不回落 config。
		middleware.HandleAppError(c, err)
		return
	}
	configured := false
	for i := range candidates {
		if candidates[i].Secret != "" {
			configured = true
			break
		}
	}
	if !configured {
		response.Error(c, 503, 50300, "Webhook 验签密钥未配置，拒绝接受请求")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookBodyMaxBytes))
	if err != nil {
		response.BadRequest(c, 40001, "读取请求体失败")
		return
	}

	// 逐候选验签：同一 App ID 的候选（用户行 + config 同名身份）各用自己
	// 的密钥试一遍，命中者的身份贯穿后续处理；全不命中才是签名无效。
	// App ID 在保存时已按 (provider, app_id) 唯一挡重，不会有多用户同名行。
	signature := c.GetHeader("X-Hub-Signature-256")
	var appUserID string
	verified := false
	for i := range candidates {
		if githubapp.VerifySignature(candidates[i].Secret, body, signature) {
			verified = true
			appUserID = candidates[i].AppUserID
			break
		}
	}
	if !verified {
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

	if err := h.prReview.HandleGitHubWebhook(c.Request.Context(), event, delivery, body, appUserID); err != nil {
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
