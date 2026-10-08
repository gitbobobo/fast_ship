package model

import "time"

// ScreenshotScreen 是截图库中的一个界面：同一界面（project_id + screen_key 唯一）
// 聚合历次上传的全部版本；version_count 与 last_uploaded_at 为冗余字段，
// 由写路径在版本增删时重算，供列表直接排序展示。
type ScreenshotScreen struct {
	ID             string    `gorm:"type:text;primaryKey" json:"id"`
	ProjectID      string    `gorm:"type:text;not null;index" json:"project_id"`
	ScreenKey      string    `gorm:"type:text;not null" json:"screen_key"`
	Title          string    `gorm:"type:text" json:"title"`
	GroupName      string    `gorm:"type:text;column:group_name" json:"group"`
	VersionCount   int       `gorm:"not null;default:0" json:"version_count"`
	LastUploadedAt time.Time `gorm:"not null" json:"last_uploaded_at"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`

	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ScreenshotScreen) TableName() string {
	return "screenshot_screens"
}

// UniqueIndex: project_id + screen_key

// ScreenshotVersion 是一次上传产生的截图版本，全量保留不清理。
// FilePath 是存储相对路径，永不随 JSON 输出。
type ScreenshotVersion struct {
	ID         string    `gorm:"type:text;primaryKey" json:"id"`
	ScreenID   string    `gorm:"type:text;not null;index" json:"screen_id"`
	FileName   string    `gorm:"type:text;not null" json:"file_name"`
	FilePath   string    `gorm:"type:text;not null" json:"-"`
	MimeType   string    `gorm:"type:text;not null" json:"mime_type"`
	FileSize   int64     `gorm:"not null" json:"file_size"`
	Note       string    `gorm:"type:text" json:"note"`
	UploadedBy string    `gorm:"type:text" json:"uploaded_by"`
	UploadedAt time.Time `gorm:"not null" json:"uploaded_at"`

	Screen ScreenshotScreen `gorm:"foreignKey:ScreenID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ScreenshotVersion) TableName() string {
	return "screenshot_versions"
}
