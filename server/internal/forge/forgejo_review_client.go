package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ForgejoReviewClient forge.ReviewClient 的 Forgejo/Gitea 适配器（INT-167 任务
// 3）：持有 SSRF 安全 http client（service 侧按集成配置构建）+ API 基址 + 机器人
// PAT，方法面把 Forgejo v1 REST 的响应转成 forge 中性类型。错误统一包装成
// *StatusError，service 的 IsForbidden/IsNotFound 按同一口径判定。
// ForgejoLoginCache PAT 所属账号的共享缓存：resolver 每次现取现建适配器，
// 仓库级 PAT 下 GET /user 恒 403，写响应学到的 login 随实例丢弃会让自评
// 标记永久失效（自己的行内评论回流下一轮历史）。缓存放 resolver 侧按
// (integrationID, PAT) 分槽随进程生命周期——命中即不再探测 /user，PAT
// 轮换到不同账号时自动换槽，旧身份不滞留。
type ForgejoLoginCache struct {
	mu    sync.Mutex
	login string
}

type ForgejoReviewClient struct {
	h       *http.Client
	apiBase string
	token   string

	// loginCache PAT 所属账号缓存（FindPullRequestComment 认自己的评论、
	// Self 标记用）。默认实例私有；resolver 注入按 integrationID 分槽的
	// 共享槽让学到的身份跨实例存活。只缓存成功结果——探测失败不记档。
	loginCache *ForgejoLoginCache
}

// 编译期钉住适配器满足审查访问面契约。
var _ ReviewClient = (*ForgejoReviewClient)(nil)

// NewForgejoReviewClient 组装 Forgejo 审查客户端。apiBaseURL 是展开后的 API
// 基址（{base_url}/api/v1 或集成显式钉死的端点）；token 为空串时按匿名请求发
// 出（公开仓库仍可读，写操作由平台 401/403 报错）。
func NewForgejoReviewClient(httpClient *http.Client, apiBaseURL, token string) *ForgejoReviewClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ForgejoReviewClient{
		h:          httpClient,
		apiBase:    strings.TrimRight(apiBaseURL, "/"),
		token:      token,
		loginCache: &ForgejoLoginCache{},
	}
}

// withLoginCache 换用共享 login 缓存（resolver 按 integrationID 分槽注入，
// 跨现取现建的实例保留学到的身份）。
func (c *ForgejoReviewClient) withLoginCache(cache *ForgejoLoginCache) *ForgejoReviewClient {
	if cache != nil {
		c.loginCache = cache
	}
	return c
}

// Kind 报告集成 kind。
func (c *ForgejoReviewClient) Kind() string { return KindForgejo }

// Bearer 媒体下载令牌：机器人 PAT。私有仓库附件下载是否带上它由调用方决定
// （review_assets 只对 GitHub 实例的附件路径带凭证）。
func (c *ForgejoReviewClient) Bearer(context.Context) (string, error) { return c.token, nil }

// statusErr 非 2xx 响应统一包装：body 截断防大 payload 撑爆日志，结构与
// GitHub 适配器透传的 githubapi.StatusError 同构。限流判定在截断前对完整
// body 做（限流字样落在被丢弃中段时也不丢判定），Body 文本只做展示与日志。
func (c *ForgejoReviewClient) statusErr(method, path string, resp *http.Response, body []byte) *StatusError {
	snippet := string(body)
	rateLimited := bodySuggestsRateLimit(snippet)
	if len(snippet) > 512 {
		// 保留头尾两段便于日志定位；限流判定不依赖这段截断面（已由
		// RateLimited 在完整 body 上钉死），中段被丢弃不影响判定。
		snippet = snippet[:384] + "…" + snippet[len(snippet)-128:]
	}
	return &StatusError{
		StatusCode:  resp.StatusCode,
		Method:      method,
		URL:         c.url(path),
		Body:        snippet,
		RateLimited: rateLimited,
	}
}

// url 拼绝对请求地址：path 以 / 开头，owner/repo/数值段由调用方先经
// pathSegment 转义。
func (c *ForgejoReviewClient) url(path string) string { return c.apiBase + path }

// pathSegment 单段路径转义（owner/repo/tag 不应含转义字符，转义是兜底）。
func pathSegment(s string) string { return url.PathEscape(s) }

// do 单次 JSON 请求：PAT 经 Authorization: token 头（Forgejo PAT 惯例）；
// 2xx 把响应体按需解进 out；非 2xx 返回 *StatusError。
func (c *ForgejoReviewClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, respBodyMaxBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusErr(method, path, resp, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("forge: 解析 %s %s 响应失败: %w", method, path, err)
		}
	}
	return nil
}

// respBodyMaxBytes 响应读取上限：diff/评论最大几十 MB 量级，兜底防异常端点。
const respBodyMaxBytes = 64 << 20

// forgejoPagingLimit / forgejoMaxPages 列表分页页大小与页数上限：50 × 20 页
// = 1000 条，与 GitHub 适配器的 per_page=100 × 10 页同量级。
const (
	forgejoPagingLimit = 50
	forgejoMaxPages    = 20
)

// repoPath "owner/name" 拆段并转义；非法名直接报错（与 githubapi.splitRepo
// 同口径）。
func repoPath(repoFullName string) (string, error) {
	owner, name, ok := strings.Cut(repoFullName, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("forge: 仓库名须为 owner/name，得到 %q", repoFullName)
	}
	return pathSegment(owner) + "/" + pathSegment(name), nil
}

func (c *ForgejoReviewClient) GetPullRequest(ctx context.Context, repoFullName string, number int) (*PullRequest, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return nil, err
	}
	var wire forgejoPR
	if err := c.do(ctx, http.MethodGet,
		"/repos/"+repo+"/pulls/"+strconv.Itoa(number), nil, &wire); err != nil {
		return nil, err
	}
	return wire.toPullRequest(), nil
}

func (c *ForgejoReviewClient) GetPullRequestDiff(ctx context.Context, repoFullName string, number int) (string, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return "", err
	}
	// .diff 端点按 text/plain 整段返回，不走 JSON 解码。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.url("/repos/"+repo+"/pulls/"+strconv.Itoa(number)+".diff"), nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.h.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// 多读一字节探测截断：到顶刚好读满不代表截断（diff 恰为上限大小），
	// 只有还有剩余字节才说明 diff 超上限——截断的 diff 不能按完整件算锚点。
	data, err := io.ReadAll(io.LimitReader(resp.Body, respBodyMaxBytes+1))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", c.statusErr(http.MethodGet, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+".diff", resp, data)
	}
	if len(data) > respBodyMaxBytes {
		// 确定性失败走 ErrDiffTooLarge 哨兵：service 的 IsDiffTooLarge 把它
		// 与平台 too_large 归一，进降级收尾而不是无限退避重试。已读前缀
		// 原样返回——前缀仍是真实内容，调用方按不完整件处置。
		return string(data[:respBodyMaxBytes]),
			fmt.Errorf("%w: forge: PR #%d 的 diff 超过 %d 字节上限", ErrDiffTooLarge, number, respBodyMaxBytes)
	}
	return string(data), nil
}

// forgejoReviewExpandMax 单次 ListReviewComments 展开评论的已提交 review
// 数上限：逐 review 再分页没有总约束时，review 数百上千的 PR 会把一次调用
// 放大到上万请求，拖垮同步预算与实例配额。超上限保留最新段——最新 review
// 恰是用户最新行内讨论与机器人最近一轮自评所在，截断它会让历史组装丢
// 最关键的发言。
const forgejoReviewExpandMax = 200

// ListReviewComments PR 级行内评论：Forgejo 没有 PR 级平铺端点，经 review
// 列表逐个取 review comments 后展平。InReplyToID 恒 nil（Forgejo 的 review
// comment 不带回复关系字段，审查链路不用它）。state=PENDING 的未提交草稿
// 对其他用户不可见，其评论不算已发帖历史——跳过既挡污染也省请求。
func (c *ForgejoReviewClient) ListReviewComments(ctx context.Context, repoFullName string, number int) ([]ReviewComment, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return nil, err
	}
	var reviews []forgejoReview
	err = c.pagedPages(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews",
		func(raw []byte) (int, error) {
			var wire []forgejoReview
			if err := json.Unmarshal(raw, &wire); err != nil {
				return 0, err
			}
			reviews = append(reviews, wire...)
			return len(wire), nil
		})
	if err != nil {
		return nil, err
	}
	// 自评标记用 PAT 所属 login；探测确定性不可用时退化为全 false（不过滤）。
	selfLogin, err := c.selfLogin(ctx)
	if err != nil {
		return nil, err
	}
	var out []ReviewComment
	// 先滤出已提交 review 再取尾部最新：列表按 id 升序返回，从头数满上限
	// 会恰好把最新 review（含用户最新行内讨论、机器人最近一轮自评）截掉。
	submitted := make([]forgejoReview, 0, len(reviews))
	for _, r := range reviews {
		if strings.EqualFold(r.State, "PENDING") {
			continue
		}
		submitted = append(submitted, r)
	}
	sort.Slice(submitted, func(i, j int) bool { return submitted[i].ID < submitted[j].ID })
	if len(submitted) > forgejoReviewExpandMax {
		slog.WarnContext(ctx, "Forgejo 行内评论展开 review 数到顶截断，只保留最新段",
			slog.String("repo", repoFullName), slog.Int("pr", number),
			slog.Int("total_reviews", len(submitted)),
			slog.Int("max_reviews", forgejoReviewExpandMax))
		submitted = submitted[len(submitted)-forgejoReviewExpandMax:]
	}
	for _, r := range submitted {
		// 单条 review 的 comments 也按 ListOptions 分页——裸 do() 只回默认页
		// 大小，评论多时会静默截断。
		var wire []forgejoComment
		err := c.pagedPages(ctx,
			"/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews/"+strconv.FormatInt(r.ID, 10)+"/comments",
			func(raw []byte) (int, error) {
				var items []forgejoComment
				if err := json.Unmarshal(raw, &items); err != nil {
					return 0, err
				}
				wire = append(wire, items...)
				return len(items), nil
			})
		if err != nil {
			return nil, err
		}
		for _, w := range wire {
			out = append(out, ReviewComment{
				ID: w.ID, User: w.User.Login, Body: w.Body,
				InReplyToID: nil, Path: w.Path, CreatedAt: w.CreatedAt,
				Self: selfLogin != "" && w.User.Login == selfLogin,
			})
		}
	}
	return out, nil
}

func (c *ForgejoReviewClient) ListIssueCommentsForPR(ctx context.Context, repoFullName string, number int) ([]IssueComment, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return nil, err
	}
	var wire []forgejoComment
	err = c.pagedPages(ctx, "/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments",
		func(raw []byte) (int, error) {
			var items []forgejoComment
			if err := json.Unmarshal(raw, &items); err != nil {
				return 0, err
			}
			wire = append(wire, items...)
			return len(items), nil
		})
	if err != nil {
		return nil, err
	}
	selfLogin, err := c.selfLogin(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IssueComment, 0, len(wire))
	for _, w := range wire {
		out = append(out, IssueComment{
			ID: w.ID, User: w.User.Login, Body: w.Body, CreatedAt: w.CreatedAt,
			Self: selfLogin != "" && w.User.Login == selfLogin,
		})
	}
	return out, nil
}

// botLogin PAT 所属账号（GET /user）：FindPullRequestComment 只认机器人自己
// 发的评论。只缓存成功；探测失败返回错误且不记档，下次调用重试。仓库范围
// PAT 拿不到 read:user（Forgejo 的 user/* scope 组不能授给 specific-repo
// token）——此时由写接口响应的 user.login 学到登录名（learnLogin），首次写
// 之前仍不可知。
func (c *ForgejoReviewClient) botLogin(ctx context.Context) (string, error) {
	c.loginCache.mu.Lock()
	defer c.loginCache.mu.Unlock()
	if c.loginCache.login != "" {
		return c.loginCache.login, nil
	}
	var wire forgejoUser
	if err := c.do(ctx, http.MethodGet, "/user", nil, &wire); err != nil {
		return "", err
	}
	c.loginCache.login = wire.Login
	return c.loginCache.login, nil
}

// selfLogin 自评判定用的 PAT 账号探测：只有非限流 403（仓库级 PAT 拿不到
// read:user 的确定性信号）才退化空 login；瞬时网络错误、5xx、401/404
// 照旧传播挂起本轮——瞬时故障若也降级 marker-only，「作者==自己」过滤就
// 形同虚设，且 PAT 有仓库管理权限时 PATCH 覆写他人评论连 403 兜底都没有。
// /user 404 不降级：仓库级 PAT 的缺 scope 信号是 403 不是 404——404 更像
// api_base_url 指错路径前缀，静默降级会让 Self 过滤失效且无任何报错线索。
// 403 降级前先判 IsRateLimit：GitHub/前置代理的限流正是回 403（body 含
// rate limit/abuse detection 字样），与权限 403 无法从状态码区分——限流
// 窗口静默降级会整窗关掉自评过滤，限流 403 与权限 403 同按「可重试的
// 非确定性」处理照旧透出挂起。
func (c *ForgejoReviewClient) selfLogin(ctx context.Context) (string, error) {
	login, err := c.botLogin(ctx)
	if err == nil {
		return login, nil
	}
	if IsForbidden(err) && !IsRateLimit(err) {
		return "", nil
	}
	return "", err
}

// learnLogin 从写接口响应的 user.login 学 PAT 所属账号（仓库级 PAT 下
// GET /user 不可用时的唯一来源）。只在「响应 user 必然是自己」的 POST
// 路径调用（create/review 的作者就是 PAT 所属账号）——PATCH 响应用的
// user 是评论原作者，marker-only 退化命中伪造评论时会把攻击者 login 学
// 进缓存。与 botLogin 共用同一份缓存。
func (c *ForgejoReviewClient) learnLogin(login string) {
	if login == "" {
		return
	}
	c.loginCache.mu.Lock()
	c.loginCache.login = login
	c.loginCache.mu.Unlock()
}

// FindPullRequestComment 按 marker 找机器人顶层评论：GitHub 侧认
// user.type=Bot，Forgejo 的机器人是普通账号——改为「作者 == PAT 所属账号 +
// 正文以 marker 开头」。login 不可知（仓库级 PAT + 尚无写响应可学）时退化
// 到 marker-only 认领：用户伪造 marker 会被覆写一次，但按原样传播错误会让
// 文档推荐的 token 契约永远发不出评论（两害相权取其轻，详见
// docs/internal/forge-integrations.md 的 PAT 契约段）。
func (c *ForgejoReviewClient) FindPullRequestComment(ctx context.Context, repoFullName string, number int, marker string) (int64, bool, error) {
	login, err := c.selfLogin(ctx)
	if err != nil {
		return 0, false, err
	}
	repo, err := repoPath(repoFullName)
	if err != nil {
		return 0, false, err
	}
	var comments []forgejoComment
	err = c.pagedPages(ctx, "/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments",
		func(raw []byte) (int, error) {
			var items []forgejoComment
			if err := json.Unmarshal(raw, &items); err != nil {
				return 0, err
			}
			comments = append(comments, items...)
			return len(items), nil
		})
	if err != nil {
		return 0, false, err
	}
	for _, w := range comments {
		if (login == "" || w.User.Login == login) &&
			strings.HasPrefix(strings.TrimSpace(w.Body), marker) {
			return w.ID, true, nil
		}
	}
	return 0, false, nil
}

func (c *ForgejoReviewClient) CreatePullRequestComment(ctx context.Context, repoFullName string, number int, body string) (int64, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return 0, err
	}
	var wire struct {
		ID   int64       `json:"id"`
		User forgejoUser `json:"user"`
	}
	if err := c.do(ctx, http.MethodPost,
		"/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments",
		map[string]string{"body": body}, &wire); err != nil {
		return 0, err
	}
	c.learnLogin(wire.User.Login)
	return wire.ID, nil
}

func (c *ForgejoReviewClient) UpdatePullRequestComment(ctx context.Context, repoFullName string, commentID int64, body string) error {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return err
	}
	// PATCH 响应不学 login：评论对象的 user 是原作者不是编辑者——marker-only
	// 退化命中他人伪造评论且 PAT 有权编辑他人评论时，会把攻击者 login 学进
	// 共享缓存（中毒是持久的：Self 把攻击者当自己、真机器人评论回流历史）。
	// 学习只走「响应 user 必然等于 PAT 所属账号」的 POST 路径（create/review）。
	if err := c.do(ctx, http.MethodPatch,
		"/repos/"+repo+"/issues/comments/"+strconv.FormatInt(commentID, 10),
		map[string]string{"body": body}, nil); err != nil {
		return err
	}
	return nil
}

func (c *ForgejoReviewClient) ListReviews(ctx context.Context, repoFullName string, number int) ([]ReviewSummary, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return nil, err
	}
	var wire []forgejoReview
	err = c.pagedPages(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews",
		func(raw []byte) (int, error) {
			var items []forgejoReview
			if err := json.Unmarshal(raw, &items); err != nil {
				return 0, err
			}
			wire = append(wire, items...)
			return len(items), nil
		})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewSummary, 0, len(wire))
	for _, w := range wire {
		// PENDING 是未提交草稿（Web UI/他端中断残留），对读者不可见——
		// 「已提交」契约内不可认，否则认领会把 pending 运行的行内
		// findings 误判已投递而静默漏投。
		if strings.EqualFold(w.State, "PENDING") {
			continue
		}
		summary := ReviewSummary{ID: w.ID, CommitID: w.CommitID, Body: w.Body, State: w.State}
		if w.SubmittedAt != nil {
			summary.CreatedAt = *w.SubmittedAt
		}
		out = append(out, summary)
	}
	return out, nil
}

// SubmitReview 提交 review（审查链路只用 event=COMMENT + 行内评论）。commit_id
// 把 review 钉在运行绑定的 head 上（Forgejo 的 CreatePullReviewOptions 支持，
// 与 GitHub 语义一致）；行内评论按 Side 映射行号：LEFT→old_position，
// RIGHT/缺省→new_position。
func (c *ForgejoReviewClient) SubmitReview(ctx context.Context, repoFullName string, number int, in ReviewInput) (int64, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return 0, err
	}
	payload := forgejoSubmitReview{Event: forgejoReviewEvent(in.Event), Body: in.Body, CommitID: in.CommitID}
	if len(in.Comments) > 0 {
		payload.Comments = make([]forgejoReviewLine, 0, len(in.Comments))
		for _, w := range in.Comments {
			line := forgejoReviewLine{Path: w.Path, Body: w.Body}
			if w.Side == "LEFT" {
				line.OldPosition = int64(w.Line)
			} else {
				line.NewPosition = int64(w.Line)
			}
			payload.Comments = append(payload.Comments, line)
		}
	}
	var wire struct {
		ID   int64       `json:"id"`
		User forgejoUser `json:"user"`
	}
	if err := c.do(ctx, http.MethodPost,
		"/repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews", payload, &wire); err != nil {
		return 0, err
	}
	c.learnLogin(wire.User.Login)
	return wire.ID, nil
}

// forgejoStatusDescriptionMax Forgejo/Gitea 的 CreateStatusOption description
// 上限（binding MaxSize(255)），超出被整单 422——适配器内按 rune 截断。
const forgejoStatusDescriptionMax = 255

// forgejoReviewEvent GitHub 事件名 → Forgejo/Gitea 事件名：Gitea 用过去式
// APPROVED（GitHub 是 APPROVE），COMMENT/REQUEST_CHANGES 同名透传。审查链路
// 当前只发 COMMENT，映射是防未来启用 APPROVE 类事件时被平台拒单。
func forgejoReviewEvent(event string) string {
	if event == "APPROVE" {
		return "APPROVED"
	}
	return event
}

func (c *ForgejoReviewClient) CreateCommitStatus(ctx context.Context, repoFullName, sha, state, statusContext, description, targetURL string) error {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return err
	}
	if runes := []rune(description); len(runes) > forgejoStatusDescriptionMax {
		description = string(runes[:forgejoStatusDescriptionMax])
	}
	payload := forgejoCommitStatus{State: state, Description: description, Context: statusContext, TargetURL: targetURL}
	return c.do(ctx, http.MethodPost,
		"/repos/"+repo+"/statuses/"+pathSegment(sha), payload, nil)
}

func (c *ForgejoReviewClient) GetRepository(ctx context.Context, repoFullName string) error {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodGet, "/repos/"+repo, nil, nil)
}

// PathExistsAtRef 用 contents 端点判存在（Forgejo 无 GitHub 的
// git/trees/{ref}:{dir} 取子树语法）：2xx → 存在（文件/目录同算），404 →
// 不存在。非规范路径（前导 /、空段、.、..）本地判不存在不发请求——与
// githubapi 实现同口径，防路径别名绕过文件归属校验。
func (c *ForgejoReviewClient) PathExistsAtRef(ctx context.Context, repoFullName, path, ref string) (bool, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return false, err
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return false, nil
		}
		segs[i] = pathSegment(s)
	}
	qref := url.QueryEscape(ref)
	err = c.do(ctx, http.MethodGet,
		"/repos/"+repo+"/contents/"+strings.Join(segs, "/")+"?ref="+qref,
		nil, nil)
	if err == nil {
		return true, nil
	}
	if !IsNotFound(err) {
		return false, err
	}
	// contents 404 恒有歧义（路径不存在 vs ref/仓库不可达）：探根目录区分，
	// ref 级失败按调用失败上报，不能误报成「文件不存在」诱导 Agent 重写
	// 合法 findings。
	rootErr := c.do(ctx, http.MethodGet, "/repos/"+repo+"/contents/?ref="+qref, nil, nil)
	if rootErr != nil {
		return false, rootErr
	}
	return false, nil
}

// ListOpenPullRequests open PR 枚举（轮询补漏专用，不在 ReviewClient 方法面）。
// fork head 与已删 fork（head.repo=null）的 PR 在这里过滤——fork 的改动不属于
// 本仓库镜像的审查对象，轮询只补同仓库分支的推送漏网。分页到短页/空页止。
func (c *ForgejoReviewClient) ListOpenPullRequests(ctx context.Context, repoFullName string) ([]PullRequest, error) {
	repo, err := repoPath(repoFullName)
	if err != nil {
		return nil, err
	}
	var raws []forgejoPR
	err = c.pagedPages(ctx, "/repos/"+repo+"/pulls?state=open",
		func(raw []byte) (int, error) {
			var items []forgejoPR
			if err := json.Unmarshal(raw, &items); err != nil {
				return 0, err
			}
			raws = append(raws, items...)
			return len(items), nil
		})
	if err != nil {
		return nil, err
	}
	out := make([]PullRequest, 0, len(raws))
	for _, w := range raws {
		// fork 判定看 head/base 的仓库关系而非 head.repo.fork 标志：仓库本身
		// 是 fork 时该标志恒 true，会把同仓分支的 PR 误杀；head.repo 缺失
		// （已删 fork、异常 payload）或 head 仓库 ≠ base 仓库时保守跳过。
		head := w.Head.Repo
		if head == nil || !strings.EqualFold(head.FullName, repoFullName) {
			continue
		}
		if w.Base.Repo != nil && !strings.EqualFold(head.FullName, w.Base.Repo.FullName) {
			continue
		}
		out = append(out, *w.toPullRequest())
	}
	return out, nil
}

// pagedPages 分页拉取：limit/page 查询参数（Forgejo 惯例），到空页止，页数
// 封顶。consume 把单页原始 JSON 解码进调用方的切片并返回条目数（Go 方法不
// 能带类型参数，解码收进各调用点）。停止条件只能看空页：Forgejo 按部署
// MaxResponseItems 收缩实际页大小（配 20 时 limit=50 的请求也只回 20 条），
// 「不满一页即末页」会把后续页静默漏掉。
func (c *ForgejoReviewClient) pagedPages(ctx context.Context, basePath string, consume func([]byte) (int, error)) error {
	sep := "?"
	if strings.Contains(basePath, "?") {
		sep = "&"
	}
	for page := 1; page <= forgejoMaxPages; page++ {
		path := fmt.Sprintf("%s%slimit=%d&page=%d", basePath, sep, forgejoPagingLimit, page)
		var raw json.RawMessage
		if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return err
		}
		n, err := consume(raw)
		if err != nil {
			return fmt.Errorf("forge: 解析 %s 第 %d 页失败: %w", basePath, page, err)
		}
		if n == 0 {
			return nil
		}
	}
	// 页数到顶（limit×maxPages 条）还没见空页 = 静默截断：降级不挂起但留痕——
	// 超大 PR 的 marker 评论可能在截断面外，FindPullRequestComment 会再发一条。
	slog.WarnContext(ctx, "Forgejo 分页到达上限，结果可能被截断",
		slog.String("path", basePath), slog.Int("max_pages", forgejoMaxPages))
	return nil
}
