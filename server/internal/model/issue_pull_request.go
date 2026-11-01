package model

import (
	"fmt"
	"time"
)

type IssuePullRequestState string

const (
	IssuePullRequestStateOpen   IssuePullRequestState = "open"
	IssuePullRequestStateClosed IssuePullRequestState = "closed"
	IssuePullRequestStateMerged IssuePullRequestState = "merged"
)

type IssuePullRequestLinkOrigin string

const (
	IssuePullRequestLinkOriginSynced IssuePullRequestLinkOrigin = "synced"
	IssuePullRequestLinkOriginManual IssuePullRequestLinkOrigin = "manual"
)

const (
	IssuePullRequestProviderGitHub  = "github"
	IssuePullRequestProviderForgejo = "forgejo"
)

// IssuePullRequest 记录 Issue 与某个远端 PR 的关联及其最近一次同步到的状态。
// link_origin 区分关联来源：manual 由用户显式 attach，synced 由远端同步投影产生；
// 同步投影的清理逻辑（DeleteMissingSynced）只允许作用于 synced 行，不能清掉 manual 行。
// 允许跨仓库：repo_full_name 记录 PR 所在仓库，不要求等于项目配置的仓库。
// 业务身份是 (issue_id, provider, host_key, repo_full_name, number)：不同 Forgejo
// 实例的同名 PR 作为独立关联并存；github 行的 host_key 恒空串（github.com 唯一）。
// host_key 是主机快照（forges.HostKey 口径）：集成改 base_url 后，行仍指向原主机，
// sync 时与集成现行 host_key 比对，不一致报错要求重新关联，不会被静默改指。
type IssuePullRequest struct {
	ID           string                     `gorm:"type:text;primaryKey" json:"id"`
	IssueID      string                     `gorm:"type:text;not null;index" json:"issue_id"`
	ProjectID    string                     `gorm:"type:text;not null;index" json:"project_id"`
	Provider     string                     `gorm:"type:text;not null;default:github" json:"provider"`
	HostKey      string                     `gorm:"type:text;not null;default:''" json:"-"`
	RepoFullName string                     `gorm:"type:text;not null" json:"repo_full_name"`
	Number       int                        `gorm:"not null" json:"number"`
	HTMLURL      string                     `gorm:"type:text" json:"html_url"`
	Title        string                     `gorm:"type:text" json:"title"`
	State        IssuePullRequestState      `gorm:"type:text;not null;default:open" json:"state"`
	IsDraft      bool                       `gorm:"not null;default:false" json:"is_draft"`
	AuthorLogin  string                     `gorm:"type:text" json:"author_login"`
	HeadRef      string                     `gorm:"type:text" json:"head_ref"`
	BaseRef      string                     `gorm:"type:text" json:"base_ref"`
	MergedAt     *time.Time                 `json:"merged_at"`
	ClosedAt     *time.Time                 `json:"closed_at"`
	LinkOrigin   IssuePullRequestLinkOrigin `gorm:"type:text;not null;default:manual" json:"link_origin"`
	// ForgeIntegrationID provider=forgejo 行的集成快照：repo_full_name 只有
	// owner/repo 没有 host，sync 按它重建该行的 Forgejo 客户端；github 行恒
	// 空串。列可空（AutoMigrate 追加）。
	ForgeIntegrationID string    `gorm:"type:text" json:"-"`
	SyncedAt           time.Time `gorm:"not null" json:"synced_at"`
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssuePullRequest) TableName() string {
	return "issue_pull_requests"
}

// SyncKey 是同步投影的去重键，与 repository 层 (provider, host_key, repo_full_name, number)
// 唯一约束一一对应。
func (l IssuePullRequest) SyncKey() string {
	return fmt.Sprintf("%s|%s|%s|%d", l.Provider, l.HostKey, l.RepoFullName, l.Number)
}
