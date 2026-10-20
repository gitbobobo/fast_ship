package selflog

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Record 是 self_logs 表的行模型。时间存 UnixNano 整数：RFC3339 文本的
// 变长小数秒破坏字典序，无法做 (time, id) 键集分页。
type Record struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	TimeUnix  int64  `gorm:"not null;index:idx_self_logs_time_id,priority:1"`
	Level     string `gorm:"type:text;not null;index"`
	Module    string `gorm:"type:text;not null;default:'';index"`
	Message   string `gorm:"type:text;not null"`
	RequestID string `gorm:"type:text;not null;default:'';index"`
	UserID    string `gorm:"type:text;not null;default:'';index"`
	ProjectID string `gorm:"type:text;not null;default:''"`
	IssueID   string `gorm:"type:text;not null;default:''"`
	RunID     string `gorm:"type:text;not null;default:'';index"`
	ThreadID  string `gorm:"type:text;not null;default:''"`
	// SystemScope 1=显式系统行（无归属且无业务关联列）。默认 0；存量空归属行不回填。
	SystemScope int `gorm:"column:system_scope;not null;default:0"`
	// Extra 指针语义：nil 存 NULL，供 json_extract 直接跳过（空串不是合法 JSON）。
	Extra *string `gorm:"type:text"`
}

func (Record) TableName() string { return "self_logs" }

// Store 是独立 SQLite 日志库的读写实现。
type Store struct {
	db   *gorm.DB
	path string
}

// OpenStore 打开（必要时创建）独立日志库并迁移表结构。日志库与业务库
// 完全隔离：单连接串行写入，WAL 降低读写互斥。
func OpenStore(path string) (*Store, error) {
	dsn := fmt.Sprintf(
		"%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)",
		path,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open self log db: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("self log db handle: %w", err)
	}
	// 写入全部由 Pipeline 的单个 batcher 串行发起；查询端允许少量并发读，
	// MaxOpenConns(2) 在单文件 SQLite 上足够且避免多连接写锁竞争。
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.AutoMigrate(&Record{}); err != nil {
		return nil, fmt.Errorf("migrate self log db: %w", err)
	}
	db.Exec("CREATE INDEX IF NOT EXISTS idx_self_logs_time_id ON self_logs(time_unix, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_self_logs_request_time ON self_logs(request_id, time_unix, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_self_logs_run_time ON self_logs(run_id, time_unix, id)")

	return &Store{db: db, path: path}, nil
}

// Add 批量写入一条事务；调用方（Pipeline batcher）保证低频大批量。
func (s *Store) Add(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	records := make([]Record, len(entries))
	for i, e := range entries {
		// Extra 留 nil 而非空串：json_extract 对空串报 malformed JSON。
		rec := Record{
			TimeUnix:  e.Time.UnixNano(),
			Level:     e.Level,
			Module:    e.Module,
			Message:   e.Message,
			RequestID: e.RequestID,
			UserID:    e.UserID,
			ProjectID: e.ProjectID,
			IssueID:   e.IssueID,
			RunID:     e.RunID,
			ThreadID:  e.ThreadID,
		}
		// 带 user_id 或任一关联列时拒绝系统行：误标不得把业务日志公开给所有认证用户。
		if e.SystemScope && e.allowsSystemScope() {
			rec.SystemScope = 1
		}
		if e.Extra != "" {
			rec.Extra = &e.Extra
		}
		records[i] = rec
	}
	return retryBusy(ctx, func() error {
		return s.db.WithContext(ctx).Create(&records).Error
	})
}

const busyRetries = 12

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// retryBusy 在 SQLite 写锁争用时短回退重试。管道层仍可在耗尽后丢弃该批。
func retryBusy(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < busyRetries; i++ {
		err = fn()
		if err == nil || !isBusy(err) {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(time.Duration(i+1) * 5 * time.Millisecond)
		runtime.Gosched()
	}
	return err
}

// Count 返回当前日志总条数（保留策略与容量测试用）。
func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&Record{}).Count(&n).Error
	return n, err
}

// FileSize 返回日志库实际磁盘占用（主库 + WAL + SHM，字节）。
func (s *Store) FileSize() int64 {
	var total int64
	for _, p := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}
