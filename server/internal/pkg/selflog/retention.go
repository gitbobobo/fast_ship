package selflog

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"gorm.io/gorm"
)

// RetentionOptions 控制定时清理：按保留天数删过期，再按最大条数删最旧。
type RetentionOptions struct {
	// KeepDays 是保留天数；≤0 表示不限期（仅受 MaxEntries 约束）。
	KeepDays int
	// MaxEntries 是最大条数上限；≤0 表示不限条数。
	MaxEntries int64
	// Interval 是清理周期；≤0 由 Pipeline 取默认 1 小时。
	Interval time.Duration
}

// Enabled 判断是否需要启动清理循环。
func (o RetentionOptions) Enabled() bool {
	return o.KeepDays > 0 || o.MaxEntries > 0
}

// 每批独立事务，批间让路给并发写入，避免单次大 DELETE 占锁拖超时。
const cleanupBatchSize = 500

// Cleanup 执行一轮清理，返回本轮删除的条数。按主键分批删除后做 WAL checkpoint。
func (s *Store) Cleanup(ctx context.Context, opts RetentionOptions) (int64, error) {
	var removed int64

	if opts.KeepDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -opts.KeepDays).UnixNano()
		n, err := s.deleteOldestMatching(ctx, "time_unix < ?", []interface{}{cutoff}, 0)
		if err != nil {
			return removed, err
		}
		removed += n
	}

	if opts.MaxEntries > 0 {
		n, err := s.trimToMaxEntries(ctx, opts.MaxEntries)
		if err != nil {
			return removed, err
		}
		removed += n
	}

	s.checkpoint(ctx)
	return removed, nil
}

func (s *Store) trimToMaxEntries(ctx context.Context, max int64) (int64, error) {
	var removed int64
	for {
		var count int64
		if err := s.db.WithContext(ctx).Model(&Record{}).Count(&count).Error; err != nil {
			return removed, fmt.Errorf("count self logs: %w", err)
		}
		if count <= max {
			return removed, nil
		}
		n, err := s.deleteOldestMatching(ctx, "", nil, count-max)
		if err != nil {
			return removed, err
		}
		if n == 0 {
			return removed, nil
		}
		removed += n
	}
}

// deleteOldestMatching 按时间升序分批删除。limit>0 时最多删 limit 条；
// where 为空则删全表最旧的（配合 trim）。每批独立事务，批间 Gosched 让路。
func (s *Store) deleteOldestMatching(ctx context.Context, where string, args []interface{}, limit int64) (int64, error) {
	var removed int64
	remaining := limit
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		batch := cleanupBatchSize
		if remaining > 0 && remaining < int64(batch) {
			batch = int(remaining)
		}

		var n int64
		err := retryBusy(ctx, func() error {
			return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				q := tx.Model(&Record{})
				if where != "" {
					q = q.Where(where, args...)
				}
				var ids []int64
				if err := q.Order("time_unix ASC, id ASC").Limit(batch).Pluck("id", &ids).Error; err != nil {
					return err
				}
				if len(ids) == 0 {
					n = 0
					return nil
				}
				res := tx.Where("id IN ?", ids).Delete(&Record{})
				if res.Error != nil {
					return res.Error
				}
				n = res.RowsAffected
				return nil
			})
		})
		if err != nil {
			return removed, fmt.Errorf("delete self logs batch: %w", err)
		}
		if n == 0 {
			return removed, nil
		}
		removed += n
		if remaining > 0 {
			remaining -= n
			if remaining <= 0 {
				return removed, nil
			}
		}
		runtime.Gosched()
	}
}

func (s *Store) checkpoint(ctx context.Context) {
	sqlDB, err := s.db.DB()
	if err != nil {
		return
	}
	_, _ = sqlDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
}
