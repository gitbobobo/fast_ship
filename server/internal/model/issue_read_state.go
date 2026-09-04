package model

import "time"

// FastShipHookCommentMarker 是发货钩子评论正文携带的标记。判断「钩子留言」
// 只能用正文子串：syncComments 的 Upsert 会把 IdempotencyKey 覆盖为空，靠不住。
const FastShipHookCommentMarker = "<!-- fast-ship-hook:"

// IssueReadState 记录用户在某个 Issue 上已读到哪条评论（水位）。
// 未读 = created_at 晚于水位的 GitHub 评论（排除钩子留言）。
type IssueReadState struct {
	UserID     string    `gorm:"type:text;primaryKey" json:"user_id"`
	IssueID    string    `gorm:"type:text;primaryKey" json:"issue_id"`
	LastReadAt time.Time `gorm:"not null" json:"last_read_at"`
	UpdatedAt  time.Time `gorm:"not null" json:"updated_at"`
}

func (IssueReadState) TableName() string {
	return "issue_read_states"
}
