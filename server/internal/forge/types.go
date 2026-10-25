package forge

import "time"

// 审查链路的中性类型（INT-167）：字段从 githubapi 同名类型照抄，forge 包的
// 接口与调用方只认这组类型——各适配器（GitHub / Forgejo）负责把平台响应
// 转换成这里的形态，service 层不感知平台差异。

// PullRequest PR 元数据（评审链路共用的子集）。RepoFullName 取 base 仓库的
// canonical 名。
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
	// UpdatedAt 平台侧的 PR 最后更新时间（Forgejo updated_at）：轮询补漏的
	// 过期判定靠它给「读到快照后平台又推了新 head」的乱序兜底；GitHub
	// 适配器不填（webhook 自带 updated_at，没有走对象判定的路径）。
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
	// Self 该评论是本系统机器人账号所发：GitHub 靠 login 的 [bot] 后缀，
	// Forgejo 机器人是普通账号，由适配器按 PAT 所属 login 标出——历史
	// 上下文组装的「过滤自己的发言」按它判定，不依赖平台特定命名。
	Self bool
}

// IssueComment 是 PR 会话页的顶层评论（含机器人与人工，按时间正序过滤由调用方负责）。
type IssueComment struct {
	ID        int64
	User      string
	Body      string
	CreatedAt time.Time
	// Self 同 ReviewComment.Self。
	Self bool
}

// ReviewInlineComment 随 review 一起提交的行内评论。Side 评论所在侧；行内
// 审查只用 RIGHT（当前 HEAD 右侧行号）。
type ReviewInlineComment struct {
	Path string
	Line int
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
