package model

import "time"

type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
	LogLevelFatal LogLevel = "fatal"
)

func IsValidLogLevel(level string) bool {
	switch LogLevel(level) {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError, LogLevelFatal:
		return true
	default:
		return false
	}
}

type LogRun struct {
	ID               string     `gorm:"type:text;primaryKey" json:"-"`
	ProjectID        string     `gorm:"type:text;not null;index;uniqueIndex:idx_log_runs_project_run" json:"project_id"`
	RunID            string     `gorm:"type:text;not null;uniqueIndex:idx_log_runs_project_run" json:"run_id"`
	Source           string     `gorm:"type:text;not null;default:''" json:"source"`
	Description      string     `gorm:"type:text;not null;default:''" json:"description"`
	EntryCount       int        `gorm:"not null;default:0" json:"entry_count"`
	FirstEntryAt     *time.Time `json:"first_entry_at"`
	LastEntryAt      *time.Time `json:"last_entry_at"`
	UploaderAPIKeyID *string    `gorm:"type:text" json:"uploader_api_key_id,omitempty"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`

	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (LogRun) TableName() string {
	return "log_runs"
}

type LogRunChunk struct {
	LogRunID  string    `gorm:"type:text;primaryKey" json:"-"`
	ChunkID   string    `gorm:"type:text;primaryKey" json:"-"`
	CreatedAt time.Time `gorm:"not null" json:"-"`

	LogRun LogRun `gorm:"foreignKey:LogRunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (LogRunChunk) TableName() string {
	return "log_run_chunks"
}

type LogEntry struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	LogRunID  string    `gorm:"type:text;not null;index" json:"-"`
	Timestamp time.Time `gorm:"not null;index" json:"timestamp"`
	Level     string    `gorm:"type:text;not null" json:"level"`
	Source    string    `gorm:"type:text;not null;default:''" json:"source"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	Metadata  string    `gorm:"type:text;not null;default:''" json:"metadata"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`

	LogRun LogRun `gorm:"foreignKey:LogRunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (LogEntry) TableName() string {
	return "log_entries"
}
