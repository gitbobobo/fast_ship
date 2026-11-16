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
		RepoFullName:     pr.RepoFullName,
		Number:           pr.Number,
		Title:            pr.Title,
		Body:             pr.Body,
		HTMLURL:          pr.HTMLURL,
		State:            pr.State,
		Draft:            pr.Draft,
		Merged:           pr.Merged,
		MergedAt:         pr.MergedAt,
		ClosedAt:         pr.ClosedAt,
		HeadSHA:          pr.HeadSHA,
		HeadRef:          pr.HeadRef,
		HeadRepoFullName: pr.HeadRepoFullName,
		BaseRef:          pr.BaseRef,
		AuthorLogin:      pr.AuthorLogin,
		UpdatedAt:        pr.UpdatedAt,
	}
}

// GitHubReviewClient forge.ReviewClient 的 GitHub 适配器：构造时收编
// installationID（由 resolver 按仓库现解析，INT-230），方法 1:1 透传到
// githubapi.GitHubClient（请求序列、安装 id 语义与错误都保持原样），数据
// 类型逐字段转换成 forge 中性类型。
type GitHubReviewClient struct {
	api            githubapi.GitHubClient
	installationID int64
	// reResolve 安装 id 陈旧时的重解析钩子（由 resolver 注入，作废缓存后
	// 实查）；为 nil 时不重试——直连 NewGitHubReviewClient 的测试桩保持
	// 单次透传语义。
	reResolve func(context.Context) (int64, error)
}

// 编译期钉住适配器满足审查访问面契约。
var _ ReviewClient = (*GitHubReviewClient)(nil)

// NewGitHubReviewClient 把 GitHub 访问面与安装 id 组装成审查客户端。合法
// 调用方两个：审查链路经 resolver 按仓库解析安装 id；关联 PR 链路（service
// 的 newPRClient）以 installationID=0 适配静态 Token 的 PRClient——0 是占位
// 值，staticTokenAuth.Bearer 忽略安装 id。换成 App 安装客户端时不得沿用 0：
// githubapp.Client 对 0 的「先解析安装再查仓库」语义在底层不变。
func NewGitHubReviewClient(api githubapi.GitHubClient, installationID int64) *GitHubReviewClient {
	return &GitHubReviewClient{api: api, installationID: installationID}
}

// Kind 报告集成 kind。
func (c *GitHubReviewClient) Kind() string { return KindGitHub }

// staleInstallErr 安装 id 陈旧的调用面特征：401/404 或非限流 403。限流 403
// 是瞬时态，重解析只烧配额不治本，不在此列。
func staleInstallErr(err error) bool {
	if IsRateLimit(err) {
		return false
	}
	return IsUnauthorized(err) || IsNotFound(err) || IsForbidden(err)
}

// staleRetry 安装令牌调用的一次自愈：命中陈旧安装（TTL 缓存窗口内的
// App 卸装重装/仓库转组，INT-230 复审 P2）时经 reResolve 作废缓存重解析
// 并重试一次；重解析失败按原错误透出（语义与未重试一致）。写操作同样
// 安全：401/403/404 时写并未发生。
func staleRetry[T any](c *GitHubReviewClient, ctx context.Context, fn func(installationID int64) (T, error)) (T, error) {
	v, err := fn(c.installationID)
	if err == nil || c.reResolve == nil || !staleInstallErr(err) {
		return v, err
	}
	id, rerr := c.reResolve(ctx)
	if rerr != nil {
		return v, err
	}
	c.installationID = id
	return fn(id)
}

// staleRetry2 同 staleRetry，服务双返回值的方法面。
func staleRetry2[T any, U any](c *GitHubReviewClient, ctx context.Context, fn func(installationID int64) (T, U, error)) (T, U, error) {
	v, u, err := fn(c.installationID)
	if err == nil || c.reResolve == nil || !staleInstallErr(err) {
		return v, u, err
	}
	id, rerr := c.reResolve(ctx)
	if rerr != nil {
		return v, u, err
	}
	c.installationID = id
	return fn(id)
}

func (c *GitHubReviewClient) GetPullRequest(ctx context.Context, repoFullName string, number int) (*PullRequest, error) {
	pr, err := staleRetry(c, ctx, func(id int64) (*githubapi.PullRequest, error) {
		return c.api.GetPullRequest(ctx, id, repoFullName, number)
	})
	if err != nil {
		return nil, err
	}
	return PullRequestFromGitHub(pr), nil
}

func (c *GitHubReviewClient) GetPullRequestDiff(ctx context.Context, repoFullName string, number int) (string, error) {
	diff, err := staleRetry(c, ctx, func(id int64) (string, error) {
		return c.api.GetPullRequestDiff(ctx, id, repoFullName, number)
	})
	if errors.Is(err, githubapi.ErrBodyTooLarge) {
		// 本端截断探测与 Forgejo 适配器同一哨兵口径：service 只认
		// IsDiffTooLarge，不区分两种来源；前缀原样透传。
		return diff, fmt.Errorf("%w: github: %s #%d 的 diff 超过本端读入上限", ErrDiffTooLarge, repoFullName, number)
	}
	return diff, err
}

func (c *GitHubReviewClient) ListPullRequestFiles(ctx context.Context, repoFullName string, number int) ([]string, bool, error) {
	return staleRetry2(c, ctx, func(id int64) ([]string, bool, error) {
		return c.api.ListPullRequestFiles(ctx, id, repoFullName, number)
	})
}

func (c *GitHubReviewClient) ListReviewComments(ctx context.Context, repoFullName string, number int) ([]ReviewComment, error) {
	list, err := staleRetry(c, ctx, func(id int64) ([]githubapi.ReviewComment, error) {
		return c.api.ListReviewComments(ctx, id, repoFullName, number)
	})
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
	list, err := staleRetry(c, ctx, func(id int64) ([]githubapi.IssueComment, error) {
		return c.api.ListIssueCommentsForPR(ctx, id, repoFullName, number)
	})
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
	return staleRetry2(c, ctx, func(id int64) (int64, bool, error) {
		return c.api.FindPullRequestComment(ctx, id, repoFullName, number, marker)
	})
}

func (c *GitHubReviewClient) CreatePullRequestComment(ctx context.Context, repoFullName string, number int, body string) (int64, error) {
	return staleRetry(c, ctx, func(id int64) (int64, error) {
		return c.api.CreatePullRequestComment(ctx, id, repoFullName, number, body)
	})
}

func (c *GitHubReviewClient) ListReviews(ctx context.Context, repoFullName string, number int) ([]ReviewSummary, error) {
	list, err := staleRetry(c, ctx, func(id int64) ([]githubapi.ReviewSummary, error) {
		return c.api.ListReviews(ctx, id, repoFullName, number)
	})
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
	return staleRetry(c, ctx, func(id int64) (int64, error) {
		return c.api.SubmitReview(ctx, id, repoFullName, number, githubapi.ReviewInput{
			Event: in.Event, Body: in.Body, CommitID: in.CommitID, Comments: comments,
		})
	})
}

func (c *GitHubReviewClient) CreateCommitStatus(ctx context.Context, repoFullName, sha, state, statusContext, description, targetURL string) error {
	_, err := staleRetry(c, ctx, func(id int64) (bool, error) {
		return true, c.api.CreateCommitStatus(ctx, id, repoFullName, sha, state, statusContext, description, targetURL)
	})
	return err
}

func (c *GitHubReviewClient) GetRepository(ctx context.Context, repoFullName string) error {
	_, err := staleRetry(c, ctx, func(id int64) (bool, error) {
		return true, c.api.GetRepository(ctx, id, repoFullName)
	})
	return err
}

func (c *GitHubReviewClient) TreeEntriesAtRef(ctx context.Context, repoFullName, dir, ref string) (map[string]bool, error) {
	entries, err := staleRetry(c, ctx, func(id int64) (map[string]bool, error) {
		return c.api.TreeEntriesAtRef(ctx, id, repoFullName, dir, ref)
	})
	if errors.Is(err, githubapi.ErrTreeTruncated) {
		return nil, fmt.Errorf("%w: %s@%s", ErrTreeTruncated, dir, ref)
	}
	return entries, err
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

// BearerScope 凭证缓存作用域：按安装 id 分桶，私有附件缓存条目只对同一
// 安装命中（见 githubmedia cacheEntry.Scope）。
func (c *GitHubReviewClient) BearerScope() string {
	return fmt.Sprintf("install:%d", c.installationID)
}
