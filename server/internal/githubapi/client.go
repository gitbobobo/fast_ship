package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthProvider 提供一次 GitHub 调用所需的 bearer（空串 = 匿名请求）。实现：
// githubapp.InstallationTokenStore（App 安装令牌，401 时作废缓存）与
// pkg/github 的静态 token（Invalidate 为 no-op）。
type AuthProvider interface {
	Bearer(ctx context.Context, installationID int64) (string, error)
	// Invalidate 401 后作废缓存的凭证；静态 token 为 no-op。
	Invalidate(installationID int64)
}

// GitHubClient PR 评审/同步/评论链路对 GitHub 的全部访问面。两个实现共享
// NewClient 的 REST 代码：githubapp.Client（安装令牌）与 pkg/github.PRClient
// （用户 Token/匿名，用于 issue 关联 PR 的读取）。
type GitHubClient interface {
	// 元数据
	GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*PullRequest, error)
	GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error)
	ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewComment, error)
	// ListIssueCommentsForPR PR 会话评论（review 行内之外的顶层讨论，INT-135
	// 历史上下文用；不与 pkg/github 的同名方法同形，命名特意带 ForPR）。
	ListIssueCommentsForPR(ctx context.Context, installationID int64, repoFullName string, number int) ([]IssueComment, error)
	// 机器人顶层评论（每 PR 一条）
	FindPullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (commentID int64, found bool, err error)
	CreatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (commentID int64, err error)
	UpdatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error
	// review
	// ListReviews 列出 PR 上已提交的 review（对账用；每页 100，最多 10 页）。
	ListReviews(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewSummary, error)
	// SubmitReview 返回创建的 review id（事件 COMMENT 时才有行内评论落点）。
	SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) (reviewID int64, err error)
	// commit status（INT-135）：POST /repos/{o}/{r}/statuses/{sha}；
	// state ∈ error/failure/pending/success，context 是固定检查名（代码审查/视觉审查）。
	CreateCommitStatus(ctx context.Context, installationID int64, repoFullName, sha, state, statusContext, description, targetURL string) error
	// 仓库级检查（INT-135）：GET /repos/{owner}/{repo}，nil 表示可访问；
	// 403/404 经 *StatusError 返回。installationID=0 表示调用方未知安装 id，
	// 由 App 侧实现先解析仓库所属安装（静态 token 实现忽略该参数）。
	GetRepository(ctx context.Context, installationID int64, repoFullName string) error
	// ResolveInstallation 按仓库名解析 App 安装 id（GET /repos/{o}/{r}/installation
	// 的 id 字段）：rerun 等没有 webhook installation 快照的路径用。
	// 静态 token / 匿名实现不支持解析，返回错误（调用方不得把 0 当安装 id 继续）。
	ResolveInstallation(ctx context.Context, repoFullName string) (int64, error)
	// PathExistsAtRef 仓库相对路径在指定 ref（commit SHA 等）的树中是否存在
	// （GET /repos/{o}/{r}/git/trees/{ref}:{dir} 取父目录条目精确匹配末段；
	// INT-169 审查结果文件归属校验）。true=存在（blob/tree/commit 条目均可），
	// 不存在→false；tree 响应截断且未命中时报错（无法判定，不猜不存在）。
	PathExistsAtRef(ctx context.Context, installationID int64, repoFullName, path, ref string) (bool, error)
	// ListPullRequestFiles PR 变更文件清单（GET pulls/N/files 分页）：rename
	// 两侧（filename+previous_filename）都收，去重。complete=false 表示清单
	// 被平台封顶截断（GitHub 3000 文件上限）——缺席不能当不存在证据。
	ListPullRequestFiles(ctx context.Context, installationID int64, repoFullName string, number int) (paths []string, complete bool, err error)
}

// githubHTTP GitHub 调用的共享客户端。Timeout 是 ctx 缺失时的兜底；调用方
// 传入的 ctx 仍通过 NewRequestWithContext 取消。
var githubHTTP = &http.Client{Timeout: 30 * time.Second}

// restClient GitHubClient 的唯一 REST 实现：JSON 进出、bearer 注入、非 2xx 转
// *StatusError。不引第三方 SDK——这些端点直接写清请求形状。每次调用现场向
// AuthProvider 取 bearer，401 时作废缓存重试一次（令牌可能刚被吊销或轮换）。
type restClient struct {
	auth AuthProvider
	h    *httpClient
}

// NewClient 用给定凭证提供者与 API base 构造共享 REST 实现。
func NewClient(auth AuthProvider, baseURL string) GitHubClient {
	return &restClient{auth: auth, h: &httpClient{baseURL: baseURL}}
}

// withToken 按「调用 → 401 → 作废缓存重试一次」的口径执行。
func (c *restClient) withToken(ctx context.Context, installationID int64, fn func(bearer string) error) error {
	bearer, err := c.auth.Bearer(ctx, installationID)
	if err != nil {
		return err
	}
	err = fn(bearer)
	if err == nil || !isStatus(err, http.StatusUnauthorized) {
		return err
	}
	c.auth.Invalidate(installationID)
	bearer, err = c.auth.Bearer(ctx, installationID)
	if err != nil {
		return err
	}
	return fn(bearer)
}

// httpClient GitHub REST 的极薄封装。
type httpClient struct {
	baseURL string
}

// bodyReadMaxBytes 一般端点响应体读取上限；diff 端点走 doLimit 单独放宽。
const bodyReadMaxBytes = 8 << 20

// do 发任意请求，返回状态码与响应体。ctx 取消时中止等待。
func (h *httpClient) do(ctx context.Context, method, url, accept, bearer string, in any) (int, []byte, error) {
	return h.doLimit(ctx, method, url, accept, bearer, in, bodyReadMaxBytes)
}

// doLimit 同 do，按 max 读响应体：2xx 超限返回「截断前缀 + ErrBodyTooLarge」
// （截断件不能当完整件用，但前缀仍是真实内容）；非 2xx 仍按限长截断返回，
// 供 StatusError 消息使用。
func (h *httpClient) doLimit(ctx context.Context, method, url, accept, bearer string, in any, max int64) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > max {
		raw = raw[:max]
		if resp.StatusCode < 300 {
			// 超限的 2xx：返回已截断前缀 + ErrBodyTooLarge，调用方按
			// 「不完整件」处置（diff 端点的前缀仍是真实内容）。
			return resp.StatusCode, raw, fmt.Errorf("%w: %s %s 超过 %d 字节", ErrBodyTooLarge, method, url, max)
		}
	}
	return resp.StatusCode, raw, nil
}

// doJSON JSON 请求/响应封装；空响应体且未要 out 时跳过解析。
func (h *httpClient) doJSON(ctx context.Context, method, path, bearer string, in, out any) error {
	code, raw, err := h.do(ctx, method, h.baseURL+path, "application/vnd.github+json", bearer, in)
	if err != nil {
		return err
	}
	if code >= 300 {
		return &StatusError{StatusCode: code, Method: method, URL: path, Body: string(raw)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("githubapi: 解析响应失败 %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *restClient) statusErr(method, path string, code int, body []byte) error {
	return &StatusError{StatusCode: code, Method: method, URL: path, Body: string(body)}
}

type repoWire struct {
	FullName string `json:"full_name"`
}

type prWire struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	Body     *string    `json:"body"`
	HTMLURL  string     `json:"html_url"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	ClosedAt *time.Time `json:"closed_at"`
	// UpdatedAt 平台侧的 PR 最后更新戳：webhook 复核路径把它落成
	// seen/总账的 pr_updated_at 判定基准（Forgejo 适配器同源字段）。
	UpdatedAt *time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA  string   `json:"sha"`
		Ref  string   `json:"ref"`
		Repo repoWire `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string   `json:"ref"`
		Repo repoWire `json:"repo"`
	} `json:"base"`
}

func (c *restClient) GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*PullRequest, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number)
	var pr prWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		code, raw, err := c.h.do(ctx, http.MethodGet, c.h.baseURL+path, "application/vnd.github+json", bearer, nil)
		if err != nil {
			return err
		}
		if code >= 300 {
			return c.statusErr(http.MethodGet, path, code, raw)
		}
		return json.Unmarshal(raw, &pr)
	})
	if err != nil {
		return nil, err
	}
	return &PullRequest{
		RepoFullName: pr.Base.Repo.FullName,
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         deref(pr.Body),
		HTMLURL:      pr.HTMLURL,
		State:        pr.State,
		Draft:        pr.Draft,
		Merged:       pr.Merged,
		MergedAt:     pr.MergedAt,
		ClosedAt:     pr.ClosedAt,
		HeadSHA:      pr.Head.SHA,
		HeadRef:      pr.Head.Ref,
		BaseRef:      pr.Base.Ref,
		AuthorLogin:  pr.User.Login,
		UpdatedAt:    pr.UpdatedAt,
	}, nil
}

// diffBodyMaxBytes diff 端点读取上限，与 Forgejo 适配器的 respBodyMaxBytes
// 对齐；超限走 ErrBodyTooLarge 而非静默截断（半截 diff 不能当完整变更清单，
// 但前缀仍是真实内容——err=ErrBodyTooLarge 时返回值是尽量读到的前缀）。
const diffBodyMaxBytes = 64 << 20

// GetPullRequestDiff diff 端点返回 text/plain（Accept: vnd.github.v3.diff），
// 不走 JSON 解析。err 为 ErrBodyTooLarge 时返回值是已截断前缀。
func (c *restClient) GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number)
	var diff string
	err = c.withToken(ctx, installationID, func(bearer string) error {
		code, raw, err := c.h.doLimit(ctx, http.MethodGet, c.h.baseURL+path, "application/vnd.github.v3.diff", bearer, nil, diffBodyMaxBytes)
		if err != nil {
			diff = string(raw) // ErrBodyTooLarge 时 raw 是已截断前缀
			return err
		}
		if code >= 300 {
			return c.statusErr(http.MethodGet, path, code, raw)
		}
		diff = string(raw)
		return nil
	})
	if err != nil {
		return diff, err
	}
	return diff, nil
}

type reviewCommentWire struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body        string    `json:"body"`
	InReplyToID *int64    `json:"in_reply_to_id"`
	Path        string    `json:"path"`
	CreatedAt   time.Time `json:"created_at"`
}

const (
	reviewCommentPageSize = 100
	reviewCommentMaxPages = 10
)

func (c *restClient) ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewComment, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	var out []ReviewComment
	for page := 1; page <= reviewCommentMaxPages; page++ {
		var wire []reviewCommentWire
		err = c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=%d&page=%d",
					owner, name, number, reviewCommentPageSize, page),
				bearer, nil, &wire)
		})
		if err != nil {
			return nil, err
		}
		for _, w := range wire {
			out = append(out, ReviewComment{
				ID:          w.ID,
				User:        w.User.Login,
				Body:        w.Body,
				InReplyToID: w.InReplyToID,
				Path:        w.Path,
				CreatedAt:   w.CreatedAt,
			})
		}
		if len(wire) < reviewCommentPageSize {
			break
		}
	}
	return out, nil
}

type issueCommentWire struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// ListIssueCommentsForPR PR 会话评论（issues comments API，分页上限与行内评论一致）。
func (c *restClient) ListIssueCommentsForPR(ctx context.Context, installationID int64, repoFullName string, number int) ([]IssueComment, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	var out []IssueComment
	for page := 1; page <= reviewCommentMaxPages; page++ {
		var wire []issueCommentWire
		err = c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d",
					owner, name, number, reviewCommentPageSize, page),
				bearer, nil, &wire)
		})
		if err != nil {
			return nil, err
		}
		for _, w := range wire {
			out = append(out, IssueComment{ID: w.ID, User: w.User.Login, Body: w.Body, CreatedAt: w.CreatedAt})
		}
		if len(wire) < reviewCommentPageSize {
			break
		}
	}
	return out, nil
}

// FindPullRequestComment 按 marker 找机器人顶层评论：分页翻查（上限
// reviewCommentMaxPages），命中即停；单页请求会让藏在第 2 页以后的评论
// 找不到而重复发帖。
func (c *restClient) FindPullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (int64, bool, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return 0, false, err
	}
	for page := 1; page <= reviewCommentMaxPages; page++ {
		var wire []issueCommentWire
		err = c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d",
					owner, name, number, reviewCommentPageSize, page),
				bearer, nil, &wire)
		})
		if err != nil {
			return 0, false, err
		}
		for _, w := range wire {
			// 只认机器人自己发的结论：用户评论里引用 marker 不能劫持落点。
			if w.User.Type != "Bot" || !strings.HasPrefix(strings.TrimSpace(w.Body), marker) {
				continue
			}
			return w.ID, true, nil
		}
		if len(wire) < reviewCommentPageSize {
			break
		}
	}
	return 0, false, nil
}

func (c *restClient) CreatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (int64, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return 0, err
	}
	var out issueCommentWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPost,
			fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, number), bearer,
			map[string]string{"body": body}, &out)
	})
	if err != nil {
		return 0, err
	}
	return out.ID, nil
}

func (c *restClient) UpdatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return err
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPatch,
			fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, name, commentID), bearer,
			map[string]string{"body": body}, nil)
	})
}

func (c *restClient) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) (int64, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{
		"event":     in.Event,
		"commit_id": in.CommitID,
	}
	if in.Body != "" {
		payload["body"] = in.Body
	}
	if len(in.Comments) > 0 {
		comments := make([]map[string]any, 0, len(in.Comments))
		for _, c := range in.Comments {
			side := c.Side
			if side == "" {
				side = "RIGHT"
			}
			comments = append(comments, map[string]any{
				"path": c.Path, "line": c.Line, "side": side, "body": c.Body,
			})
		}
		payload["comments"] = comments
	}
	var wire struct {
		ID int64 `json:"id"`
	}
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPost,
			fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, name, number), bearer, payload, &wire)
	})
	if err != nil {
		return 0, err
	}
	return wire.ID, nil
}

// ListReviews 列出 PR 已有的 review：提交成功后响应丢失时，下一次同步据此
// 认定 review 已存在，避免重复提交同一批行内评论。
func (c *restClient) ListReviews(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewSummary, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	var out []ReviewSummary
	for page := 1; page <= reviewCommentMaxPages; page++ {
		var wire []struct {
			ID          int64     `json:"id"`
			CommitID    string    `json:"commit_id"`
			Body        string    `json:"body"`
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submitted_at"`
		}
		err = c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=%d&page=%d",
					owner, name, number, reviewCommentPageSize, page),
				bearer, nil, &wire)
		})
		if err != nil {
			return nil, err
		}
		for _, w := range wire {
			out = append(out, ReviewSummary{
				ID: w.ID, CommitID: w.CommitID, Body: w.Body, State: w.State, CreatedAt: w.SubmittedAt,
			})
		}
		if len(wire) < reviewCommentPageSize {
			break
		}
	}
	return out, nil
}

// CreateCommitStatus 提交 commit status（INT-135 审查状态输出）。
// GetRepository 仓库可访问性检查（Installation 校验）：成功返回 nil，
// 403/404 由 statusErr 转 *StatusError。
func (c *restClient) GetRepository(ctx context.Context, installationID int64, repoFullName string) error {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return err
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, name), bearer, nil, nil)
	})
}

// ResolveInstallation 共享 REST 实现拿不到 App JWT，无法解析仓库所属安装；
// 需要解析的调用方应使用 githubapp.Client（它覆盖本方法）。
func (c *restClient) ResolveInstallation(ctx context.Context, repoFullName string) (int64, error) {
	return 0, fmt.Errorf("githubapi: 该客户端不支持 installation 解析")
}

// gitTreeWire git/trees 响应：非递归时给该 tree 的直接子条目。
type gitTreeWire struct {
	Tree      []gitTreeEntry `json:"tree"`
	Truncated bool           `json:"truncated"`
}

type gitTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob / tree / commit（子模块）
}

// PathExistsAtRef 用非递归 git/trees 判路径存在性：拉取父目录的直接条目、
// 精确匹配末段名——contents API 会把目录/符号链接/子模块和大文件都混进
// 2xx，无法严格回答「树中是否有此路径」。路径必须是规范的仓库相对形态：
// 前导 /、空段、「.」「..」一律按不存在处理——不静默归一化（校验过的
// 形态会被原样存进结果，别名会造成校验对象与存储值不一致，且转义拦不下
// 它们，HTTP 层归一化可能把请求拼到别的端点上）。
func (c *restClient) PathExistsAtRef(ctx context.Context, installationID int64, repoFullName, path, ref string) (bool, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return false, err
	}
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return false, nil
		}
	}
	base := segs[len(segs)-1]
	dir := strings.Join(segs[:len(segs)-1], "/")
	escapedRef := url.PathEscape(ref)
	treeish := escapedRef
	if dir != "" {
		escaped := make([]string, 0, len(segs)-1)
		for _, s := range segs[:len(segs)-1] {
			escaped = append(escaped, url.PathEscape(s))
		}
		treeish += ":" + strings.Join(escaped, "/")
	}
	var out gitTreeWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/%s/git/trees/%s", owner, name, treeish), bearer, nil, &out)
	})
	if err != nil {
		// 目录清单超读入上限（>8MiB 的大目录）——与 truncated=true 同属
		// 不完整证据：不可判定，不可当缺席证明，也不该 50200 卡死重试。
		if errors.Is(err, ErrBodyTooLarge) {
			return false, fmt.Errorf("%w: %q 的目录清单超读入上限，无法判定是否存在", ErrTreeTruncated, path)
		}
		if !IsNotFound(err) {
			return false, err
		}
		// 404 有歧义：dir 级可能是「目录真不存在」，也可能是 ref/仓库本身
		// 不可达——后者报「文件不存在」会把基础设施故障误报成 Agent 的
		// 审查对象错误。探一次根树区分：根 404/出错 → 按调用失败上报；
		// 根在 → dir 确实缺，路径不存在。根级请求本身 404 即 ref 无效。
		if dir == "" {
			return false, err
		}
		var root gitTreeWire
		rootErr := c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/git/trees/%s", owner, name, escapedRef), bearer, nil, &root)
		})
		if rootErr != nil {
			if errors.Is(rootErr, ErrBodyTooLarge) {
				return false, fmt.Errorf("%w: 根树清单超读入上限，无法判定 %q 是否存在", ErrTreeTruncated, path)
			}
			return false, rootErr
		}
		return false, nil
	}
	for _, e := range out.Tree {
		if e.Path == base {
			return true, nil
		}
	}
	if out.Truncated {
		return false, fmt.Errorf("%w: 无法判定 %q 是否存在", ErrTreeTruncated, path)
	}
	return false, nil
}

// pullRequestFileWire pulls/N/files 元素：status=renamed 时 previous_filename
// 是旧路径（「不该删它」类 finding 指向旧名，两侧都收）。
type pullRequestFileWire struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
}

// prFilesHardCap GitHub files 端点硬封顶：单 PR 最多返回 3000 个文件，
// 打满即按不完整清单处理（缺席不作否决证据）。
const prFilesHardCap = 3000

// ListPullRequestFiles 分页拉 PR 变更文件清单（每页 100）。平台对超大 PR 的
// diff 文本可能无标记截断——否决类证据必须用这份权威分页清单。
func (c *restClient) ListPullRequestFiles(ctx context.Context, installationID int64, repoFullName string, number int) ([]string, bool, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for page := 1; ; page++ {
		var items []pullRequestFileWire
		err := c.withToken(ctx, installationID, func(bearer string) error {
			return c.h.doJSON(ctx, http.MethodGet,
				fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, name, number, page), bearer, nil, &items)
		})
		if err != nil {
			// 单页带 patch 负载可超 8MiB 读上限：清单判不完整——缺席不作
			// 否决证据，比每次 50200 卡死重试强。
			if errors.Is(err, ErrBodyTooLarge) {
				return paths, false, nil
			}
			return nil, false, err
		}
		for _, f := range items {
			add(f.Filename)
			add(f.PreviousFilename)
		}
		if len(items) < 100 {
			return paths, true, nil
		}
		if len(paths) >= prFilesHardCap {
			return paths, false, nil
		}
	}
}

func (c *restClient) CreateCommitStatus(ctx context.Context, installationID int64, repoFullName, sha, state, statusContext, description, targetURL string) error {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return err
	}
	payload := map[string]any{"state": state, "context": statusContext}
	if description != "" {
		payload["description"] = description
	}
	if targetURL != "" {
		payload["target_url"] = targetURL
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPost,
			fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, name, sha), bearer, payload, nil)
	})
}
