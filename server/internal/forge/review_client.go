package forge

import "context"

// 集成 kind：适配器报自己的集成身份，供日志与断言。
const (
	KindGitHub  = "github"
	KindForgejo = "forgejo"
)

// ReviewClient 审查链路对代码托管平台的全部访问面（INT-167 泛化）：githubapi
// .GitHubClient 首位的 installationID 由适配器构造时收编，方法面只描述平台
// 无关的审查语义。ResolveInstallation 不在本接口——App 安装解析是 GitHub
// 专属 seam，留在 githubapi/githubapp 侧；rerun 等需要安装 id 的路径经解析
// 后把值传进 resolver。GetRepository 的 installationID=0「先解析安装再查仓
// 库」语义由 GitHub 适配器原样透传保底。
type ReviewClient interface {
	// 元数据
	GetPullRequest(ctx context.Context, repoFullName string, number int) (*PullRequest, error)
	// GetPullRequestDiff err 为 ErrDiffTooLarge 时返回值是尽量读到的前缀
	// （可能为空）——前缀仍是真实内容，调用方按不完整件处置。
	GetPullRequestDiff(ctx context.Context, repoFullName string, number int) (string, error)
	// ListPullRequestFiles PR 变更文件清单（rename 旧名+新名都含，去重）：
	// 分页端点的权威证据——平台对超大 PR 的 diff 文本可能无标记截断，
	// 否决类判定不得用 diff 文本。complete=false 表示清单被平台封顶
	// （如 GitHub 3000 文件上限），缺席不能当不存在证据。
	ListPullRequestFiles(ctx context.Context, repoFullName string, number int) (paths []string, complete bool, err error)
	ListReviewComments(ctx context.Context, repoFullName string, number int) ([]ReviewComment, error)
	// ListIssueCommentsForPR PR 会话评论（review 行内之外的顶层讨论，INT-135
	// 历史上下文用）。
	ListIssueCommentsForPR(ctx context.Context, repoFullName string, number int) ([]IssueComment, error)
	// 机器人顶层评论（每 PR 一条）
	FindPullRequestComment(ctx context.Context, repoFullName string, number int, marker string) (commentID int64, found bool, err error)
	CreatePullRequestComment(ctx context.Context, repoFullName string, number int, body string) (commentID int64, err error)
	UpdatePullRequestComment(ctx context.Context, repoFullName string, commentID int64, body string) error
	// review
	// ListReviews 列出 PR 上已提交的 review（对账用）。
	ListReviews(ctx context.Context, repoFullName string, number int) ([]ReviewSummary, error)
	// SubmitReview 返回创建的 review id（事件 COMMENT 时才有行内评论落点）。
	SubmitReview(ctx context.Context, repoFullName string, number int, in ReviewInput) (reviewID int64, err error)
	// commit status：state ∈ error/failure/pending/success，statusContext 是
	// 固定检查名（代码审查/视觉审查）。
	CreateCommitStatus(ctx context.Context, repoFullName, sha, state, statusContext, description, targetURL string) error
	// 仓库级检查：nil 表示可访问；403/404 经 *StatusError 返回。
	GetRepository(ctx context.Context, repoFullName string) error
	// PathExistsAtRef 仓库相对路径在指定 ref（commit SHA 等）的树中是否存在
	// （INT-169 文件归属校验：审查 findings 的 file 必须落在本轮 head 树里）。
	// 非规范路径（绝对路径、空段、.、..）按不存在处理而非规范化。
	PathExistsAtRef(ctx context.Context, repoFullName, path, ref string) (bool, error)
	// Kind 报告适配器的集成 kind（github/forgejo）。
	Kind() string
}

// BearerSource 审查客户端可选暴露的媒体下载 bearer：私有仓库附件下载用
// （githubapp.Client 持有安装令牌；无凭证实现返回空串 = 匿名下载）。经类型
// 断言取用，不进 ReviewClient 方法面。
type BearerSource interface {
	Bearer(ctx context.Context) (string, error)
}
