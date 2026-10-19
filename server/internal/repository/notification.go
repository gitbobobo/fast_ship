package repository

import (
	"context"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// NotificationRepository 站内通知仓储（原属 automation.go，INT-135 起独立）。
type NotificationRepository struct {
	db *gorm.DB
}

func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

func (r *NotificationRepository) ListByUserID(ctx context.Context, userID string, limit int, unreadOnly bool) ([]model.Notification, error) {
	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if unreadOnly {
		query = query.Where("read_at IS NULL")
	}
	var items []model.Notification
	err := query.Order("updated_at DESC, id DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(&n).Error
	return n, err
}

func (r *NotificationRepository) FindByIDAndUserID(ctx context.Context, id, userID string) (*model.Notification, error) {
	var n model.Notification
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

// MarkRead 条件置 read_at（已读保持幂等，不刷新时间）。
func (r *NotificationRepository) MarkRead(ctx context.Context, id, userID string, now time.Time) error {
	res := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Update("read_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *NotificationRepository) MarkAllRead(ctx context.Context, userID string, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", now)
	return res.RowsAffected, res.Error
}
