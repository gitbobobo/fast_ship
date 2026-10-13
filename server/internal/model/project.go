package model

import "time"

type Project struct {
	ID                  string    `gorm:"type:text;primaryKey" json:"id"`
	UserID              string    `gorm:"type:text;not null;index" json:"user_id"`
	Name                string    `gorm:"type:text;not null" json:"name"`
	Description         string    `gorm:"type:text" json:"description"`
	GithubOwner         string    `gorm:"type:text" json:"github_owner"`
	GithubRepo          string    `gorm:"type:text" json:"github_repo"`
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

// UniqueIndex: user_id + name
// 通过 GORM 的 CreateIndex 或 AutoMigrate 时手动添加
