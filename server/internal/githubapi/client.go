package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	// 机器人顶层评论（每 PR 一条）
	FindPullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (commentID int64, found bool, err error)
	CreatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (commentID int64, err error)
	UpdatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error
	// check run
	CreateCheckRun(ctx context.Context, installationID int64, repoFullName, name, headSHA, externalID string) (checkRunID int64, err error)
	UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in CheckUpdate) error
	// review
	SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) error
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

// do 发任意请求，返回状态码与（截到 8MB 的）响应体。ctx 取消时中止等待。
func (h *httpClient) do(ctx context.Context, method, url, accept, bearer string, in any) (int, []byte, error) {
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
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
	User     struct {
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
	}, nil
}

// GetPullRequestDiff diff 端点返回 text/plain（Accept: vnd.github.v3.diff），
// 不走 JSON 解析。
func (c *restClient) GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number)
	var diff string
	err = c.withToken(ctx, installationID, func(bearer string) error {
		code, raw, err := c.h.do(ctx, http.MethodGet, c.h.baseURL+path, "application/vnd.github.v3.diff", bearer, nil)
		if err != nil {
			return err
		}
		if code >= 300 {
			return c.statusErr(http.MethodGet, path, code, raw)
		}
		diff = string(raw)
		return nil
	})
	if err != nil {
		return "", err
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
	} `json:"user"`
	Body string `json:"body"`
}

func (c *restClient) FindPullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (int64, bool, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return 0, false, err
	}
	var wire []issueCommentWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, name, number), bearer, nil, &wire)
	})
	if err != nil {
		return 0, false, err
	}
	for _, w := range wire {
		if strings.Contains(w.Body, marker) {
			return w.ID, true, nil
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

type checkRunWire struct {
	ID int64 `json:"id"`
}

func (c *restClient) CreateCheckRun(ctx context.Context, installationID int64, repoFullName, checkName, headSHA, externalID string) (int64, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return 0, err
	}
	var out checkRunWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/check-runs", owner, name), bearer, map[string]any{
			"name":        checkName,
			"head_sha":    headSHA,
			"status":      string(CheckQueued),
			"external_id": externalID,
		}, &out)
	})
	if err != nil {
		return 0, err
	}
	return out.ID, nil
}

func (c *restClient) UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in CheckUpdate) error {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"status": string(in.Status),
	}
	if in.Title != "" {
		payload["name"] = in.Title
	}
	if in.Summary != "" || in.Text != "" {
		payload["output"] = map[string]any{
			"title":   in.Title,
			"summary": in.Summary,
			"text":    in.Text,
		}
	}
	if in.Status == CheckCompleted {
		payload["conclusion"] = string(in.Conclusion)
		payload["completed_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPatch,
			fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, name, checkRunID), bearer, payload, nil)
	})
}

func (c *restClient) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) error {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"event":     in.Event,
		"commit_id": in.CommitID,
	}
	if in.Body != "" {
		payload["body"] = in.Body
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPost,
			fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, name, number), bearer, payload, nil)
	})
}
