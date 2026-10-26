package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"go.uber.org/zap"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

// refSource 候选来源。准入按来源分策略：显式声明的图（Markdown 图片 /
// HTML <img>）只做结构准入；裸链与普通链接目的地（[label](u)、<a href>）
// 没有图片语法背书，还要求「GitHub 附件形态 ∪ 位图扩展名」。
type refSource int

const (
	refSourceMarkdownImage refSource = iota
	refSourceHTMLImage
	refSourceBareLink
	refSourceLink // 普通链接目的地（ast.Link 与 <a href>）
)

// prBodyMarkdown PR 描述的 Markdown 解析器（GFM 含 Linkify）。收集走 AST：
// 「URL 到哪结束」是 GFM 规范已定义的问题——行内代码/围栏代码块不产链接
// 节点、括号配对与 ASCII 尾标点剥离有规范行为、引用式与尖括号目的地都解析
// 成同一个 Destination——逐字符正则重建它每轮审查都会漏一种包法。
var prBodyMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Linkify),
)

// trimTrailingPunct 裸链候选的尾标点兜底裁剪。Linkify 已按 GFM 剥过一轮
// ASCII 尾标点并做了括号配对，但中文句读、直角引号等非 GFM 字符不归它管
// ——位图扩展名带尾标点判 false 漏收（HasScreenshots=false → 视觉轮静默
// 不开），GitHub 附件形态带尾标点发请求 404 误写「截图无法下载」。裁剪只
// 作用于尾部：这些字符可能合法出现在 URL 中段/查询串里，不能按类别整段
// 排除。显式声明的图（![a](u) / <img src>）一律不裁——URL 写在括号/引号
// 里边界明确，裁尾反而会吃掉合法字符（如 ?token=abc! 结尾的 !）。
func trimTrailingPunct(u string) string {
	u = strings.TrimRightFunc(u, func(r rune) bool {
		switch {
		case strings.ContainsRune(`?!.,:*_'~`, r):
			return true // GFM 尾标点集
		case unicode.Is(unicode.Pe, r), unicode.Is(unicode.Pf, r):
			return true // 右闭合符号（）》』】等）与右引号（’”等）
		case strings.ContainsRune("。，、；：！？…", r):
			return true // 中文句读（Unicode 类别归不进 Pe/Pf 的部分）
		}
		return false
	})
	// 括号配对兜底：`)` 多于 `(` 时剥掉尾部多余的 `)`（GFM 语义，Linkify
	// 已做过一轮，这里补它漏掉的边角）。
	for strings.HasSuffix(u, ")") && strings.Count(u, ")") > strings.Count(u, "(") {
		u = strings.TrimSuffix(u, ")")
	}
	return u
}

// normalizeRefURL 收集层的引用 URL 规整：先剥 #fragment（不是资源本身——
// 下载面 normalize 会拒 fragment，IsGitHubMediaURL 也因此判 false，私有附
// 件带 fragment 时 bearer 重试失效）；裸链再裁掉尾部粘住的句读/引号。收集
// 结果与后续指纹都用规整后的 URL。
func normalizeRefURL(u string, bare bool) string {
	if head, _, ok := strings.Cut(u, "#"); ok {
		u = head
	}
	if bare {
		u = trimTrailingPunct(u)
	}
	return u
}

// admitScreenshotURL 结构准入，三个来源统一过一遍：严格镜像下载面
// normalizeAllowedURL 的拒绝集——scheme 必须 https（截图面只走 https，
// http、data:、相对路径发不出去）、host 非空、不能带 userinfo（下载面拒
// User != nil，漏了会把 admin:pw@… 送去吃一个必拒）。不按扩展名拒绝：显式
// 声明的图（![]()/<img>）交给探测的 Content-Type 裁决——render.php?id=1
// 这类无扩展名直链可能是真图，扩展名误判会漏收（视觉轮静默不开）；「全部
// 不是图」由探测三态收敛成 visualNone，不会每个同步事件刷「截图无法下载」。
func admitScreenshotURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// htmlMediaSources 从一段 HTML literal 里取所有 <img src> 与 <a href>。
// 用 golang.org/x/net/html
// 解析而不是正则：实体（&amp;）由解析器解码（旧正则会把 &amp; 原样收进
// URL），注释 <!-- --> 与代码里的元素不产节点，不会误收。`<a href>` 与
// `<img src>` 同段一次解析取出：链接目的地交给「链接」准入口径（GitHub
// 附件形态 ∪ 位图扩展名）。
func htmlMediaSources(fragment string) (imgs, links []string) {
	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), root)
	if err != nil {
		return nil, nil
	}
	var walkHTML func(*html.Node)
	walkHTML = func(n *html.Node) {
		if n.Type == html.ElementNode {
			var attr string
			switch {
			case strings.EqualFold(n.Data, "img"):
				attr = "src"
			case strings.EqualFold(n.Data, "a"):
				attr = "href"
			}
			if attr != "" {
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, attr) {
						if v := strings.TrimSpace(a.Val); v != "" {
							if attr == "src" {
								imgs = append(imgs, v)
							} else {
								links = append(links, v)
							}
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkHTML(c)
		}
	}
	for _, n := range nodes {
		walkHTML(n)
	}
	return imgs, links
}

// imageLabelText 图片 alt / 链接 label 文本：递归收集所有后代 Text（
// `![**登录页**](u)` 这类带行内格式的文本套在 Emphasis 等节点下，只看直接
// 子节点会丢成空串）。
func imageLabelText(n ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(source))
			}
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// collectScreenshotURLsAt 按出现顺序提取 PR 描述里的图片引用；同 URL 只取首次，
// 标签取 alt 文本，缺省回退「截图 N」。三个来源（walk 顺序即文档序）：
// ![alt](url)（含引用式与尖括号目的地）、HTML <img src>（行内与整行 HTML
// 块）、裸链（Linkify 自动链接）。围栏代码/行内代码/HTML 注释不产链接节点，
// 自然不收集。githubWebHost 是部署的 GitHub Web 地址（host[:port]，见
// githubmedia.GitHubWebHost）：裸链/普通链接里的「用户上传的附件直链
// （通常无文件名扩展名）」按「公共 GitHub 形态 ∪ 本部署实例形态」判定，
// GHE 实例的 `https://<host>/user-attachments/assets/…` 同样收。
func collectScreenshotURLsAt(body, githubWebHost string) []screenshotRef {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	type candidate struct {
		ref    screenshotRef
		source refSource
	}
	var candidates []candidate
	source := []byte(body)
	doc := prBodyMarkdown.Parser().Parse(text.NewReader(source))
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			// 目的地由 goldmark 填充：行内、尖括号（![a](<u(1).png>)）、引用
			// 式（![a][id] + 定义行）解析成同一个 Destination；没有定义的引
			// 用不产 Image 节点，自然不收集。显式声明的 URL 不裁尾。
			candidates = append(candidates, candidate{
				ref: screenshotRef{
					URL:   normalizeRefURL(string(n.Destination), false),
					Label: imageLabelText(n, source),
				},
				source: refSourceMarkdownImage,
			})
		case *ast.AutoLink:
			// Linkify/尖括号自动链接 → 裸链候选：URL 边界由 GFM 定义（ASCII
			// 尾标点与括号配对已剥），中文句读等非 GFM 字符再兜底裁一次。
			if n.AutoLinkType != ast.AutoLinkURL {
				return ast.WalkContinue, nil
			}
			u := string(n.URL(source))
			// www. 前缀裸链的 scheme 是 Linkify 按 GFM 补的 http://——作者没
			// 写协议，GitHub 渲染同款可点链接；下载面只收 https，按 https 准
			// 入。显式写 http:// 的裸链原样保留（准入照常拒收）。
			if strings.HasPrefix(string(n.Text(source)), "www.") {
				u = "https://" + strings.TrimPrefix(u, "http://")
			}
			candidates = append(candidates, candidate{
				ref:    screenshotRef{URL: normalizeRefURL(u, true)},
				source: refSourceBareLink,
			})
		case *ast.Link:
			// 普通链接的目的地也可能是 GitHub 附件/位图直链（旧 bareAttachRe
			// 的覆盖面）：边界由 goldmark 精确给出不裁尾，准入走「链接」口径。
			candidates = append(candidates, candidate{
				ref: screenshotRef{
					URL:   normalizeRefURL(string(n.Destination), false),
					Label: imageLabelText(n, source),
				},
				source: refSourceLink,
			})
		case *ast.RawHTML:
			imgs, links := htmlMediaSources(string(n.Segments.Value(source)))
			for _, u := range imgs {
				candidates = append(candidates, candidate{
					ref:    screenshotRef{URL: normalizeRefURL(u, false)},
					source: refSourceHTMLImage,
				})
			}
			for _, u := range links {
				candidates = append(candidates, candidate{
					ref:    screenshotRef{URL: normalizeRefURL(u, false)},
					source: refSourceLink,
				})
			}
		case *ast.HTMLBlock:
			// 整块拼接后再解析：`<img\n src=...>` 跨行时逐行解析会把每个
			// fragment 都看成不完整的元素而漏收。
			var buf strings.Builder
			for i := 0; i < n.Lines().Len(); i++ {
				line := n.Lines().At(i)
				buf.Write(line.Value(source))
			}
			imgs, links := htmlMediaSources(buf.String())
			for _, u := range imgs {
				candidates = append(candidates, candidate{
					ref:    screenshotRef{URL: normalizeRefURL(u, false)},
					source: refSourceHTMLImage,
				})
			}
			for _, u := range links {
				candidates = append(candidates, candidate{
					ref:    screenshotRef{URL: normalizeRefURL(u, false)},
					source: refSourceLink,
				})
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var refs []screenshotRef
	for _, c := range candidates {
		// 结构准入在去重前对三来源统一过一遍：下载面必然拒绝的形态不进收
		// 集结果。同 URL 带/不带 fragment 规整后也按同一张去重。
		if c.ref.URL == "" || !admitScreenshotURL(c.ref.URL) {
			continue
		}
		// 裸链与普通链接目的地都没有图片语法背书，在结构准入之外额外要求
		// 「部署 GitHub 实例的附件形态（附件直链无扩展名）∪ 位图扩展名」：无
		// 扩展名的任意 https 链接都收会把描述里每个网页链接当成截图（按扩
		// 展名结尾判定，接受这个启发式的误收）。
		if (c.source == refSourceBareLink || c.source == refSourceLink) &&
			!githubmedia.IsGitHubAttachmentURLFor(c.ref.URL, githubWebHost) && !githubmedia.HasBitmapExtension(c.ref.URL) {
			continue
		}
		if _, dup := seen[c.ref.URL]; dup {
			continue
		}
		seen[c.ref.URL] = struct{}{}
		if c.ref.Label == "" {
			c.ref.Label = fmt.Sprintf("截图 %d", len(refs)+1)
		}
		refs = append(refs, c.ref)
	}
	return refs
}

// logURLRe 日志脱敏的 URL 匹配：http(s) 序列，到空白/引号/尖括号为止。
var logURLRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// scrubLogURLs 日志脱敏：把文本里的 http(s) URL 剥掉 query/fragment/userinfo，
// 只留 scheme://host/path。失败日志里的 URL 可能带预签名 query——重定向后
// 的 *url.Error 内嵌的是最终请求的完整地址，原样落日志会把凭证写进排查记录。
// 解析不了的序列原样保留。
func scrubLogURLs(s string) string {
	return logURLRe.ReplaceAllStringFunc(s, func(m string) string {
		parsed, err := url.Parse(m)
		if err != nil || parsed == nil {
			return m
		}
		return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}).String()
	})
}

// errReviewMediaUnconfigured 部署没有配置截图下载面（s.media == nil）。与
// 「图床连不上」分开成两个原因：默认文案「无法连接图片服务器」会让用户去
// 查网络，而真正的原因是本部署不支持截图审查。
var errReviewMediaUnconfigured = errors.New("截图下载面未配置")

// errForgejoMediaPrivate Forgejo 附件匿名下载被拒：PAT 不随 Bearer 出域
// （媒体主机 ≠ API 作用域，无令牌面），目前不支持私有附件带凭证下载——
// 给用户可见的「私有附件不支持」而不是笼统的「无法连接图片服务器」。
var errForgejoMediaPrivate = errors.New("Forgejo 私有附件暂不支持带凭证下载，请改用公开图片链接")

// downloadScreenshot 单张截图下载：匿名优先；匿名失败且失败是「可能靠令牌
// 解决」的（GitHub 媒体域的 401/403/404——私有仓库附件对匿名请求正是回
// 404/403，GitHub 不区分不存在与无权限）才带安装令牌重试一次。协议
// 不支持、内容不是图片、超过大小上限、内网地址这类确定性失败换令牌也一样
// 失败，不白铸令牌（每次同步都会重来，探测路径还占预算）。令牌是仓库凭证，
// 外部图床一律不发（防止 PR 描述里的外链图床靠回 403 换走安装令牌）。media
// 未配置视为该部署不支持截图审查。
func (s *ReviewService) downloadScreenshot(ctx context.Context, integrationID string, installationID int64, url string) (*githubmedia.CachedMedia, error) {
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
	// 带凭证重试只面向 GitHub 媒体：forge 集成显式短路（今天恰好靠
	// installationID=0 挡住；未来迁移工具若回填非零 installation，PAT 也不
	// 得随 Bearer 出域到 github 主机）。Forgejo 附件匿名 401/403 打独立
	// sentinel（公开仓匿名也能拉，401/403 多半是私有附件）；404 不归类
	// ——公开仓附件被删同样回 404，报「请改用公开图片链接」是误导。
	if reviewIntegrationID(integrationID) != model.ForgeIntegrationBuiltinGitHubID {
		if upstream.StatusCode != http.StatusNotFound {
			return nil, fmt.Errorf("%w（upstream %d）: %w", errForgejoMediaPrivate, upstream.StatusCode, err)
		}
		return nil, err
	}
	if !githubmedia.IsGitHubMediaURLFor(url, s.githubWebHost()) || s.mediaBearer == nil || installationID == 0 {
		return nil, err
	}
	bearer, berr := s.mediaBearer(ctx, integrationID, installationID)
	if berr != nil || bearer == "" {
		return nil, err
	}
	// scope 用安装 id：令牌拉进缓存的私有附件条目只对同一安装命中（见
	// cacheEntry.Scope）。
	return s.media.FetchAndCacheWithBearer(ctx, url, bearer, fmt.Sprintf("install:%d", installationID))
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
		errForgejoMediaPrivate,
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

// githubWebHost 本部署的 GitHub Web 地址（GHE 部署在 github_app.api_base_url
// 里配置实例地址，公共 GitHub 留空）——host[:port]，默认端口去掉。截图收集的
// 附件形态判定与「匿名失败后能否带 App 安装令牌重试」都以它为准：写死
// github.com 会让 GHE 部署的附件直链被准入拒掉（视觉审查静默不启动），私有
// 附件也永远拿不到令牌。
func (s *ReviewService) githubWebHost() string {
	if s.cfg == nil {
		return githubmedia.GitHubWebHost("")
	}
	return githubmedia.GitHubWebHost(s.cfg.GitHubApp.APIBaseURL)
}

// ensureAssets 保证 (集成,repo,pr,head) 的截图资产已落账：逐引用查重 → 未下
// 载的走 downloadScreenshot；失败行（storage_path 为空）每次重试下载，成功则
// 更新原行。私有仓库附件匿名拉不到：GitHub 媒体域（含部署实例附件路径）
// 失败且有安装 id 时带 App 令牌重试一次。下载失败也落一行（error 非空、
// storage_path 空），图片端点据此回 404。返回本次指纹与资产列表。
func (s *ReviewService) ensureAssets(ctx context.Context, integrationID, repo string, prNumber int, headSHA, prBody string, installationID int64) (string, []model.ReviewAsset, error) {
	refs := collectScreenshotURLsAt(prBody, s.githubWebHost())
	fingerprint := screenshotFingerprint(refs)
	now := time.Now().UTC()

	var assets []model.ReviewAsset
	if err := s.db.Where("integration_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
		integrationID, repo, prNumber, headSHA).Order("position ASC").Find(&assets).Error; err != nil {
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
		if cached, err := s.downloadScreenshot(ctx, integrationID, installationID, ref.URL); err != nil {
			// 原始错误（含网络层英文文案）只进日志，落库的是中文可读原因；
			// URL 与错误文本先脱敏——*url.Error 内嵌重定向后的完整地址，预签
			// 名 query 不落日志。
			s.logger.Warn("截图下载失败", zap.String("url", scrubLogURLs(ref.URL)),
				zap.String("error", scrubLogURLs(err.Error())))
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
			ID:            uuid.NewString(),
			IntegrationID: integrationID,
			RepoFullName:  repo,
			PRNumber:      prNumber,
			HeadSHA:       headSHA,
			Position:      pos,
			Label:         ref.Label,
			SourceURL:     ref.URL,
			StoragePath:   storagePath,
			MimeType:      mime,
			SizeBytes:     size,
			Error:         errText,
			CreatedAt:     now,
		}
		// 并发/重入安全：同 (集成,repo,pr,head,source_url) 只保留先到的行
		// （唯一索引 idx_review_assets_source 是冲突目标）。
		res := s.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "integration_id"}, {Name: "repo_full_name"},
				{Name: "pr_number"}, {Name: "head_sha"}, {Name: "source_url"},
			},
			DoNothing: true,
		}).Create(asset)
		if res.Error != nil {
			return "", nil, errs.ErrInternal
		}
		assets = append(assets, *asset)
	}
	// 重查一次拿全量（含并发他方先建的行）。
	if err := s.db.Where("integration_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
		integrationID, repo, prNumber, headSHA).Order("position ASC").Find(&assets).Error; err != nil {
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

// screenshotProbeResult 探测三态：ready 至少一张可下载；none 全部「不是图」
// （内容/地址形态被下载面拒）；unavailable 拉不到（网络故障、超时、预算耗
// 尽）。成败两态表达不了「全部不是图」——那该收敛到 visualNone，而不是每次
// 同步都刷一条「截图无法下载」failure。
type screenshotProbeResult int

const (
	probeReady screenshotProbeResult = iota
	probeNone
	probeUnavailable
)

// isNotAnImageError 失败是否属于「不是图」——上游证明候选不是可服务的位图：
// ErrUnsupportedUpstreamMedia（Content-Type 不是位图、octet-stream 且非位图
// 扩展名）、ErrInvalidMediaURL（地址不是可请求的媒体形态）。内网/保留地址、
// 超限、空响应虽是本次请求必败，但指向的确实可能是图（内网图床、换小文件、
// 补 body 都可能自愈），归「拉不到」如实报 failure，不静默归「没有截图」。
// 其余一切（网络、超时、缓存、预算）同样是「拉不到」。
func isNotAnImageError(err error) bool {
	return errors.Is(err, githubmedia.ErrUnsupportedUpstreamMedia) ||
		errors.Is(err, githubmedia.ErrInvalidMediaURL)
}

// hashFileSHA256 流式读文件算 SHA-256 hex。探测用它对下载成功的缓存文件取
// 内容指纹——下载面单文件上限 256 MiB，整文件读进内存再哈希是白付的峰值
// 内存，边读边算只占一个哈希缓冲。
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// screenshotProbe probeScreenshots 的结果。contentFP 内容指纹只在 result=
// probeReady 且全部引用都下载成功时非空——只成功一部分时无法证明「内容没
// 变」，按未知（调用方保守视为变化）处理。
type screenshotProbe struct {
	result    screenshotProbeResult
	reason    string
	contentFP string
}

// probeScreenshots 试下载 PR 描述里的全部截图引用（不再「第一张成功即停」
// ——每个成功图都要参与内容指纹，指纹是「URL 换了但图没变」判定的依据）。
// 成功图已落盘缓存（视觉运行时直接命中）。逐 ref 分类失败：「不是图」与
// 「拉不到」（见 isNotAnImageError）。任一成功 → probeReady（部分失败时内容
// 指纹为空）；全部探完且全部「不是图」→ probeNone；零成功且存在「拉不到」
// 或总预算耗尽未探完 → probeUnavailable，reason 取第一条「拉不到」类原因，
// 没有再取第一条失败原因。探测不写 review_assets——资产行仍由视觉运行时的
// ensureAssets 落账，这里不按资产行计数，避免「已不在描述里的陈旧资产」被
// 当成可用截图。
func (s *ReviewService) probeScreenshots(ctx context.Context, integrationID string, installationID int64, prBody string) screenshotProbe {
	refs := collectScreenshotURLsAt(prBody, s.githubWebHost())
	if len(refs) == 0 {
		return screenshotProbe{result: probeNone}
	}
	ctx, cancel := context.WithTimeout(ctx, screenshotProbeTimeout)
	defer cancel()
	firstReason := ""
	firstFetchFailReason := ""
	fetchFailed := false
	completed := true
	var contentHashes []string
	for _, ref := range refs {
		// 单张限时：挂死图只吃掉自己的时限，还有剩余预算就继续试下一张；
		// 总预算吃满才放弃（剩下的引用继续探测只会继续超时，同步循环还在等）。
		attemptCtx, cancel := context.WithTimeout(ctx, screenshotProbeAttemptTimeout)
		cached, err := s.downloadScreenshot(attemptCtx, integrationID, installationID, ref.URL)
		cancel()
		if err == nil {
			// 每张开始哈希前检查总预算：前面的图把预算吃光时这张不再读盘
			// （内容按未知处理），也不能给「全部不是图」的结论留口子——按
			// 预算耗尽收敛，下轮同步事件再试。
			if ctx.Err() != nil {
				completed = false
				if firstReason == "" {
					firstReason = screenshotErrorText(ctx.Err())
				}
				break
			}
			// 下载成功只读一次盘：哈希在下载当场算，不再在末尾按路径二次
			// 读取。读失败（磁盘异常）按该张「拉不到」处理，不当成可用图——
			// 视觉运行时同样会栽在读文件上。
			sum, hashErr := hashFileSHA256(cached.Path)
			if hashErr != nil {
				err = fmt.Errorf("%w: %v", githubmedia.ErrLocalCache, hashErr)
			} else {
				contentHashes = append(contentHashes, sum)
				continue
			}
		}
		text := screenshotErrorText(err)
		if firstReason == "" {
			firstReason = text
		}
		if !isNotAnImageError(err) {
			fetchFailed = true
			if firstFetchFailReason == "" {
				firstFetchFailReason = text
			}
		}
		// 逐张失败记日志：status 只带第一条原因，其余各张的失败（含网络层
		// 原始英文文案）只有这里能留下排查线索；URL 与错误文本先脱敏。
		s.logger.Warn("截图探测失败", zap.String("url", scrubLogURLs(ref.URL)),
			zap.String("reason", text), zap.String("error", scrubLogURLs(err.Error())))
		if ctx.Err() != nil {
			// 总预算耗尽没探完：不能下「全部不是图」的结论（后面的引用可能
			// 还有图），按拉不到收敛，下轮同步事件再试。
			completed = false
			break
		}
	}
	// 任一成功即 probeReady（优先于「拉不到」——决策要的是「能否开视觉轮」，
	// 部分失败交给运行时的 unmeasurable 与指纹「内容未知」口径），但只有全
	// 部成功才带内容指纹。
	if len(contentHashes) > 0 {
		probe := screenshotProbe{result: probeReady}
		if len(contentHashes) == len(refs) {
			probe.contentFP = screenshotContentFingerprintFromHashes(contentHashes)
		}
		return probe
	}
	if fetchFailed {
		return screenshotProbe{result: probeUnavailable, reason: firstFetchFailReason}
	}
	if !completed {
		return screenshotProbe{result: probeUnavailable, reason: firstReason}
	}
	return screenshotProbe{result: probeNone}
}

// AgentScreenshots GET /api/review-agent/runs/:runId/screenshots：仅视觉运行可用。
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
	client, err := s.reviewClient(ctx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	if err != nil {
		return nil, false, err
	}
	pr, err := client.GetPullRequest(ctx, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, false, errs.New(errs.ErrGitHubAPI.Code, "读取 PR 失败: "+err.Error())
	}
	fingerprint, assets, err := s.ensureAssets(ctx, run.IntegrationID, run.RepoFullName, run.PRNumber, run.HeadSHA, pr.Body, run.InstallationID)
	if err != nil {
		return nil, false, err
	}

	// changed：本轮指纹与上一轮视觉运行（更早 round）绑定 head 的资产指纹
	// 比较。上一轮运行只在本 (项目, 集成) 账本内找——资产本身按 (集成, 仓库)
	// 归一共享，「何时做过视觉审查」属于执行态，按项目分区。
	changed := true
	var prevRuns []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND kind = ? AND round_number < ?",
		run.ProjectID, run.RepoFullName, run.PRNumber, run.IntegrationID, model.ReviewKindVisual, run.RoundNumber).
		Order("round_number DESC").Limit(1).Find(&prevRuns).Error; err != nil {
		return nil, false, errs.ErrInternal
	}
	if len(prevRuns) > 0 {
		var prevAssets []model.ReviewAsset
		if err := s.db.Where("integration_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			run.IntegrationID, run.RepoFullName, run.PRNumber, prevRuns[0].HeadSHA).
			Order("position ASC").Find(&prevAssets).Error; err != nil {
			return nil, false, errs.ErrInternal
		}
		prevRefs := make([]screenshotRef, 0, len(prevAssets))
		for _, a := range prevAssets {
			prevRefs = append(prevRefs, screenshotRef{URL: a.SourceURL})
		}
		changed = screenshotFingerprint(prevRefs) != fingerprint
	}

	// 陈旧行过滤：规整/准入口径演进后，同一 head 可能残留旧口径的资产行
	// （URL 规整形态不同、或按旧准入本不该收的）。只把 SourceURL 仍在当前
	// 描述收集结果里的行交给审查员，残留行不再展示。
	currentURLs := map[string]struct{}{}
	for _, ref := range collectScreenshotURLsAt(pr.Body, s.githubWebHost()) {
		currentURLs[ref.URL] = struct{}{}
	}
	items := make([]api.ReviewAgentScreenshot, 0, len(assets))
	for _, a := range assets {
		if _, current := currentURLs[a.SourceURL]; !current {
			continue
		}
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
		// 图片地址带本轮 run 作用域（INT-169）：端点按 runId+令牌双重绑定，
		// URL 只能取本运行 id，不能从其他运行借。
		item.Url = "/api/review-agent/runs/" + runID + "/screenshots/" + a.ID
		items = append(items, item)
	}
	return items, changed, nil
}

// AgentScreenshotImage 取图片本体：(路径, MIME)。仅视觉运行；校验资产与运行
// 同 (集成,repo,pr,head)；下载失败或路径缺失 → 40406。
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
		Where("id = ? AND integration_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			assetID, run.IntegrationID, run.RepoFullName, run.PRNumber, run.HeadSHA).
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
