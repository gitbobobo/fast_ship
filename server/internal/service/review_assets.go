package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
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
	// 裸链接（用户直接粘贴、没有图片语法）：任意 https URL 粗筛。排除 # 与
	// 紧贴链接的中文/全角句读（。，、）——fragment 不是资源本身，句读会破
	// 坏扩展名判定；# 在粗筛排除，三个来源的引用再由 normalizeRefURL 统一剥
	// 一次兜底（md/HTML 的捕获正则不排除 #）。其余句尾标点不能放进排除类
	// ——? : ! 等可能合法出现在 URL 中段/查询串里——由 normalizeRefURL 在
	// 匹配之后规整。裸链没有图片语法背书，收集时额外按「GitHub 附件形态 ∪
	// 位图扩展名」准入（见 collectScreenshotURLs 内注），三来源共用的结构
	// 准入见 admitScreenshotURL。
	bareURLRe = regexp.MustCompile(`https://[^\s)<>"'\]#。，、）]+`)
)

// trimTrailingPunct 剥掉引用匹配尾部粘住的标点：半角句尾标点（. , ; : ! ?）
// 与全角句读（；：！？…）、右侧引号、反引号与中文直角引号（代码/引用包
// 法）、Markdown 强调符（*）会跟着正则一起被吃进匹配——位图扩展名带尾标点
// 判 false 漏收（HasScreenshots=false → 视觉轮静默不开），GitHub 附件形态带
// 尾标点发请求 404 误写「截图无法下载」。这些字符可能合法出现在 URL 中段，
// 不能进 bareURLRe 的排除类，只裁尾部不动中段；带查询串的正常链接结尾不是
// 标点，不受影响。
func trimTrailingPunct(u string) string {
	const cutset = ".,;:!?。，、）；：！？…”’\"'`」』*"
	return strings.TrimRight(u, cutset)
}

// normalizeRefURL 收集层的引用 URL 规整，三个来源统一：先剥 #fragment（不是
// 资源本身——下载面 normalize 会拒 fragment，IsGitHubMediaURL 也因此判
// false，私有附件带 fragment 时 bearer 重试失效），再裁掉匹配尾部粘住的标
// 点。收集结果与后续指纹都用规整后的 URL。
func normalizeRefURL(u string) string {
	if head, _, ok := strings.Cut(u, "#"); ok {
		u = head
	}
	return trimTrailingPunct(u)
}

// admitScreenshotURL 结构准入，三个来源统一过一遍（只排除「下载面必然拒
// 绝」的形态）：scheme 必须 https 且 host 非空（http、data:、相对路径发不
// 出请求，下载面 normalize 同样要求）；路径带扩展名时必须是位图扩展名（集
// 合与下载面 imagesOnly 同源，svg/视频/网页不收），无扩展名保留——可能是
// image/* 直链，GitHub 附件直链本就没有扩展名。![]()/<img> 此前无差别收集，
// 描述里只有 svg 徽章 / http 图 / data: URI / 网页链接时探测必然全败，每次
// 同步都写一次「截图无法下载」failure，永远收敛不到「没有截图」。
func admitScreenshotURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	if path.Ext(parsed.Path) == "" {
		return true
	}
	return githubmedia.HasBitmapExtension(u)
}

// collectScreenshotURLs 按出现顺序提取 PR 描述里的图片引用；同 URL 只取首次，
// 标签取 alt 文本，缺省回退文件名。
func collectScreenshotURLs(body string) []screenshotRef {
	type hit struct {
		pos int
		ref screenshotRef
	}
	var hits []hit
	for _, m := range mdImageRe.FindAllStringSubmatchIndex(body, -1) {
		url := normalizeRefURL(strings.TrimSpace(body[m[4]:m[5]]))
		label := strings.TrimSpace(body[m[2]:m[3]])
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url, Label: label}})
	}
	for _, m := range htmlImgRe.FindAllStringSubmatchIndex(body, -1) {
		url := normalizeRefURL(strings.TrimSpace(body[m[2]:m[3]]))
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url}})
	}
	for _, m := range bareURLRe.FindAllStringIndex(body, -1) {
		url := normalizeRefURL(body[m[0]:m[1]])
		// 裸链没有图片语法背书，在结构准入之外额外要求「GitHub 附件形态
		// （附件直链无扩展名）∪ 位图扩展名」：无扩展名的任意 https 链接都
		// 收会把描述里每个网页链接当成截图（按扩展名结尾判定，接受这个启
		// 发式的误收）。
		if !githubmedia.IsGitHubAttachmentURL(url) && !githubmedia.HasBitmapExtension(url) {
			continue
		}
		hits = append(hits, hit{pos: m[0], ref: screenshotRef{URL: url}})
	}
	// 三个来源按原文位置合并排序（HTML 与 Markdown 混用时顺序不乱）。
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
		// 结构准入在去重前对三来源统一过一遍：下载面必然拒绝的形态不进收
		// 集结果——否则探测必然全败，每次同步都写「截图无法下载」failure；
		// 准入后 refs 为空 → HasScreenshots=false → visualNone。同 URL 带/
		// 不带 fragment 规整后也按同一张去重。
		if !admitScreenshotURL(h.ref.URL) {
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

// downloadScreenshot 单张截图下载：匿名优先；匿名失败且失败是「可能靠令牌
// 解决」的（GitHub 媒体域的 401/403/404——私有仓库附件对匿名请求正是回
// 404/403，GitHub 不区分不存在与无权限）才带 App 安装令牌重试一次。协议
// 不支持、内容不是图片、超过大小上限、内网地址这类确定性失败换令牌也一样
// 失败，不白铸令牌（每次同步都会重来，探测路径还占预算）。令牌是仓库凭证，
// 外部图床一律不发（防止 PR 描述里的外链图床靠回 403 换走安装令牌）。media
// 未配置视为该部署不支持截图审查。
func (s *ReviewService) downloadScreenshot(ctx context.Context, installationID int64, url string) (*githubmedia.CachedMedia, error) {
	if s.media == nil {
		return nil, errReviewMediaUnconfigured
	}
	cached, err := s.media.FetchAndCache(ctx, url)
	if err == nil {
		return cached, nil
	}
	var upstream *githubmedia.UpstreamStatusError
	if !errors.As(err, &upstream) {
		return nil, err
	}
	switch upstream.StatusCode {
	case 401, 403, 404:
	default:
		return nil, err
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
	// 原文连同目标 URL 会一起透出到 status 与落库字段。ErrLocalCache 的分类
	// 文案与 sentinel 文案不同，单独一层；它由 githubmedia 在本地缓存操作
	// 失败的产生点打标，不再按 *fs.PathError / *os.SyscallError 外壳嗅探
	// ——网络错误的 syscall 形态（dial connect: connection refused 也是
	// SyscallError）不会再被误归成磁盘故障。
	if errors.Is(err, githubmedia.ErrLocalCache) {
		return "截图缓存写入失败"
	}
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
	// 本地磁盘/缓存写入失败（磁盘满、权限、缓存目录损坏）不是网络故障，落到
	// 兜底「无法连接图片服务器」会把用户引向查网络——由上面的 ErrLocalCache
	// sentinel 覆盖。
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
// 所以探测必须有更紧的总时限。var 是为了让测试缩短等待。
var screenshotProbeTimeout = 30 * time.Second

// screenshotProbeAttemptTimeout 单张截图的探测时限，必须显著小于总预算：
// 否则描述里靠前的地址黑洞（握手挂起、连不上）会用一张挂死图吃满总预算，
// 后面的可用截图一次都轮不到，决策落入「截图无法下载」，且每次同步按同样
// 顺序重复同一张挂死图。var 是为了让测试缩短等待。
var screenshotProbeAttemptTimeout = 10 * time.Second

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
		// 单张限时：挂死图只吃掉自己的时限，还有剩余预算就继续试下一张；
		// 总预算吃满才放弃（剩下的引用继续探测只会继续超时，同步循环还在等）。
		attemptCtx, cancel := context.WithTimeout(ctx, screenshotProbeAttemptTimeout)
		_, err := s.downloadScreenshot(attemptCtx, installationID, ref.URL)
		cancel()
		if err == nil {
			return true, ""
		}
		text := screenshotErrorText(err)
		if reason == "" {
			reason = text
		}
		// 逐张失败记日志：status 只带第一条原因，其余各张的失败（含网络层
		// 原始英文文案）只有这里能留下排查线索。
		s.logger.Warn("截图探测失败", zap.String("url", ref.URL),
			zap.String("reason", text), zap.Error(err))
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
