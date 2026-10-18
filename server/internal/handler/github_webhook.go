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

// headerGitHubAppID GitHub App webhook 携带的目标 ID 头：只当候选排序提示，
// 不承担正确性——GitHub 文档对它只说「创建 webhook 的资源 ID」，未保证 App 级
// 投递上等于 App ID（仓库级投递就是仓库 ID）。身份归属只认验签结果。
const headerGitHubAppID = "X-GitHub-Hook-Installation-Target-ID"

// WebhookSecretSource 每次请求现取 webhook 验签候选（INT-119）：targetAppID
// 取自目标 ID 头、只作排序提示，可为空；实现方返回全部候选身份（AppUserID
// 空串=部署 config 身份），由 handler 逐个验签。
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
	// secrets 每次请求现取候选：全部身份（各用户的行 + config），逐候选验签，
	// 命中候选的身份贯穿后续处理（含为空 → 503）。
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
		// 数据库密文解密失败（50000）等：原样映射，不回落 config。
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

	// 逐候选验签并收集全部命中：候选是全部身份（各用户的行 + config），密钥
	// 互不相同的身份至多一把验中——命中恰好一个即认它的身份；命中多个说明多
	// 份身份配了相同的 webhook 密钥，无法安全归属（投递可能来自其中任何一份），
	// 拒绝；全不命中才是签名无效。
	signature := c.GetHeader("X-Hub-Signature-256")
	matched := make([]service.WebhookCandidate, 0, len(candidates))
	for i := range candidates {
		if githubapp.VerifySignature(candidates[i].Secret, body, signature) {
			matched = append(matched, candidates[i])
		}
	}
	if len(matched) > 1 {
		// 用户行接管部署 config 的同一个 App（首次保存收编 config 密钥）时两把
		// 都会验中：同 App 只保留用户行，这是「从部署配置迁到个人配置」的默认
		// 路径；不同身份仍共享同一密钥则无法安全归属，交给下面的多命中分支拒绝。
		userAppIDs := map[string]bool{}
		for _, cand := range matched {
			if cand.AppUserID != "" {
				userAppIDs[cand.AppID] = true
			}
		}
		kept := matched[:0]
		for _, cand := range matched {
			if cand.AppUserID == "" && userAppIDs[cand.AppID] {
				continue
			}
			kept = append(kept, cand)
		}
		matched = kept
	}
	if len(matched) > 1 {
		response.Unauthorized(c, 40107, "无法确定集成身份（多个身份使用了相同的 webhook 密钥）")
		return
	}
	if len(matched) == 0 {
		response.Unauthorized(c, 40107, "Webhook 签名无效或缺失")
		return
	}
	appUserID := matched[0].AppUserID

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
