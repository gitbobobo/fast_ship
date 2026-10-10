package model

import "time"

// ScreenshotAnnotation 是挂在某个截图版本上的「矩形框 + 文字」标注。
// 坐标以图片宽高比例（0~1）存储，展示时按原图像素换算。
// Version/Screen/Project 删除级联删除标注；Issue 删除只把 issue_id 置空，
// 标注本身保留（解除关联，不丢用户写的内容）。IssueID 的可空语义依赖
// SQLite 外键约束，须保证连接开启 foreign_keys。
type ScreenshotAnnotation struct {
	ID         string     `gorm:"type:text;primaryKey" json:"id"`
	VersionID  string     `gorm:"type:text;not null;index" json:"version_id"`
	ScreenID   string     `gorm:"type:text;not null;index" json:"screen_id"`
	ProjectID  string     `gorm:"type:text;not null;index" json:"project_id"`
	IssueID    *string    `gorm:"type:text;index" json:"issue_id"`
	X          float64    `gorm:"not null" json:"x"`
	Y          float64    `gorm:"not null" json:"y"`
	Width      float64    `gorm:"not null" json:"width"`
	Height     float64    `gorm:"not null" json:"height"`
	Body       string     `gorm:"type:text" json:"body"`
	Status     string     `gorm:"type:text;not null;default:'open';index" json:"status"`
	CreatedBy  string     `gorm:"type:text" json:"created_by"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt  time.Time  `gorm:"not null" json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at"`

	// 只声明 belongs-to：与父表双侧声明时 GORM 取父侧 constraint（见 issue.go 注释），
	// 而 Issue 模型不携带标注的 has-many，本侧 constraint 即生效。
	Version ScreenshotVersion `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE" json:"-"`
	Screen  ScreenshotScreen  `gorm:"foreignKey:ScreenID;constraint:OnDelete:CASCADE" json:"-"`
	Project Project           `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	Issue   *Issue            `gorm:"foreignKey:IssueID;constraint:OnDelete:SET NULL" json:"-"`
}

func (ScreenshotAnnotation) TableName() string {
	return "screenshot_annotations"
}
