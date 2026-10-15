package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PullRequest 是评审链路需要的 PR 元数据子集。
type PullRequest struct {
	RepoFullName string
	Number       int
	Title        string
	Body         string
	HTMLURL      string
	HeadSHA      string
	HeadRef      string
	BaseRef      string
	Draft        bool
}

// ReviewComment 是 PR 的 review 评论（行内评论，不含 issue 评论）。
type ReviewComment struct {
	ID          int64
	User        string
	Body        string
	InReplyToID *int64
	Path        string
	CreatedAt   time.Time
}

// ReviewInput 一次 review 提交。Event ∈ COMMENT | APPROVE | REQUEST_CHANGES；
// CommitID 由调用方取运行绑定的 head SHA。
type ReviewInput struct {
	Event    string
	Body     string
	CommitID string
}

// CheckState check run 的运行态；Completed 时看 Conclusion。
type CheckState string

const (
	CheckQueued     CheckState = "queued"
	CheckInProgress CheckState = "in_progress"
	CheckCompleted  CheckState = "completed"
)

// CheckConclusion 终态结论（GitHub 官方枚举子集）。
type CheckConclusion string

const (
	ConclusionSuccess   CheckConclusion = "success"
	ConclusionFailure   CheckConclusion = "failure"
	ConclusionCancelled CheckConclusion = "cancelled"
)

// CheckUpdate 一次 check run 更新的全部字段；Status=Completed 时必须给 Conclusion。
type CheckUpdate struct {
	Status     CheckState
	Conclusion CheckConclusion
	// Title 同时作为 check run 名称与 output 标题。
	Title   string
	Summary string
	Text    string
}

// PullRequestGitHub PR 评审链路对 GitHub 的全部访问面。实现：client.go（安装
// 令牌真实调用）与 fake.go（内存假实现，记录调用供断言）。
type PullRequestGitHub interface {
	// 元数据
	GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*PullRequest, error)
	GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error)
	ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewComment, error)
	// 机器人顶层评论（每 PR 一条）
	FindIssueCommentByMarker(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (commentID int64, found bool, err error)
	CreateIssueComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (commentID int64, err error)
	UpdateIssueComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error
	// check run
	CreateCheckRun(ctx context.Context, installationID int64, repoFullName, name, headSHA, externalID string) (checkRunID int64, err error)
	UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in CheckUpdate) error
	// review
	SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) error
}

// ErrForbidden GitHub 返回 403（如 Checks API 未授权）：调用方据此退回合并 check；
// 合并 check 同样 403 则跳过 check、仍同步评论。
var ErrForbidden = errors.New("githubapp: forbidden")

// StatusError 非 2xx 响应。
type StatusError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("githubapp: %s %s -> %d: %s", e.Method, e.URL, e.StatusCode, truncateBody(e.Body))
}

// IsForbidden 判断 err 是否 403（直接或包在链里）。
func IsForbidden(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode == http.StatusForbidden
	}
	return errors.Is(err, ErrForbidden)
}

// IsNotFound 判断 err 是否 404（评论被删后 PATCH 失败，回退到按标记找回）。
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

func truncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// splitRepo "owner/name" → 两段；不合法返回错误。
func splitRepo(repoFullName string) (string, string, error) {
	owner, name, ok := strings.Cut(repoFullName, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("githubapp: 仓库名须为 owner/name，得到 %q", repoFullName)
	}
	return owner, name, nil
}
