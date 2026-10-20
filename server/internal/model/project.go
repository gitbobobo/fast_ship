package model

import "time"

type Project struct {
	ID          string `gorm:"type:text;primaryKey" json:"id"`
	UserID      string `gorm:"type:text;not null;index" json:"user_id"`
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	GithubOwner string `gorm:"type:text" json:"github_owner"`
	GithubRepo  string `gorm:"type:text" json:"github_repo"`
	// CodeGithubOwner/CodeGithubRepo 代码仓库（PR 审查用）：未单独配置时为空，
	// 审查经 CodeRepo() 回落到公开仓库；与公开仓库（Issue 同步/发版）互不影响。
	CodeGithubOwner     string    `gorm:"column:code_github_owner;type:text" json:"code_github_owner"`
	CodeGithubRepo      string    `gorm:"column:code_github_repo;type:text" json:"code_github_repo"`
	PublicGitHubTokenID string    `gorm:"column:public_github_token_id;type:text" json:"-"`
	CodeGitHubTokenID   string    `gorm:"column:code_github_token_id;type:text" json:"-"`
	CreatedAt           time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt           time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Project) TableName() string {
	return "projects"
}

// IsGitHubConfigured 检查项目是否已关联 GitHub 仓库（owner、repo 均非空）。
// 仓库已关联但未配公开仓库 Token 时，公开仓库操作单独按 PublicGitHubTokenID 判定。
// 两个引用列不建外键（避免删用户时 RESTRICT 与 CASCADE 冲突），引用一致性由应用层保证。
func (p *Project) IsGitHubConfigured() bool {
	return p.GithubOwner != "" && p.GithubRepo != ""
}

// CodeRepo 返回 PR 审查使用的代码仓库：两个 code 字段都非空时用显式配置，
// 否则回落公开仓库。审查链路只经它取仓库。
func (p *Project) CodeRepo() (owner, repo string) {
	if p.CodeGithubOwner != "" && p.CodeGithubRepo != "" {
		return p.CodeGithubOwner, p.CodeGithubRepo
	}
	return p.GithubOwner, p.GithubRepo
}

// CodeRepoFullName 返回 "owner/repo" 形式的审查仓库（未配置 code 字段即公开仓库）。
func (p *Project) CodeRepoFullName() string {
	owner, repo := p.CodeRepo()
	return owner + "/" + repo
}

// UniqueIndex: user_id + name
// 通过 GORM 的 CreateIndex 或 AutoMigrate 时手动添加
