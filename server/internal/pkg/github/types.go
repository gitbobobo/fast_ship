package github

import (
	"encoding/json"
	"time"
)

// 本文件是 Client 接口的领域类型：TokenClient 直接把 GitHub REST 响应解码
// 进来，service 层不再接触 go-github 类型。字段与 GitHub API 载荷同名
//（snake_case），指针字段区分「远端缺省」与零值；读取统一走 nil 安全
// getter。只保留 service 实际读取的字段，不是 GitHub 响应的完整映射。

// User 是 issue/评论/时间线参与者的最小投影：镜像只存 login 与头像。
type User struct {
	Login     *string `json:"login,omitempty"`
	AvatarURL *string `json:"avatar_url,omitempty"`
}

func (u *User) GetLogin() string {
	if u == nil || u.Login == nil {
		return ""
	}
	return *u.Login
}

func (u *User) GetAvatarURL() string {
	if u == nil || u.AvatarURL == nil {
		return ""
	}
	return *u.AvatarURL
}

// Label 同时覆盖仓库标签（ListRepositoryLabels）与 issue/时间线上的标签。
type Label struct {
	Name        *string `json:"name,omitempty"`
	Color       *string `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (l *Label) GetName() string {
	if l == nil || l.Name == nil {
		return ""
	}
	return *l.Name
}

func (l *Label) GetColor() string {
	if l == nil || l.Color == nil {
		return ""
	}
	return *l.Color
}

func (l *Label) GetDescription() string {
	if l == nil || l.Description == nil {
		return ""
	}
	return *l.Description
}

// Milestone 覆盖 issue 与时间线事件上的里程碑。
type Milestone struct {
	Number      *int    `json:"number,omitempty"`
	Title       *string `json:"title,omitempty"`
	State       *string `json:"state,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (m *Milestone) GetNumber() int {
	if m == nil || m.Number == nil {
		return 0
	}
	return *m.Number
}

func (m *Milestone) GetTitle() string {
	if m == nil || m.Title == nil {
		return ""
	}
	return *m.Title
}

func (m *Milestone) GetState() string {
	if m == nil || m.State == nil {
		return ""
	}
	return *m.State
}

func (m *Milestone) GetDescription() string {
	if m == nil || m.Description == nil {
		return ""
	}
	return *m.Description
}

// Reactions 是 issue/评论的表态汇总。
type Reactions struct {
	TotalCount *int `json:"total_count,omitempty"`
	PlusOne    *int `json:"+1,omitempty"`
	MinusOne   *int `json:"-1,omitempty"`
	Laugh      *int `json:"laugh,omitempty"`
	Hooray     *int `json:"hooray,omitempty"`
	Confused   *int `json:"confused,omitempty"`
	Heart      *int `json:"heart,omitempty"`
	Rocket     *int `json:"rocket,omitempty"`
	Eyes       *int `json:"eyes,omitempty"`
}

func (r *Reactions) GetTotalCount() int {
	if r == nil || r.TotalCount == nil {
		return 0
	}
	return *r.TotalCount
}

func (r *Reactions) GetPlusOne() int {
	if r == nil || r.PlusOne == nil {
		return 0
	}
	return *r.PlusOne
}

func (r *Reactions) GetMinusOne() int {
	if r == nil || r.MinusOne == nil {
		return 0
	}
	return *r.MinusOne
}

func (r *Reactions) GetLaugh() int {
	if r == nil || r.Laugh == nil {
		return 0
	}
	return *r.Laugh
}

func (r *Reactions) GetHooray() int {
	if r == nil || r.Hooray == nil {
		return 0
	}
	return *r.Hooray
}

func (r *Reactions) GetConfused() int {
	if r == nil || r.Confused == nil {
		return 0
	}
	return *r.Confused
}

func (r *Reactions) GetHeart() int {
	if r == nil || r.Heart == nil {
		return 0
	}
	return *r.Heart
}

func (r *Reactions) GetRocket() int {
	if r == nil || r.Rocket == nil {
		return 0
	}
	return *r.Rocket
}

func (r *Reactions) GetEyes() int {
	if r == nil || r.Eyes == nil {
		return 0
	}
	return *r.Eyes
}

// Rename 是 renamed 时间线事件的标题变更。
type Rename struct {
	From *string `json:"from,omitempty"`
	To   *string `json:"to,omitempty"`
}

func (r *Rename) GetFrom() string {
	if r == nil || r.From == nil {
		return ""
	}
	return *r.From
}

func (r *Rename) GetTo() string {
	if r == nil || r.To == nil {
		return ""
	}
	return *r.To
}

// TimelineSource 是 cross-referenced 时间线事件的来源；镜像只读来源 issue 号。
type TimelineSource struct {
	Issue *Issue `json:"issue,omitempty"`
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

// Issue 覆盖 service 现用的全部 GitHub Issue 字段。PullRequestLinks 是
// issues 列表中 PR 条目的标记字段（GitHub 把 PR 混进 issues 端点，PR 条目
// 带 pull_request 对象）：镜像只判断它在不在以跳过 PR，不读内容。
type Issue struct {
	ID                *int64           `json:"id,omitempty"`
	NodeID            *string          `json:"node_id,omitempty"`
	Number            *int             `json:"number,omitempty"`
	State             *string          `json:"state,omitempty"`
	StateReason       *string          `json:"state_reason,omitempty"`
	Title             *string          `json:"title,omitempty"`
	Body              *string          `json:"body,omitempty"`
	BodyHTML          *string          `json:"body_html,omitempty"`
	User              *User            `json:"user,omitempty"`
	Assignees         []*User          `json:"assignees,omitempty"`
	Labels            []*Label         `json:"labels,omitempty"`
	Milestone         *Milestone       `json:"milestone,omitempty"`
	Reactions         *Reactions       `json:"reactions,omitempty"`
	Comments          *int             `json:"comments,omitempty"`
	Locked            *bool            `json:"locked,omitempty"`
	ActiveLockReason  *string          `json:"active_lock_reason,omitempty"`
	AuthorAssociation *string          `json:"author_association,omitempty"`
	HTMLURL           *string          `json:"html_url,omitempty"`
	CreatedAt         *time.Time       `json:"created_at,omitempty"`
	UpdatedAt         *time.Time       `json:"updated_at,omitempty"`
	ClosedAt          *time.Time       `json:"closed_at,omitempty"`
	PullRequestLinks  *json.RawMessage `json:"pull_request,omitempty"`
	// Raw 保存解码时传入的完整原始载荷（不参与序列化）：镜像的取证列
	// RawJSON 与前端递归搜 html_url 的深链接依赖未映射字段。
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON 先把入参字节存进 Raw 再按别名类型解码字段：list、单条与
// 嵌套（TimelineSource.Issue）各解码路径都自动带上原始载荷。别名类型不带
// 本方法，不会递归回自身。
func (i *Issue) UnmarshalJSON(data []byte) error {
	i.Raw = append(i.Raw[:0], data...)
	type issueAlias Issue
	return json.Unmarshal(data, (*issueAlias)(i))
}

func (i *Issue) GetID() int64 {
	if i == nil || i.ID == nil {
		return 0
	}
	return *i.ID
}

func (i *Issue) GetNodeID() string {
	if i == nil || i.NodeID == nil {
		return ""
	}
	return *i.NodeID
}

func (i *Issue) GetNumber() int {
	if i == nil || i.Number == nil {
		return 0
	}
	return *i.Number
}

func (i *Issue) GetState() string {
	if i == nil || i.State == nil {
		return ""
	}
	return *i.State
}

func (i *Issue) GetStateReason() string {
	if i == nil || i.StateReason == nil {
		return ""
	}
	return *i.StateReason
}

func (i *Issue) GetTitle() string {
	if i == nil || i.Title == nil {
		return ""
	}
	return *i.Title
}

func (i *Issue) GetBody() string {
	if i == nil || i.Body == nil {
		return ""
	}
	return *i.Body
}

func (i *Issue) GetBodyHTML() string {
	if i == nil || i.BodyHTML == nil {
		return ""
	}
	return *i.BodyHTML
}

func (i *Issue) GetUser() *User {
	if i == nil {
		return nil
	}
	return i.User
}

func (i *Issue) GetComments() int {
	if i == nil || i.Comments == nil {
		return 0
	}
	return *i.Comments
}

func (i *Issue) GetLocked() bool {
	if i == nil || i.Locked == nil {
		return false
	}
	return *i.Locked
}

func (i *Issue) GetActiveLockReason() string {
	if i == nil || i.ActiveLockReason == nil {
		return ""
	}
	return *i.ActiveLockReason
}

func (i *Issue) GetAuthorAssociation() string {
	if i == nil || i.AuthorAssociation == nil {
		return ""
	}
	return *i.AuthorAssociation
}

func (i *Issue) GetHTMLURL() string {
	if i == nil || i.HTMLURL == nil {
		return ""
	}
	return *i.HTMLURL
}

func (i *Issue) GetCreatedAt() time.Time {
	if i == nil || i.CreatedAt == nil {
		return time.Time{}
	}
	return *i.CreatedAt
}

func (i *Issue) GetUpdatedAt() time.Time {
	if i == nil || i.UpdatedAt == nil {
		return time.Time{}
	}
	return *i.UpdatedAt
}

func (i *Issue) GetClosedAt() time.Time {
	if i == nil || i.ClosedAt == nil {
		return time.Time{}
	}
	return *i.ClosedAt
}

// IsPullRequest 判定该条目是否为混入 issues 端点的 PR（pull_request 键存在）。
func (i *Issue) IsPullRequest() bool {
	return i != nil && i.PullRequestLinks != nil
}

// IssueComment 覆盖 service 现用的全部 GitHub Issue 评论字段。
type IssueComment struct {
	ID                *int64          `json:"id,omitempty"`
	NodeID            *string         `json:"node_id,omitempty"`
	Body              *string         `json:"body,omitempty"`
	BodyHTML          *string         `json:"body_html,omitempty"`
	HTMLURL           *string         `json:"html_url,omitempty"`
	User              *User           `json:"user,omitempty"`
	AuthorAssociation *string         `json:"author_association,omitempty"`
	Reactions         *Reactions      `json:"reactions,omitempty"`
	CreatedAt         *time.Time      `json:"created_at,omitempty"`
	UpdatedAt         *time.Time      `json:"updated_at,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

// UnmarshalJSON 同 Issue：保留完整原始载荷供镜像取证列写回。
func (c *IssueComment) UnmarshalJSON(data []byte) error {
	c.Raw = append(c.Raw[:0], data...)
	type issueCommentAlias IssueComment
	return json.Unmarshal(data, (*issueCommentAlias)(c))
}

func (c *IssueComment) GetID() int64 {
	if c == nil || c.ID == nil {
		return 0
	}
	return *c.ID
}

func (c *IssueComment) GetNodeID() string {
	if c == nil || c.NodeID == nil {
		return ""
	}
	return *c.NodeID
}

func (c *IssueComment) GetBody() string {
	if c == nil || c.Body == nil {
		return ""
	}
	return *c.Body
}

func (c *IssueComment) GetBodyHTML() string {
	if c == nil || c.BodyHTML == nil {
		return ""
	}
	return *c.BodyHTML
}

func (c *IssueComment) GetHTMLURL() string {
	if c == nil || c.HTMLURL == nil {
		return ""
	}
	return *c.HTMLURL
}

func (c *IssueComment) GetUser() *User {
	if c == nil {
		return nil
	}
	return c.User
}

func (c *IssueComment) GetAuthorAssociation() string {
	if c == nil || c.AuthorAssociation == nil {
		return ""
	}
	return *c.AuthorAssociation
}

func (c *IssueComment) GetCreatedAt() time.Time {
	if c == nil || c.CreatedAt == nil {
		return time.Time{}
	}
	return *c.CreatedAt
}

func (c *IssueComment) GetUpdatedAt() time.Time {
	if c == nil || c.UpdatedAt == nil {
		return time.Time{}
	}
	return *c.UpdatedAt
}

// TimelineEvent 覆盖 service 现用的全部时间线字段。
type TimelineEvent struct {
	ID        *int64             `json:"id,omitempty"`
	Event     *string            `json:"event,omitempty"`
	Actor     *User              `json:"actor,omitempty"`
	User      *User              `json:"user,omitempty"`
	Body      *string            `json:"body,omitempty"`
	CreatedAt *time.Time         `json:"created_at,omitempty"`
	Label     *Label             `json:"label,omitempty"`
	Assignee  *User              `json:"assignee,omitempty"`
	Milestone *Milestone         `json:"milestone,omitempty"`
	Rename    *Rename            `json:"rename,omitempty"`
	Source    *TimelineSource    `json:"source,omitempty"`
	CommitID  *string            `json:"commit_id,omitempty"`
	IssueType *TimelineIssueType `json:"issue_type,omitempty"`
	Raw       json.RawMessage    `json:"-"`
}

// UnmarshalJSON 同 Issue：时间线事件 PayloadJSON 要存完整原始载荷，前端靠
// 其中的 html_url 生成「复制 GitHub 深链接」。
func (t *TimelineEvent) UnmarshalJSON(data []byte) error {
	t.Raw = append(t.Raw[:0], data...)
	type timelineEventAlias TimelineEvent
	return json.Unmarshal(data, (*timelineEventAlias)(t))
}

func (t *TimelineEvent) GetID() int64 {
	if t == nil || t.ID == nil {
		return 0
	}
	return *t.ID
}

func (t *TimelineEvent) GetEvent() string {
	if t == nil || t.Event == nil {
		return ""
	}
	return *t.Event
}

func (t *TimelineEvent) GetActor() *User {
	if t == nil {
		return nil
	}
	return t.Actor
}

func (t *TimelineEvent) GetUser() *User {
	if t == nil {
		return nil
	}
	return t.User
}

func (t *TimelineEvent) GetBody() string {
	if t == nil || t.Body == nil {
		return ""
	}
	return *t.Body
}

func (t *TimelineEvent) GetCreatedAt() time.Time {
	if t == nil || t.CreatedAt == nil {
		return time.Time{}
	}
	return *t.CreatedAt
}

func (t *TimelineEvent) GetLabel() *Label {
	if t == nil {
		return nil
	}
	return t.Label
}

func (t *TimelineEvent) GetAssignee() *User {
	if t == nil {
		return nil
	}
	return t.Assignee
}

func (t *TimelineEvent) GetMilestone() *Milestone {
	if t == nil {
		return nil
	}
	return t.Milestone
}

func (t *TimelineEvent) GetRename() *Rename {
	if t == nil {
		return nil
	}
	return t.Rename
}

func (t *TimelineEvent) GetSource() *TimelineSource {
	if t == nil {
		return nil
	}
	return t.Source
}

func (t *TimelineEvent) GetCommitID() string {
	if t == nil || t.CommitID == nil {
		return ""
	}
	return *t.CommitID
}

func (t *TimelineEvent) GetIssueType() *TimelineIssueType {
	if t == nil {
		return nil
	}
	return t.IssueType
}

// UpdateIssueRequest 是 PATCH /repos/{owner}/{repo}/issues/{n} 的请求体。
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

// Repository 是 FetchRepository 的探测投影：批量关闭只取 has_issues 区分
// 整仓级与单条成因（INT-198）；缺省视为开启的三态口径由调用方判定。
type Repository struct {
	HasIssues *bool `json:"has_issues,omitempty"`
}

func (r *Repository) GetHasIssues() bool {
	if r == nil || r.HasIssues == nil {
		return false
	}
	return *r.HasIssues
}

// Release 是 CreateRelease 的标识投影：ship 只取 ID 传 UploadAsset、取
// HTMLURL 写回版本记录。
type Release struct {
	ID      *int64  `json:"id,omitempty"`
	HTMLURL *string `json:"html_url,omitempty"`
}

func (r *Release) GetID() int64 {
	if r == nil || r.ID == nil {
		return 0
	}
	return *r.ID
}

func (r *Release) GetHTMLURL() string {
	if r == nil || r.HTMLURL == nil {
		return ""
	}
	return *r.HTMLURL
}
