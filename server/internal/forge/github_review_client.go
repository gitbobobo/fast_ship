package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// PullRequestFromGitHub 把 githubapi.PullRequest 逐字段转成中性类型：GitHub
// 适配器的读路径与 GitHub 专属路径（如投递重投的实况闸门）共用同一份转换。
func PullRequestFromGitHub(pr *githubapi.PullRequest) *PullRequest {
	if pr == nil {
		return nil
	}
	return &PullRequest{
		RepoFullName: pr.RepoFullName,
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         pr.Body,
		HTMLURL:      pr.HTMLURL,
		State:        pr.State,
		Draft:        pr.Draft,
		Merged:       pr.Merged,
		MergedAt:     pr.MergedAt,
		ClosedAt:     pr.ClosedAt,
		HeadSHA:      pr.HeadSHA,
		HeadRef:      pr.HeadRef,
		BaseRef:      pr.BaseRef,
		AuthorLogin:  pr.AuthorLogin,
		UpdatedAt:    pr.UpdatedAt,
	}
}

// GitHubReviewClient forge.ReviewClient 的 GitHub 适配器：构造时收编
// installationID，方法 1:1 透传到 githubapi.GitHubClient（请求序列、安装 id
// 语义与错误都保持原样），数据类型逐字段转换成 forge 中性类型。
type GitHubReviewClient struct {
	api            githubapi.GitHubClient
	installationID int64
}

// 编译期钉住适配器满足审查访问面契约。
var _ ReviewClient = (*GitHubReviewClient)(nil)

// NewGitHubReviewClient 把 GitHub 访问面与安装 id 组装成审查客户端。installationID=0
// 原样保留：只出现在仓库级前置检查，githubapp.Client 对 0 的「先解析安装再
// 查仓库」语义在底层不变。
func NewGitHubReviewClient(api githubapi.GitHubClient, installationID int64) *GitHubReviewClient {
	return &GitHubReviewClient{api: api, installationID: installationID}
}

// Kind 报告集成 kind。
func (c *GitHubReviewClient) Kind() string { return KindGitHub }

func (c *GitHubReviewClient) GetPullRequest(ctx context.Context, repoFullName string, number int) (*PullRequest, error) {
	pr, err := c.api.GetPullRequest(ctx, c.installationID, repoFullName, number)
	if err != nil {
		return nil, err
	}
	return PullRequestFromGitHub(pr), nil
}

func (c *GitHubReviewClient) GetPullRequestDiff(ctx context.Context, repoFullName string, number int) (string, error) {
	diff, err := c.api.GetPullRequestDiff(ctx, c.installationID, repoFullName, number)
	if errors.Is(err, githubapi.ErrBodyTooLarge) {
		// 本端截断探测与 Forgejo 适配器同一哨兵口径：service 只认
		// IsDiffTooLarge，不区分两种来源；前缀原样透传。
		return diff, fmt.Errorf("%w: github: %s #%d 的 diff 超过本端读入上限", ErrDiffTooLarge, repoFullName, number)
	}
	return diff, err
}

func (c *GitHubReviewClient) ListPullRequestFiles(ctx context.Context, repoFullName string, number int) ([]string, bool, error) {
	return c.api.ListPullRequestFiles(ctx, c.installationID, repoFullName, number)
}

func (c *GitHubReviewClient) ListReviewComments(ctx context.Context, repoFullName string, number int) ([]ReviewComment, error) {
	list, err := c.api.ListReviewComments(ctx, c.installationID, repoFullName, number)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(list))
	for _, w := range list {
		out = append(out, ReviewComment{
			ID: w.ID, User: w.User, Body: w.Body, InReplyToID: w.InReplyToID,
			Path: w.Path, CreatedAt: w.CreatedAt,
		})
	}
	return out, nil
}

func (c *GitHubReviewClient) ListIssueCommentsForPR(ctx context.Context, repoFullName string, number int) ([]IssueComment, error) {
	list, err := c.api.ListIssueCommentsForPR(ctx, c.installationID, repoFullName, number)
	if err != nil {
		return nil, err
	}
	out := make([]IssueComment, 0, len(list))
	for _, w := range list {
		out = append(out, IssueComment{ID: w.ID, User: w.User, Body: w.Body, CreatedAt: w.CreatedAt})
	}
	return out, nil
}

func (c *GitHubReviewClient) FindPullRequestComment(ctx context.Context, repoFullName string, number int, marker string) (int64, bool, error) {
	return c.api.FindPullRequestComment(ctx, c.installationID, repoFullName, number, marker)
}

func (c *GitHubReviewClient) CreatePullRequestComment(ctx context.Context, repoFullName string, number int, body string) (int64, error) {
	return c.api.CreatePullRequestComment(ctx, c.installationID, repoFullName, number, body)
}

func (c *GitHubReviewClient) UpdatePullRequestComment(ctx context.Context, repoFullName string, commentID int64, body string) error {
	return c.api.UpdatePullRequestComment(ctx, c.installationID, repoFullName, commentID, body)
}

func (c *GitHubReviewClient) ListReviews(ctx context.Context, repoFullName string, number int) ([]ReviewSummary, error) {
	list, err := c.api.ListReviews(ctx, c.installationID, repoFullName, number)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewSummary, 0, len(list))
	for _, w := range list {
		// PENDING 是未提交草稿，对读者不可见——「已提交」契约内不可认，
		// 否则对账认领会把 pending 运行的 findings 误判已投递。
		if strings.EqualFold(w.State, "PENDING") {
			continue
		}
		out = append(out, ReviewSummary{
			ID: w.ID, CommitID: w.CommitID, Body: w.Body, State: w.State, CreatedAt: w.CreatedAt,
		})
	}
	return out, nil
}

func (c *GitHubReviewClient) SubmitReview(ctx context.Context, repoFullName string, number int, in ReviewInput) (int64, error) {
	comments := make([]githubapi.ReviewInlineComment, 0, len(in.Comments))
	for _, w := range in.Comments {
		comments = append(comments, githubapi.ReviewInlineComment{
			Path: w.Path, Line: w.Line, Side: w.Side, Body: w.Body,
		})
	}
	return c.api.SubmitReview(ctx, c.installationID, repoFullName, number, githubapi.ReviewInput{
		Event: in.Event, Body: in.Body, CommitID: in.CommitID, Comments: comments,
	})
}

func (c *GitHubReviewClient) CreateCommitStatus(ctx context.Context, repoFullName, sha, state, statusContext, description, targetURL string) error {
	return c.api.CreateCommitStatus(ctx, c.installationID, repoFullName, sha, state, statusContext, description, targetURL)
}

func (c *GitHubReviewClient) GetRepository(ctx context.Context, repoFullName string) error {
	return c.api.GetRepository(ctx, c.installationID, repoFullName)
}

func (c *GitHubReviewClient) PathExistsAtRef(ctx context.Context, repoFullName, path, ref string) (bool, error) {
	exists, err := c.api.PathExistsAtRef(ctx, c.installationID, repoFullName, path, ref)
	if errors.Is(err, githubapi.ErrTreeTruncated) {
		return false, fmt.Errorf("%w: %s@%s", ErrTreeTruncated, path, ref)
	}
	return exists, err
}

// Bearer 媒体下载令牌：委派底层实现的 Bearer(ctx, installationID)（私有仓库
// 截图下载）。实现没有凭证面（静态 token / 匿名）时返回空串，调用方按匿名
// 下载处理——与令牌路径改造前 main 的类型断言同口径。
func (c *GitHubReviewClient) Bearer(ctx context.Context) (string, error) {
	src, ok := c.api.(interface {
		Bearer(context.Context, int64) (string, error)
	})
	if !ok {
		return "", nil
	}
	return src.Bearer(ctx, c.installationID)
}
