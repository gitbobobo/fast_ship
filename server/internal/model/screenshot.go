package model

import "time"

// ScreenshotScreen 是截图库中的一张截图：同一 (project_id, screen_key,
// platform, locale, theme) 变体唯一，再上传即覆盖文件而非新增行。
// 文件信息直接挂在 screen 行上；platform/locale/theme 描述截图所属维度，
// 均可空（NULL = 未标注）。唯一索引 (project_id, screen_key) 由迁移删除，
// 变体唯一由 main.go 的 COALESCE 表达式唯一索引保证（NULL 与 NULL 视为相等）。
type ScreenshotScreen struct {
	ID        string `gorm:"type:text;primaryKey" json:"id"`
	ProjectID string `gorm:"type:text;not null;index" json:"project_id"`
	ScreenKey string `gorm:"type:text;not null" json:"screen_key"`
	Title     string `gorm:"type:text" json:"title"`
	// 三个变体维度，NULL = 未标注；platform 开放词表，locale BCP 47，theme light|dark
	Platform *string `gorm:"type:text" json:"platform"`
	Locale   *string `gorm:"type:text" json:"locale"`
	Theme    *string `gorm:"type:text" json:"theme"`
	// 当前文件信息；FilePath 是存储相对路径，永不随 JSON 输出
	FilePath   string    `gorm:"type:text;not null" json:"-"`
	MimeType   string    `gorm:"type:text;not null" json:"mime_type"`
	FileSize   int64     `gorm:"not null" json:"file_size"`
	Width      int       `gorm:"not null;default:0" json:"width"`
	Height     int       `gorm:"not null;default:0" json:"height"`
	UploadedBy string    `gorm:"type:text" json:"uploaded_by"`
	UploadedAt time.Time `gorm:"not null" json:"uploaded_at"`
	CreatedAt  time.Time `gorm:"not null" json:"created_at"`

	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ScreenshotScreen) TableName() string {
	return "screenshot_screens"
}

// ScreenshotPendingDelete 是折叠迁移的待删文件队列（INT-99）：旧版本文件路径
// 在删除事务提交前落库，提交后逐个删文件，成功即出队；删除失败的行留在表里，
// 下次启动继续重试，不依赖迁移进程存活。
type ScreenshotPendingDelete struct {
	Path      string    `gorm:"type:text;primaryKey" json:"-"`
	CreatedAt time.Time `gorm:"not null" json:"-"`
}

func (ScreenshotPendingDelete) TableName() string {
	return "screenshot_pending_deletes"
}
