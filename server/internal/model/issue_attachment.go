package model

import "time"

// IssueAttachment 是 Issue 的文件附件。与正文内联图片（issue_assets）相互独立：
// 生命周期不随正文引用变化，仅显式删除或 Issue 删除（FK 级联）时移除；
// 项目删除时经 issues 级联删行，磁盘文件随 DeletePrefix(projectID) 一并清理。
type IssueAttachment struct {
	ID         string    `gorm:"type:text;primaryKey" json:"id"`
	IssueID    string    `gorm:"type:text;not null;index" json:"issue_id"`
	FileName   string    `gorm:"type:text;not null" json:"file_name"`
	FilePath   string    `gorm:"type:text;not null" json:"-"`
	MimeType   string    `gorm:"type:text;not null" json:"mime_type"`
	FileSize   int64     `gorm:"not null" json:"file_size"`
	UploadedBy string    `gorm:"type:text" json:"uploaded_by"`
	CreatedAt  time.Time `gorm:"not null;index" json:"created_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueAttachment) TableName() string {
	return "issue_attachments"
}
