package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
)

// stubGitHubClient 未实现方法的兜底：各测试 fake 内嵌它补齐 ghclient.Client 与
// githubapi.GitHubClient 的其余方法，误调用即失败并报出方法名。
type stubGitHubClient struct{}

func stubErr(method string) error {
	return fmt.Errorf("stubGitHubClient: %s 未实现", method)
}

func (stubGitHubClient) ValidateRepository(context.Context) error {
	return stubErr("ValidateRepository")
}

func (stubGitHubClient) ListIssues(context.Context, string, *time.Time, int, int) ([]*ghclient.Issue, *gh.Response, error) {
	return nil, nil, stubErr("ListIssues")
}

func (stubGitHubClient) GetIssue(context.Context, int) (*ghclient.Issue, error) {
	return nil, stubErr("GetIssue")
}

func (stubGitHubClient) ListRepositoryLabels(context.Context, int, int) ([]*gh.Label, *gh.Response, error) {
	return nil, nil, stubErr("ListRepositoryLabels")
}

func (stubGitHubClient) ListIssueComments(context.Context, int, int, int) ([]*ghclient.IssueComment, *gh.Response, error) {
	return nil, nil, stubErr("ListIssueComments")
}

func (stubGitHubClient) ListIssueTimeline(context.Context, int, int, int) ([]*ghclient.TimelineEvent, *gh.Response, error) {
	return nil, nil, stubErr("ListIssueTimeline")
}

func (stubGitHubClient) CreateIssueComment(context.Context, int, string) (*ghclient.IssueComment, error) {
	return nil, stubErr("CreateIssueComment")
}

func (stubGitHubClient) UpdateIssue(context.Context, int, ghclient.UpdateIssueRequest) (*ghclient.Issue, error) {
	return nil, stubErr("UpdateIssue")
}

func (stubGitHubClient) CreateIssue(context.Context, string, string) (*ghclient.Issue, error) {
	return nil, stubErr("CreateIssue")
}

func (stubGitHubClient) CreateTag(context.Context, string, string) error {
	return stubErr("CreateTag")
}

func (stubGitHubClient) CreateRelease(context.Context, string, string, string) (*gh.RepositoryRelease, error) {
	return nil, stubErr("CreateRelease")
}

func (stubGitHubClient) UploadAsset(context.Context, int64, string, *os.File) error {
	return stubErr("UploadAsset")
}

func (stubGitHubClient) ListBranches(context.Context) ([]*ghclient.Branch, string, error) {
	return nil, "", stubErr("ListBranches")
}

func (stubGitHubClient) GetPullRequest(context.Context, int64, string, int) (*githubapi.PullRequest, error) {
	return nil, stubErr("GetPullRequest")
}

func (stubGitHubClient) GetPullRequestDiff(context.Context, int64, string, int) (string, error) {
	return "", stubErr("GetPullRequestDiff")
}

func (stubGitHubClient) ListReviewComments(context.Context, int64, string, int) ([]githubapi.ReviewComment, error) {
	return nil, stubErr("ListReviewComments")
}

func (stubGitHubClient) FindPullRequestComment(context.Context, int64, string, int, string) (int64, bool, error) {
	return 0, false, stubErr("FindPullRequestComment")
}

func (stubGitHubClient) CreatePullRequestComment(context.Context, int64, string, int, string) (int64, error) {
	return 0, stubErr("CreatePullRequestComment")
}

func (stubGitHubClient) UpdatePullRequestComment(context.Context, int64, string, int64, string) error {
	return stubErr("UpdatePullRequestComment")
}

func (stubGitHubClient) SubmitReview(context.Context, int64, string, int, githubapi.ReviewInput) (int64, error) {
	return 0, stubErr("SubmitReview")
}

func (stubGitHubClient) ListReviews(context.Context, int64, string, int) ([]githubapi.ReviewSummary, error) {
	return nil, stubErr("ListReviews")
}

func (stubGitHubClient) CreateCommitStatus(context.Context, int64, string, string, string, string, string, string) error {
	return stubErr("CreateCommitStatus")
}

func (stubGitHubClient) GetRepository(context.Context, int64, string) error {
	return stubErr("GetRepository")
}

func (stubGitHubClient) ResolveInstallation(context.Context, string) (int64, error) {
	return 0, stubErr("ResolveInstallation")
}

func (stubGitHubClient) ListIssueCommentsForPR(context.Context, int64, string, int) ([]githubapi.IssueComment, error) {
	return nil, stubErr("ListIssueCommentsForPR")
}

// 编译期钉住 stub 自身满足两个 seam 接口。
var (
	_ ghclient.Client        = stubGitHubClient{}
	_ githubapi.GitHubClient = stubGitHubClient{}
)
