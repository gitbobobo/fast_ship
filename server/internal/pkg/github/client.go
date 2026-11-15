package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gh "github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

func ptr[T any](v T) *T { return &v }

// Client 业务 service 依赖的 GitHub repo 能力面（Issue 同步/写、分支、发布）。
// 接口只暴露领域类型（types.go）：分页返回下一页页码（0 表示末页），
// go-github 的 Response/Repository/Label/Release 类型不外泄，错误分类经
// errors.go 的判定助手完成。实现只有 *TokenClient；PR 读取（关联 PR 的
// attach/sync）走 githubapi.GitHubClient 的 *PRClient 实现，两个 adapter
// 共享同一份 REST 代码。
type Client interface {
	ValidateRepository(ctx context.Context) error
	// FetchRepository 拉取绑定仓库的元数据（含 has_issues）：批量关闭用它区分
	// 404 的整仓级与单条成因（INT-198）。与 ValidateRepository 同一端点，但
	// 保留仓库投影，调用方按 has_issues 细分。
	FetchRepository(ctx context.Context) (*Repository, error)
	ListIssues(ctx context.Context, state string, since *time.Time, page, perPage int) ([]*Issue, int, error)
	GetIssue(ctx context.Context, issueNumber int) (*Issue, error)
	ListRepositoryLabels(ctx context.Context, page, perPage int) ([]*Label, int, error)
	ListIssueComments(ctx context.Context, issueNumber, page, perPage int) ([]*IssueComment, int, error)
	ListIssueTimeline(ctx context.Context, issueNumber, page, perPage int) ([]*TimelineEvent, int, error)
	CreateIssueComment(ctx context.Context, issueNumber int, body string) (*IssueComment, error)
	UpdateIssue(ctx context.Context, issueNumber int, req UpdateIssueRequest) (*Issue, error)
	CreateIssue(ctx context.Context, title, body string) (*Issue, error)
	CreateTag(ctx context.Context, tag, commitish string) error
	CreateRelease(ctx context.Context, tag, name, body string) (*Release, error)
	UploadAsset(ctx context.Context, releaseID int64, filename string, file *os.File) error
	ListBranches(ctx context.Context) ([]*Branch, string, error)
}

// ClientFactory 按 token/owner/repo 构造绑定单仓库的客户端；测试注入替身的缝。
type ClientFactory func(token, owner, repo string) Client

// 编译期钉住具体实现满足接口。
var _ Client = (*TokenClient)(nil)

type TokenClient struct {
	client *gh.Client
	upload *gh.Client
	owner  string
	repo   string
}

const mediaTypeFullJSON = "application/vnd.github.full+json"

// nextPageOf 取响应的下一页页码；无响应（请求失败）时按末页处理。
func nextPageOf(resp *gh.Response) int {
	if resp == nil {
		return 0
	}
	return resp.NextPage
}

func repositoryFromSDK(repo *gh.Repository) *Repository {
	if repo == nil {
		return nil
	}
	return &Repository{HasIssues: repo.HasIssues}
}

func labelFromSDK(label *gh.Label) *Label {
	if label == nil {
		return nil
	}
	return &Label{
		Name:        label.Name,
		Color:       label.Color,
		Description: label.Description,
	}
}

func releaseFromSDK(release *gh.RepositoryRelease) *Release {
	if release == nil {
		return nil
	}
	return &Release{
		ID:      release.ID,
		HTMLURL: release.HTMLURL,
	}
}

func (c *TokenClient) ListRepositoryLabels(ctx context.Context, page, perPage int) ([]*Label, int, error) {
	opts := &gh.ListOptions{Page: page, PerPage: perPage}
	labels, resp, err := c.client.Issues.ListLabels(ctx, c.owner, c.repo, opts)
	if err != nil {
		return nil, nextPageOf(resp), err
	}
	out := make([]*Label, 0, len(labels))
	for _, label := range labels {
		out = append(out, labelFromSDK(label))
	}
	return out, nextPageOf(resp), nil
}

// githubRequestTimeout 兜底所有 GitHub API 调用：连接半死时无超时的请求会
// 永久挂住调用方 goroutine（auto-sync / push 等路径都在后台 goroutine 里跑）。
const githubRequestTimeout = 30 * time.Second

// githubUploadTimeout 单独给 release asset 上传：http.Client.Timeout 覆盖整个
// 请求体传输，大文件在慢链路上会超出 30s 被误杀；仍设上限防半死连接永久挂住。
const githubUploadTimeout = 30 * time.Minute

// NewClient 创建绑定单个 owner/repo 的 GitHub 客户端。
// token 为空时走未认证请求：空 AccessToken 的 oauth2 客户端仍会发出
// "Authorization: Bearer " 头导致 401，而未认证请求可访问公共仓库
// （未配 GitHub 的项目 attach 公共仓库 PR 依赖这一行为）。
func NewClient(token, owner, repo string) *TokenClient {
	return &TokenClient{
		client: gh.NewClient(newHTTPClient(token, githubRequestTimeout)),
		upload: gh.NewClient(newHTTPClient(token, githubUploadTimeout)),
		owner:  owner,
		repo:   repo,
	}
}

func newHTTPClient(token string, timeout time.Duration) *http.Client {
	if token == "" {
		return &http.Client{Timeout: timeout}
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	c := oauth2.NewClient(context.Background(), ts)
	c.Timeout = timeout
	return c
}

func (c *TokenClient) ValidateRepository(ctx context.Context) error {
	_, err := c.FetchRepository(ctx)
	return err
}

func (c *TokenClient) FetchRepository(ctx context.Context) (*Repository, error) {
	repo, _, err := c.client.Repositories.Get(ctx, c.owner, c.repo)
	if err != nil {
		return nil, err
	}
	return repositoryFromSDK(repo), nil
}

func (c *TokenClient) ListIssues(ctx context.Context, state string, since *time.Time, page, perPage int) ([]*Issue, int, error) {
	query := url.Values{}
	if state != "" {
		query.Set("state", state)
	}
	query.Set("sort", "updated")
	query.Set("direction", "asc")
	if page > 0 {
		query.Set("page", fmt.Sprintf("%d", page))
	}
	if perPage > 0 {
		query.Set("per_page", fmt.Sprintf("%d", perPage))
	}
	if since != nil {
		query.Set("since", since.UTC().Format(time.RFC3339))
	}

	path := fmt.Sprintf("repos/%s/%s/issues?%s", c.owner, c.repo, query.Encode())
	req, err := c.client.NewRequest("GET", path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var issues []*Issue
	resp, err := c.client.Do(ctx, req, &issues)
	if err != nil {
		return nil, nextPageOf(resp), err
	}
	return issues, nextPageOf(resp), nil
}

func (c *TokenClient) ListIssueComments(ctx context.Context, issueNumber, page, perPage int) ([]*IssueComment, int, error) {
	query := url.Values{}
	query.Set("sort", "created")
	query.Set("direction", "asc")
	if page > 0 {
		query.Set("page", fmt.Sprintf("%d", page))
	}
	if perPage > 0 {
		query.Set("per_page", fmt.Sprintf("%d", perPage))
	}

	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments?%s", c.owner, c.repo, issueNumber, query.Encode())
	req, err := c.client.NewRequest("GET", path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var comments []*IssueComment
	resp, err := c.client.Do(ctx, req, &comments)
	if err != nil {
		return nil, nextPageOf(resp), err
	}
	return comments, nextPageOf(resp), nil
}

func (c *TokenClient) ListIssueTimeline(ctx context.Context, issueNumber, page, perPage int) ([]*TimelineEvent, int, error) {
	query := url.Values{}
	if page > 0 {
		query.Set("page", fmt.Sprintf("%d", page))
	}
	if perPage > 0 {
		query.Set("per_page", fmt.Sprintf("%d", perPage))
	}
	path := fmt.Sprintf("repos/%s/%s/issues/%d/timeline?%s", c.owner, c.repo, issueNumber, query.Encode())
	req, err := c.client.NewRequest("GET", path, nil)
	if err != nil {
		return nil, 0, err
	}

	var events []*TimelineEvent
	resp, err := c.client.Do(ctx, req, &events)
	if err != nil {
		return nil, nextPageOf(resp), err
	}
	return events, nextPageOf(resp), nil
}

func (c *TokenClient) CreateIssueComment(ctx context.Context, issueNumber int, body string) (*IssueComment, error) {
	payload := map[string]string{"body": body}
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments", c.owner, c.repo, issueNumber)
	req, err := c.client.NewRequest("POST", path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var comment IssueComment
	if _, err := c.client.Do(ctx, req, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// GetIssue 拉取单个 Issue 的当前远端快照（含 updated_at 与 comments 计数）。
// 同步兜底重拉（INT-105）用它取得真实的远端水位：本地 issues.updated_at 会被
// GORM 自动更新与本地工作流操作污染，不能当远端时间戳。
func (c *TokenClient) GetIssue(ctx context.Context, issueNumber int) (*Issue, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d", c.owner, c.repo, issueNumber)
	req, err := c.client.NewRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var issue Issue
	if _, err := c.client.Do(ctx, req, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func (c *TokenClient) UpdateIssue(ctx context.Context, issueNumber int, payload UpdateIssueRequest) (*Issue, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d", c.owner, c.repo, issueNumber)
	req, err := c.client.NewRequest("PATCH", path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var issue Issue
	if _, err := c.client.Do(ctx, req, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func (c *TokenClient) CreateIssue(ctx context.Context, title, body string) (*Issue, error) {
	payload := map[string]string{"title": title, "body": body}
	path := fmt.Sprintf("repos/%s/%s/issues", c.owner, c.repo)
	req, err := c.client.NewRequest("POST", path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var issue Issue
	if _, err := c.client.Do(ctx, req, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// CreateTag 创建 Git Tag（如果不存在）
func (c *TokenClient) CreateTag(ctx context.Context, tag, commitish string) error {
	commitish = strings.TrimSpace(commitish)
	if commitish == "" {
		return errors.New("target commitish is empty")
	}

	// 检查 tag 是否已存在
	_, _, err := c.client.Git.GetRef(ctx, c.owner, c.repo, "tags/"+tag)
	if err == nil {
		return nil // tag 已存在，跳过
	}
	if !isNotFound(err) {
		return err
	}

	sha, err := c.resolveCommitish(ctx, commitish)
	if err != nil {
		return fmt.Errorf("resolve target %q to commit SHA failed: %w", commitish, err)
	}

	ref := &gh.Reference{
		Ref:    ptr("refs/tags/" + tag),
		Object: &gh.GitObject{SHA: ptr(sha)},
	}
	_, _, err = c.client.Git.CreateRef(ctx, c.owner, c.repo, ref)
	return err
}

// CreateRelease 创建 GitHub Release（如果不存在则创建，已存在则返回已有的）
func (c *TokenClient) CreateRelease(ctx context.Context, tag, name, body string) (*Release, error) {
	// 检查是否已存在
	release, _, err := c.client.Repositories.GetReleaseByTag(ctx, c.owner, c.repo, tag)
	if err == nil {
		return releaseFromSDK(release), nil
	}
	if !isNotFound(err) {
		return nil, err
	}

	rel := &gh.RepositoryRelease{
		TagName: ptr(tag),
		Name:    ptr(name),
		Body:    ptr(body),
	}
	release, _, err = c.client.Repositories.CreateRelease(ctx, c.owner, c.repo, rel)
	return releaseFromSDK(release), err
}

// UploadAsset 上传文件到 Release
func (c *TokenClient) UploadAsset(ctx context.Context, releaseID int64, filename string, file *os.File) error {
	// 先检查是否有同名 asset，有则删除
	assets, _, err := c.client.Repositories.ListReleaseAssets(ctx, c.owner, c.repo, releaseID, nil)
	if err != nil {
		return fmt.Errorf("list release assets failed: %w", err)
	}
	for _, asset := range assets {
		if asset.GetName() == filename {
			if _, err := c.client.Repositories.DeleteReleaseAsset(ctx, c.owner, c.repo, asset.GetID()); err != nil {
				return fmt.Errorf("delete existing asset %s failed: %w", filename, err)
			}
			break
		}
	}

	opts := &gh.UploadOptions{Name: filename}
	_, _, err = c.upload.Repositories.UploadReleaseAsset(ctx, c.owner, c.repo, releaseID, opts, file)
	if err != nil {
		return fmt.Errorf("upload asset %s failed: %w", filename, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var errResp *gh.ErrorResponse
	return errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == 404
}

func (c *TokenClient) resolveCommitish(ctx context.Context, commitish string) (string, error) {
	sha, _, err := c.client.Repositories.GetCommitSHA1(ctx, c.owner, c.repo, commitish, "")
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", fmt.Errorf("commit %q returned empty SHA", commitish)
	}
	return sha, nil
}

// ListBranches fetches all branches from the repository
func (c *TokenClient) ListBranches(ctx context.Context) ([]*Branch, string, error) {
	// Get repository info to find default branch
	repo, _, err := c.client.Repositories.Get(ctx, c.owner, c.repo)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get repository: %w", err)
	}
	defaultBranch := repo.GetDefaultBranch()

	// List all branches
	opts := &gh.BranchListOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var allBranches []*Branch

	for {
		branches, resp, err := c.client.Repositories.ListBranches(ctx, c.owner, c.repo, opts)
		if err != nil {
			return nil, "", fmt.Errorf("failed to list branches: %w", err)
		}

		for _, branch := range branches {
			allBranches = append(allBranches, &Branch{
				Name:    branch.GetName(),
				SHA:     branch.GetCommit().GetSHA(),
				Default: branch.GetName() == defaultBranch,
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}

	return allBranches, defaultBranch, nil
}
