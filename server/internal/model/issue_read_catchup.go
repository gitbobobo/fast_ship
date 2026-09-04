package model

import "time"

// IssueReadCatchup 标记「存量 GitHub Issue 视为已赶上」回填是否已完成（单行表）。
type IssueReadCatchup struct {
	ID          int       `gorm:"primaryKey" json:"id"`
	CompletedAt time.Time `gorm:"not null" json:"completed_at"`
}

func (IssueReadCatchup) TableName() string {
	return "issue_read_catchup"
}
