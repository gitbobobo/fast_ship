package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	gh "github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

func ptr[T any](v T) *T { return &v }

type Client struct {
	client *gh.Client
	owner  string
	repo   string
}

const mediaTypeFullJSON = "application/vnd.github.full+json"

type Issue struct {
	gh.Issue
	BodyHTML *string `json:"body_html,omitempty"`
}

func (i *Issue) GetBodyHTML() string {
	if i == nil || i.BodyHTML == nil {
		return ""
	}
	return *i.BodyHTML
}

type IssueComment struct {
	gh.IssueComment
	BodyHTML *string `json:"body_html,omitempty"`
}

func (c *IssueComment) GetBodyHTML() string {
	if c == nil || c.BodyHTML == nil {
		return ""
	}
	return *c.BodyHTML
}

type UpdateIssueRequest struct {
	Title       *string   `json:"title,omitempty"`
	Body        *string   `json:"body,omitempty"`
	State       *string   `json:"state,omitempty"`
	StateReason *string   `json:"state_reason,omitempty"`
	Labels      *[]string `json:"labels,omitempty"`
}

// Branch represents a GitHub branch
type Branch struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Default bool   `json:"default"`
}

func (c *Client) ListRepositoryLabels(ctx context.Context, page, perPage int) ([]*gh.Label, *gh.Response, error) {
	opts := &gh.ListOptions{Page: page, PerPage: perPage}
	return c.client.Issues.ListLabels(ctx, c.owner, c.repo, opts)
}

func NewClient(token, owner, repo string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &Client{
		client: gh.NewClient(tc),
		owner:  owner,
		repo:   repo,
	}
}

func (c *Client) ValidateRepository(ctx context.Context) error {
	_, _, err := c.client.Repositories.Get(ctx, c.owner, c.repo)
	return err
}

func (c *Client) ListIssues(ctx context.Context, state string, since *time.Time, page, perPage int) ([]*Issue, *gh.Response, error) {
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
		return nil, nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var issues []*Issue
	resp, err := c.client.Do(ctx, req, &issues)
	if err != nil {
		return nil, resp, err
	}
	return issues, resp, nil
}

func (c *Client) ListIssueComments(ctx context.Context, issueNumber, page, perPage int) ([]*IssueComment, *gh.Response, error) {
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
		return nil, nil, err
	}
	req.Header.Set("Accept", mediaTypeFullJSON)

	var comments []*IssueComment
	resp, err := c.client.Do(ctx, req, &comments)
	if err != nil {
		return nil, resp, err
	}
	return comments, resp, nil
}

type TimelineIssueType struct {
	ID     int64  `json:"id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Color  string `json:"color,omitempty"`
}

func (t *TimelineIssueType) GetName() string {
	if t == nil {
		return ""
	}
	return t.Name
}
func (t *TimelineIssueType) GetColor() string {
	if t == nil {
		return ""
	}
	return t.Color
}

type TimelineEvent struct {
	gh.Timeline
	IssueType *TimelineIssueType `json:"issue_type,omitempty"`
}

func (t *TimelineEvent) GetIssueType() *TimelineIssueType {
	if t == nil {
		return nil
	}
	return t.IssueType
}

func (c *Client) ListIssueTimeline(ctx context.Context, issueNumber, page, perPage int) ([]*TimelineEvent, *gh.Response, error) {
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
		return nil, nil, err
	}

	var events []*TimelineEvent
	resp, err := c.client.Do(ctx, req, &events)
	if err != nil {
		return nil, resp, err
	}
	return events, resp, nil
}

func (c *Client) CreateIssueComment(ctx context.Context, issueNumber int, body string) (*IssueComment, error) {
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

func (c *Client) UpdateIssue(ctx context.Context, issueNumber int, payload UpdateIssueRequest) (*Issue, error) {
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

func (c *Client) CreateIssue(ctx context.Context, title, body string) (*Issue, error) {
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
func (c *Client) CreateTag(ctx context.Context, tag, commitish string) error {
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
func (c *Client) CreateRelease(ctx context.Context, tag, name, body string) (*gh.RepositoryRelease, error) {
	// 检查是否已存在
	release, _, err := c.client.Repositories.GetReleaseByTag(ctx, c.owner, c.repo, tag)
	if err == nil {
		return release, nil
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
	return release, err
}

// UploadAsset 上传文件到 Release
func (c *Client) UploadAsset(ctx context.Context, releaseID int64, filename string, file *os.File) error {
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
	_, _, err = c.client.Repositories.UploadReleaseAsset(ctx, c.owner, c.repo, releaseID, opts, file)
	if err != nil {
		return fmt.Errorf("upload asset %s failed: %w", filename, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var errResp *gh.ErrorResponse
	return errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == 404
}

func (c *Client) resolveCommitish(ctx context.Context, commitish string) (string, error) {
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
func (c *Client) ListBranches(ctx context.Context) ([]*Branch, string, error) {
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
