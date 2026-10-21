package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm/clause"
)

// 视觉截图资产（INT-135）：从 PR 描述提取图片 URL → githubmedia 代理
// 下载+磁盘缓存 → review_assets 落账。资产按 (repo,pr,head,position) 归属，
// 图片端点只对同运行绑定身份放行。

// screenshotRef 一处截图引用：源 URL + 展示标签。
type screenshotRef struct {
	URL   string
	Label string
}

var (
	// Markdown 图片 ![alt](url)（允许 title）。
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?([^\s>)]+)>?[^)]*\)`)
	// HTML <img src="...">。
	htmlImgRe = regexp.MustCompile(`<img[^>]+src=["']([^"']+)["'][^>]*>`)
	// 裸的 GitHub 附件 URL（用户直接粘贴，没有图片语法）。
	bareAttachRe = regexp.MustCompile(`https://(?:github\.com/user-attachments/assets|user-images\.githubusercontent\.com)/[^\s)<>"'\]]+`)
)

// collectScreenshotURLs 按出现顺序提取 PR 描述里的图片引用；同 URL 只取首次，
// 标签取 alt 文本，缺省回退文件名。
func collectScreenshotURLs(body string) []screenshotRef {
	type hit struct {
		pos int
		ref screenshotRef
	}
	var hits []hit
	for _, m := range mdImageRe.FindAllStringSubmatchIndex(body, -1) {
		url := strings.TrimSpace(body[m[4]:m[5]])
		label := strings.TrimSpace(body[m[2]:m[3]])
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url, Label: label}})
	}
	for _, m := range htmlImgRe.FindAllStringSubmatchIndex(body, -1) {
		url := strings.TrimSpace(body[m[2]:m[3]])
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url}})
	}
	for _, m := range bareAttachRe.FindAllStringIndex(body, -1) {
		url := body[m[0]:m[1]]
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url}})
	}
	// 两个来源按原文位置合并排序（HTML 与 Markdown 混用时顺序不乱）。
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j-1].pos > hits[j].pos; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
	seen := map[string]struct{}{}
	var refs []screenshotRef
	for _, h := range hits {
		if h.ref.URL == "" {
			continue
		}
		if _, dup := seen[h.ref.URL]; dup {
			continue
		}
		seen[h.ref.URL] = struct{}{}
		if h.ref.Label == "" {
			h.ref.Label = fmt.Sprintf("截图 %d", len(refs)+1)
		}
		refs = append(refs, h.ref)
	}
	return refs
}

// errReviewMediaUnconfigured 部署没有配置截图下载面（s.media == nil）。与
// 「图床连不上」分开成两个原因：默认文案「无法连接图片服务器」会让用户去
// 查网络，而真正的原因是本部署不支持截图审查。
var errReviewMediaUnconfigured = errors.New("截图下载面未配置")

// downloadScreenshot 单张截图下载：匿名优先；匿名失败且目标是 GitHub 媒体域
// 时才带 App 安装令牌重试——令牌是仓库凭证，外部图床一律不发（防止 PR 描述
// 里的外链图床靠回 403 换走安装令牌）。media 未配置视为该部署不支持截图审查。
func (s *ReviewService) downloadScreenshot(ctx context.Context, installationID int64, url string) (*githubmedia.CachedMedia, error) {
	if s.media == nil {
		return nil, errReviewMediaUnconfigured
	}
	cached, err := s.media.FetchAndCache(ctx, url)
	if err == nil {
		return cached, nil
	}
	if !githubmedia.IsGitHubMediaURL(url) || s.mediaBearer == nil || installationID == 0 {
		return nil, err
	}
	bearer, berr := s.mediaBearer(ctx, installationID)
	if berr != nil || bearer == "" {
		return nil, err
	}
	return s.media.FetchAndCacheWithBearer(ctx, url, bearer)
}

// screenshotErrorText 用户可见的下载失败原因。网络层原始错误（*url.Error 的
// dial tcp…、connection refused 等）是英文堆栈式文案，不进 review_assets.error
// 与 status 描述；原始错误由调用方记日志。
func screenshotErrorText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "下载超时"
	}
	// 同步被新快照推进、进程关停、以及探测 30s 时限之外的上层取消都以
	// context.Canceled 到达，单独给文案——落到兜底「无法连接图片服务器」
	// 会把一次正常取消和真实网络故障混在一起。
	if errors.Is(err, context.Canceled) {
		return "下载被取消"
	}
	// sentinel 命中后必须返回 sentinel 自己的文案：生产里 Dialer.Control、
	// CheckRedirect 抛出的 sentinel 会被 *net.OpError → *url.Error 层层包住，
	// 外层 err.Error() 是「Get https://…: dial tcp …: 中文文案」——英文网络
	// 原文连同目标 URL 会一起透出到 status 与落库字段。
	for _, sentinel := range []error{
		errReviewMediaUnconfigured,
		githubmedia.ErrInvalidMediaURL,
		githubmedia.ErrUnsupportedUpstreamMedia,
		githubmedia.ErrBlockedUpstreamAddress,
		githubmedia.ErrEmptyUpstreamMedia,
		githubmedia.ErrMediaTooLarge,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	var upstream *githubmedia.UpstreamStatusError
	if errors.As(err, &upstream) {
		return upstream.Error()
	}
	// 45s 客户端超时以 net.Error（Timeout）形态到达（*httpError 没有 Unwrap，
	// errors.Is 只认 context.DeadlineExceeded），这里补一层。
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "下载超时"
	}
	return "无法连接图片服务器"
}

// ensureAssets 保证 (repo,pr,head) 的截图资产已落账：逐引用查重 → 未下载的
// 走 downloadScreenshot；失败行（storage_path 为空）每次重试下载，成功则
// 更新原行。私有仓库附件匿名拉不到：GitHub 媒体域失败且有安装 id 时带 App
// 令牌重试一次。下载失败也落一行（error 非空、storage_path 空），图片端点
// 据此回 404。返回本次指纹与资产列表。
func (s *ReviewService) ensureAssets(ctx context.Context, repo string, prNumber int, headSHA, prBody string, installationID int64) (string, []model.ReviewAsset, error) {
	refs := collectScreenshotURLs(prBody)
	fingerprint := screenshotFingerprint(refs)
	now := time.Now().UTC()

	var assets []model.ReviewAsset
	if err := s.db.Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?",
		repo, prNumber, headSHA).Order("position ASC").Find(&assets).Error; err != nil {
		return "", nil, errs.ErrInternal
	}
	have := map[string]model.ReviewAsset{}
	for _, a := range assets {
		have[a.SourceURL] = a
	}
	for pos, ref := range refs {
		existing, seenBefore := have[ref.URL]
		if seenBefore && existing.StoragePath != "" {
			continue
		}
		var storagePath, mime, errText string
		var size int64
		if cached, err := s.downloadScreenshot(ctx, installationID, ref.URL); err != nil {
			// 原始错误（含网络层英文文案）只进日志，落库的是中文可读原因。
			s.logger.Warn("截图下载失败", zap.String("url", ref.URL), zap.Error(err))
			errText = screenshotErrorText(err)
		} else {
			storagePath = cached.Path
			mime = cached.Metadata.ContentType
			size = cached.Metadata.Size
		}
		if seenBefore {
			// 失败行重试：更新原行（成功填路径，失败刷新原因）。
			if err := s.db.Model(&model.ReviewAsset{}).Where("id = ?", existing.ID).
				Updates(map[string]any{
					"storage_path": storagePath, "mime_type": mime,
					"size_bytes": size, "error": errText,
				}).Error; err != nil {
				return "", nil, errs.ErrInternal
			}
			continue
		}
		asset := &model.ReviewAsset{
			ID:           uuid.NewString(),
			RepoFullName: repo,
			PRNumber:     prNumber,
			HeadSHA:      headSHA,
			Position:     pos,
			Label:        ref.Label,
			SourceURL:    ref.URL,
			StoragePath:  storagePath,
			MimeType:     mime,
			SizeBytes:    size,
			Error:        errText,
			CreatedAt:    now,
		}
		// 并发/重入安全：同 (repo,pr,head,source_url) 只保留先到的行
		// （唯一索引 idx_review_assets_source 是冲突目标）。
		res := s.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "repo_full_name"}, {Name: "pr_number"},
				{Name: "head_sha"}, {Name: "source_url"},
			},
			DoNothing: true,
		}).Create(asset)
		if res.Error != nil {
			return "", nil, errs.ErrInternal
		}
		assets = append(assets, *asset)
	}
	// 重查一次拿全量（含并发他方先建的行）。
	if err := s.db.Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?",
		repo, prNumber, headSHA).Order("position ASC").Find(&assets).Error; err != nil {
		return "", nil, errs.ErrInternal
	}
	return fingerprint, assets, nil
}

// screenshotProbeTimeout 截图探测的总时限。探测跑在共享同步循环里，图床挂死
// 时不能把同一批其它 PR 的同步一起拖满——单请求客户端超时 45s 比这更长，
// 所以探测必须有更紧的总时限。
const screenshotProbeTimeout = 30 * time.Second

// probeScreenshots 试下载 PR 描述里的截图，第一张成功即停：成功图已落盘缓存
// （视觉运行时直接命中），全部失败返回第一条失败原因（可能为空串，由调用方
// 兜底文案）。探测不写 review_assets——资产行仍由视觉运行时的 ensureAssets
// 落账，这里不按资产行计数，避免「已不在描述里的陈旧资产」被当成可用截图。
// 单张下载失败只累计原因不外抛：downloadScreenshot 的错误已被
// screenshotErrorText 归纳成中文文案，对调用方没有额外的错误信息。
func (s *ReviewService) probeScreenshots(ctx context.Context, installationID int64, prBody string) (bool, string) {
	refs := collectScreenshotURLs(prBody)
	if len(refs) == 0 {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(ctx, screenshotProbeTimeout)
	defer cancel()
	reason := ""
	for _, ref := range refs {
		_, err := s.downloadScreenshot(ctx, installationID, ref.URL)
		if err == nil {
			return true, ""
		}
		if reason == "" {
			reason = screenshotErrorText(err)
		}
		// 时限吃满就放弃：剩下的引用继续探测只会继续超时，同步循环还在等。
		if ctx.Err() != nil {
			break
		}
	}
	return false, reason
}

// AgentScreenshots GET /api/review-agent/screenshots：仅视觉运行可用。
// items 的 url 是本端点图片路径（相对 public_url）；changed 表示截图集合
// 相对上一轮视觉审查是否有变化。
func (s *ReviewService) AgentScreenshots(ctx context.Context, runID string) ([]api.ReviewAgentScreenshot, bool, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if run.Kind != model.ReviewKindVisual {
		return nil, false, errs.ErrCredentialNotAllowed
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return nil, false, err
	}
	pr, err := client.GetPullRequest(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, false, errs.New(errs.ErrGitHubAPI.Code, "读取 PR 失败: "+err.Error())
	}
	fingerprint, assets, err := s.ensureAssets(ctx, run.RepoFullName, run.PRNumber, run.HeadSHA, pr.Body, run.InstallationID)
	if err != nil {
		return nil, false, err
	}

	// changed：本轮指纹与上一轮视觉运行（更早 round）绑定 head 的资产指纹
	// 比较。上一轮运行只在本项目账本内找——资产本身是仓库归一的共享产物，
	// 「何时做过视觉审查」属于执行态，按项目分区。
	changed := true
	var prevRuns []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND kind = ? AND round_number < ?",
		run.ProjectID, run.RepoFullName, run.PRNumber, model.ReviewKindVisual, run.RoundNumber).
		Order("round_number DESC").Limit(1).Find(&prevRuns).Error; err != nil {
		return nil, false, errs.ErrInternal
	}
	if len(prevRuns) > 0 {
		var prevAssets []model.ReviewAsset
		if err := s.db.Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			run.RepoFullName, run.PRNumber, prevRuns[0].HeadSHA).
			Order("position ASC").Find(&prevAssets).Error; err != nil {
			return nil, false, errs.ErrInternal
		}
		prevRefs := make([]screenshotRef, 0, len(prevAssets))
		for _, a := range prevAssets {
			prevRefs = append(prevRefs, screenshotRef{URL: a.SourceURL})
		}
		changed = screenshotFingerprint(prevRefs) != fingerprint
	}

	items := make([]api.ReviewAgentScreenshot, 0, len(assets))
	for _, a := range assets {
		item := api.ReviewAgentScreenshot{Id: a.ID, Label: a.Label}
		if a.StoragePath == "" {
			// 下载失败：给出原因但不给 URL（图片端点只会 404），审查员据此
			// 记入 unmeasurable，而不是以为漏了一张图。
			reason := a.Error
			if reason == "" {
				reason = "截图下载失败"
			}
			item.Error = api.Ptr(reason)
			items = append(items, item)
			continue
		}
		item.Url = "/api/review-agent/screenshots/" + a.ID
		items = append(items, item)
	}
	return items, changed, nil
}

// AgentScreenshotImage 取图片本体：(路径, MIME)。仅视觉运行；校验资产与运行
// 同 repo/pr/head；下载失败或路径缺失 → 40406。
func (s *ReviewService) AgentScreenshotImage(ctx context.Context, runID, assetID string) (string, string, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return "", "", err
	}
	if run.Kind != model.ReviewKindVisual {
		return "", "", errs.ErrCredentialNotAllowed
	}
	var asset model.ReviewAsset
	if err := s.db.WithContext(ctx).
		Where("id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			assetID, run.RepoFullName, run.PRNumber, run.HeadSHA).
		First(&asset).Error; err != nil {
		return "", "", errs.ErrIssueAssetNotFound
	}
	if asset.StoragePath == "" {
		return "", "", errs.ErrIssueAssetNotFound
	}
	mime := asset.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	return asset.StoragePath, mime, nil
}
