package githubapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PullRequest PR 元数据（评审链路与 issue 关联行共用的子集；字段从 GitHub
// pulls/{n} 响应映射）。RepoFullName 取 base 仓库的 canonical 名。
type PullRequest struct {
	RepoFullName string
	Number       int
	Title        string
	Body         string
	HTMLURL      string
	State        string
	Draft        bool
	Merged       bool
	MergedAt     *time.Time
	ClosedAt     *time.Time
	HeadSHA      string
	HeadRef      string
	BaseRef      string
	AuthorLogin  string
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

// IssueComment 是 PR 会话页的顶层评论（含机器人与人工，按时间正序过滤由调用方负责）。
type IssueComment struct {
	ID        int64
	User      string
	Body      string
	CreatedAt time.Time
}

// ReviewInlineComment 随 review 一起提交的行内评论（GitHub reviews.comments[]）。
type ReviewInlineComment struct {
	Path string
	Line int
	// Side 评论所在侧；行内审查只用 RIGHT（当前 HEAD 右侧行号）。
	Side string
	Body string
}

// ReviewSummary 已提交的 PR review（用于对账：POST 成功但响应丢失时，
// 下一次同步能认出这条 review 已经存在）。
type ReviewSummary struct {
	ID        int64
	CommitID  string
	Body      string
	State     string
	CreatedAt time.Time
}

// ReviewInput 一次 review 提交。Event ∈ COMMENT | APPROVE | REQUEST_CHANGES；
// CommitID 由调用方取运行绑定的 head SHA。Comments 为行内评论列表。
type ReviewInput struct {
	Event    string
	Body     string
	CommitID string
	Comments []ReviewInlineComment
}

// ErrForbidden GitHub 返回 403（App 未获该操作权限，例如缺 statuses:write）。
var ErrForbidden = errors.New("githubapi: forbidden")

// StatusError 非 2xx 响应。
type StatusError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("githubapi: %s %s -> %d: %s", e.Method, e.URL, e.StatusCode, truncateBody(e.Body))
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

// isStatus 判断 err 链里是否带指定状态码的 *StatusError。
func isStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == code
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
		return "", "", fmt.Errorf("githubapi: 仓库名须为 owner/name，得到 %q", repoFullName)
	}
	return owner, name, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
