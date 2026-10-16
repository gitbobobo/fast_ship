package model

import "time"

// 机器人记忆（INT-102）：对模型是文件式接口（USER.md / MEMORY.md /
// daily/日期.md），底层存数据库。bot_memory_files 是当前文件（删除用墓碑，
// 历史保留）；bot_memory_versions 是完整版本快照，每文件保留最近 50 个版本。
// 文件身份 (bot_id, path) 唯一，重建同路径沿用文件身份并产生新版本。

type BotMemoryWriterKind string

const (
	// BotMemoryWriterMain 主对话线（含压缩前静默存档——它是主线衍生操作，
	// 同时关联维护运行）。
	BotMemoryWriterMain BotMemoryWriterKind = "main"
	// BotMemoryWriterScheduled 预留给 INT-103 通用定时任务。
	BotMemoryWriterScheduled BotMemoryWriterKind = "scheduled"
	// BotMemoryWriterNightly 夜间整理。
	BotMemoryWriterNightly BotMemoryWriterKind = "nightly"
)

type BotMemoryOperation string

const (
	BotMemoryOpCreate  BotMemoryOperation = "create"
	BotMemoryOpReplace BotMemoryOperation = "replace"
	BotMemoryOpAppend  BotMemoryOperation = "append"
	BotMemoryOpDelete  BotMemoryOperation = "delete"
	BotMemoryOpRestore BotMemoryOperation = "restore"
)

// BotMemoryFile 是某机器人的一个记忆文件的当前状态。
type BotMemoryFile struct {
	ID    string `gorm:"type:text;primaryKey" json:"id"`
	BotID string `gorm:"type:text;not null" json:"bot_id"`
	// Path 是数据库逻辑路径（USER.md / MEMORY.md / daily/YYYY-MM-DD.md），
	// 不对应本机文件系统。
	Path    string `gorm:"type:text;not null" json:"path"`
	Content string `gorm:"type:text;not null" json:"content"`
	// ContentHash 是正文的 SHA-256（十六进制），用于快速比对与审计。
	ContentHash string `gorm:"type:text;not null" json:"content_hash"`
	// CurrentVersion 是当前版本号；任何正文/删除状态改变都必须恰好加一，
	// 且存在匹配的版本行（守护触发器拦截）。
	CurrentVersion int64      `gorm:"not null" json:"current_version"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotMemoryFile) TableName() string {
	return "bot_memory_files"
}

// BotMemoryVersion 是一次写入的完整快照，只追加（禁止 UPDATE，允许 DELETE
// 用于 50 版保留与级联）。OperationKey 是幂等键（服务端调用 ID 或维护运行 ID）。
type BotMemoryVersion struct {
	ID      string `gorm:"type:text;primaryKey" json:"id"`
	FileID  string `gorm:"type:text;not null" json:"file_id"`
	Version int64  `gorm:"not null" json:"version"`

	Operation BotMemoryOperation `gorm:"type:text;not null" json:"operation"`
	// Content 是该版本完整正文；删除墓碑记删除前正文（Deleted=true）。
	Content     string `gorm:"type:text;not null" json:"content"`
	Deleted     bool   `gorm:"not null" json:"deleted"`
	ContentHash string `gorm:"type:text;not null" json:"content_hash"`

	// WriterKind: main / scheduled / nightly。
	WriterKind BotMemoryWriterKind `gorm:"type:text;not null" json:"writer_kind"`
	// SourceRunID 是主线来源 run；MaintenanceRunID 关联维护运行（存档/整理）。
	SourceRunID      *string `gorm:"type:text" json:"source_run_id,omitempty"`
	MaintenanceRunID *string `gorm:"type:text" json:"maintenance_run_id,omitempty"`
	// OperationKey 幂等键：主线写 = 平台调用 ID；存档 = 维护运行 ID。
	OperationKey string    `gorm:"type:text;not null" json:"operation_key"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`

	File BotMemoryFile `gorm:"foreignKey:FileID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotMemoryVersion) TableName() string {
	return "bot_memory_versions"
}
