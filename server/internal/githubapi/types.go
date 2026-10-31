package githubapi

import (
	"encoding/json"
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
	// HeadRepoFullName head 仓库全名（pulls/{n} 的 head.repo.full_name）。
	// 跨仓 fork 守卫用：head.repo 为 null（已删 fork）时为空串——与 base
	// 仓库名不同名或缺失都不得开轮审查。
	HeadRepoFullName string
	// UpdatedAt pulls/{n} 响应的 updated_at——webhook 复核路径用它落
	// seen/总账的 pr_updated_at 基准（Forgejo 适配器同源字段）。
	UpdatedAt *time.Time
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

// ErrBodyTooLarge 2xx 响应体超本端读取上限。截断的响应不能按完整件处理
// （静默截断的 diff 会被误当成完整变更清单），超过上限确定性报错。
var ErrBodyTooLarge = errors.New("githubapi: 响应体超过读取上限")

// ErrTreeTruncated git/trees 响应被平台截断（truncated=true，如 >10 万条目
// 的目录）：条目清单不完整，「查不到」不等于「不存在」——按存在性不可判定
// 上报，调用方不可将其当缺席证明。
var ErrTreeTruncated = errors.New("githubapi: tree 响应被截断")

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

// IsDiffTooLarge 判断 err 是否是 GitHub 对「PR diff 超出上限」的确定性拒绝：
// 4xx（实测 406；413/422 防御性兼容）且响应体 errors[] 里有 code = "too_large"
// （field 为 diff 或缺失）。GitHub 把 406 列为通用的 "Unacceptable"，而
// too_large 是 422 系的结构化 validation code，所以只认 code——单看状态码可能
// 把别的原因当成不可重试，那会把行内意见静默丢掉；解析不出 too_large 时按可
// 重试处理。field 存在且不是 diff（例如 files 端点的同名 code）不归此类：本
// 函数只服务于 PR diff 读取，复用到其它端点前先定好语义。5xx 不在此列：那是
// 服务端暂时态，重试有意义。
func IsDiffTooLarge(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.StatusCode {
	case http.StatusNotAcceptable, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
	default:
		return false
	}
	var body struct {
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal([]byte(se.Body), &body) != nil {
		return false
	}
	for _, e := range body.Errors {
		if strings.TrimSpace(e.Field) != "" && !strings.EqualFold(strings.TrimSpace(e.Field), "diff") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(e.Code), "too_large") {
			return true
		}
	}
	return false
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
