package model

import "time"

// LegacyAutomationThread 通用自动化下线（INT-135）时仍在途的旧运行线程。
// 迁移会删掉 automation_runs，旧调度器与 agent 端点也一并删除；这里保留最小
// 墓碑，让新服务在启动后仍能停掉/归档这些远端线程（处理完即删行）。
type LegacyAutomationThread struct {
	ID            uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        string    `gorm:"column:user_id;type:text;not null" json:"user_id"`
	EnvironmentID string    `gorm:"column:environment_id;type:text;not null" json:"environment_id"`
	ThreadID      string    `gorm:"column:thread_id;type:text;not null" json:"thread_id"`
	Attempts      int       `gorm:"not null;default:0" json:"attempts"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
}

func (LegacyAutomationThread) TableName() string { return "legacy_automation_threads" }
