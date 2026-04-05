package model

import "time"

type Project struct {
	ID                    string    `gorm:"type:text;primaryKey" json:"id"`
	UserID                string    `gorm:"type:text;not null;index" json:"user_id"`
	Name                  string    `gorm:"type:text;not null" json:"name"`
	Description           string    `gorm:"type:text" json:"description"`
	GithubOwner           string    `gorm:"type:text;not null" json:"github_owner"`
	GithubRepo            string    `gorm:"type:text;not null" json:"github_repo"`
	GithubTokenEncrypted  []byte    `gorm:"type:blob;not null" json:"-"`
	CreatedAt             time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt             time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Project) TableName() string {
	return "projects"
}

// UniqueIndex: user_id + name
// 通过 GORM 的 CreateIndex 或 AutoMigrate 时手动添加
