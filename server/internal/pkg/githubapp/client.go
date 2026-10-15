package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient GitHub REST 的极薄封装：JSON 进出、Bearer 注入、非 2xx 转
// *StatusError。不引第三方 SDK——评审链路只用少数几个端点，直接写清请求形状。
type httpClient struct {
	baseURL string
}

// githubHTTP GitHub 调用的共享客户端。Timeout 是 ctx 缺失时的兜底；调用方
// 传入的 ctx 仍通过 NewRequestWithContext 取消。
var githubHTTP = &http.Client{Timeout: 30 * time.Second}

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
			return fmt.Errorf("githubapp: 解析响应失败 %s %s: %w", method, path, err)
		}
	}
	return nil
}

// Client PullRequestGitHub 的真实实现：每次调用现场取（缓存的）安装令牌，
// 401 时作废缓存重试一次（令牌可能在 GitHub 侧被吊销）。
type Client struct {
	store *InstallationTokenStore
	nowFn func() time.Time
	h     *httpClient
}

func NewClient(store *InstallationTokenStore) *Client {
	return &Client{
		store: store,
		nowFn: time.Now,
		h:     store.http,
	}
}

// withToken 按「调用 → 401 → 作废缓存重试一次」的口径执行。
func (c *Client) withToken(ctx context.Context, installationID int64, fn func(bearer string) error) error {
	bearer, err := c.store.Token(ctx, installationID, c.nowFn())
	if err != nil {
		return err
	}
	err = fn(bearer)
	if err == nil || !isStatus(err, http.StatusUnauthorized) {
		return err
	}
	c.store.Invalidate(installationID)
	bearer, err = c.store.Token(ctx, installationID, c.nowFn())
	if err != nil {
		return err
	}
	return fn(bearer)
}

func isStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == code
}

func (c *Client) statusErr(method, path string, code int, body []byte) error {
	return &StatusError{StatusCode: code, Method: method, URL: path, Body: string(body)}
}

type repoWire struct {
	FullName string `json:"full_name"`
	Fork     bool   `json:"fork"`
}

type prWire struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	HTMLURL string  `json:"html_url"`
	Draft   bool    `json:"draft"`
	Head    struct {
		SHA  string   `json:"sha"`
		Ref  string   `json:"ref"`
		Repo repoWire `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string   `json:"ref"`
		Repo repoWire `json:"repo"`
	} `json:"base"`
}

func (c *Client) GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*PullRequest, error) {
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
		RepoFullName: pr.Head.Repo.FullName,
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         deref(pr.Body),
		HTMLURL:      pr.HTMLURL,
		HeadSHA:      pr.Head.SHA,
		HeadRef:      pr.Head.Ref,
		BaseRef:      pr.Base.Ref,
		Draft:        pr.Draft,
	}, nil
}

// GetPullRequestDiff diff 端点返回 text/plain（Accept: vnd.github.v3.diff），
// 不走 JSON 解析。
func (c *Client) GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error) {
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

func (c *Client) ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewComment, error) {
	owner, name, err := splitRepo(repoFullName)
	if err != nil {
		return nil, err
	}
	var wire []reviewCommentWire
	err = c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, name, number), bearer, nil, &wire)
	})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(wire))
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
	return out, nil
}

type issueCommentWire struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body string `json:"body"`
}

func (c *Client) FindIssueCommentByMarker(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (int64, bool, error) {
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

func (c *Client) CreateIssueComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (int64, error) {
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

func (c *Client) UpdateIssueComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error {
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

func (c *Client) CreateCheckRun(ctx context.Context, installationID int64, repoFullName, checkName, headSHA, externalID string) (int64, error) {
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

func (c *Client) UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in CheckUpdate) error {
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
		payload["completed_at"] = c.nowFn().UTC().Format(time.RFC3339)
	}
	return c.withToken(ctx, installationID, func(bearer string) error {
		return c.h.doJSON(ctx, http.MethodPatch,
			fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, name, checkRunID), bearer, payload, nil)
	})
}

func (c *Client) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) error {
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

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
