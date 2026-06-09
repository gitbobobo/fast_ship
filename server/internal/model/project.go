package model

import "time"

type Project struct {
	ID                   string    `gorm:"type:text;primaryKey" json:"id"`
	UserID               string    `gorm:"type:text;not null;index" json:"user_id"`
	Name                 string    `gorm:"type:text;not null" json:"name"`
	Description          string    `gorm:"type:text" json:"description"`
	GithubOwner          string    `gorm:"type:text" json:"github_owner"`
	GithubRepo           string    `gorm:"type:text" json:"github_repo"`
	GithubTokenEncrypted []byte    `gorm:"type:blob" json:"-"`
	CreatedAt            time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Project) TableName() string {
	return "projects"
}

// IsGitHubConfigured 检查项目是否已关联 GitHub 仓库（owner、repo、token 三者均非空）
func (p *Project) IsGitHubConfigured() bool {
	return p.GithubOwner != "" && p.GithubRepo != "" && len(p.GithubTokenEncrypted) > 0
}

// UniqueIndex: user_id + name
// 通过 GORM 的 CreateIndex 或 AutoMigrate 时手动添加
