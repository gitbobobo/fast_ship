// Package selflog 是 Fast Ship 自身日志（INT-94）的采集与存储适配层：
// slog handler → 异步批量写入独立 SQLite 日志库，同时保留 stdout 可读输出；
// 查询侧提供列表/聚合/链路三种只读读取。
package selflog

import (
	"context"
	"log/slog"
	"time"
)

// 级别采用 slog 的语义，存储为字符串；排序按数值级别比较。
const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// 关联字段是 Entry 的固定列；其余 slog 属性统一进 Extra JSON。
const (
	AttrModule       = "module"
	AttrRequestID    = "request_id"
	AttrUserID       = "user_id"
	AttrProjectID    = "project_id"
	AttrIssueID      = "issue_id"
	AttrAutomationID = "automation_id"
	AttrRunID        = "run_id"
	AttrThreadID     = "thread_id"
)

// CorrelationFields 列出可作等值过滤与链路查询的关联字段名（供 handler 校验 fields 参数）。
var CorrelationFields = []string{
	AttrModule, AttrRequestID, AttrUserID, AttrProjectID,
	AttrIssueID, AttrAutomationID, AttrRunID, AttrThreadID,
}

// Entry 是一条自身日志的内存表示，与存储列一一对应。
type Entry struct {
	ID           int64     `json:"id"`
	Time         time.Time `json:"time"`
	Level        string    `json:"level"`
	Module       string    `json:"module,omitempty"`
	Message      string    `json:"message"`
	RequestID    string    `json:"request_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	IssueID      string    `json:"issue_id,omitempty"`
	AutomationID string    `json:"automation_id,omitempty"`
	RunID        string    `json:"run_id,omitempty"`
	ThreadID     string    `json:"thread_id,omitempty"`
	Extra        string    `json:"extra,omitempty"`
	// Dropped 标记该条因管道积压被丢弃前的重试信息，仅供内部诊断，不入库。
	Dropped bool `json:"-"`
}

// LevelValue 返回级别的数值（数值越大越严重），未知级别按 INFO 处理。
func LevelValue(level string) int {
	switch level {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	}
	return 1
}

// NormalizeLevel 把 slog.Level 归一为存储用的级别字符串。
func NormalizeLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarn
	case l >= slog.LevelInfo:
		return LevelInfo
	default:
		return LevelDebug
	}
}

// ParseLevel 解析级别字符串（大小写不敏感）；无法识别返回 false。
func ParseLevel(s string) (string, bool) {
	switch normalize(s) {
	case LevelDebug:
		return LevelDebug, true
	case LevelInfo:
		return LevelInfo, true
	case LevelWarn:
		return LevelWarn, true
	case LevelError:
		return LevelError, true
	}
	return "", false
}

// Sink 是日志库的写入端；Pipeline 通过它与具体存储解耦，测试可注入失败实现。
type Sink interface {
	Add(ctx context.Context, entries []Entry) error
}

func normalize(s string) string {
	if len(s) == 0 {
		return ""
	}
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
