package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NotificationSink 站内通知写入端（INT-90）。审查系统故障通知共用一套 dedup 语义：
//
//	dedup_key        = hash(environment_id, thread_id, kind, problem_key)
//	state_fingerprint = hash(state_key)
//
// 同键同指纹只推进 last_seen_at（同状态重复发生不刷屏）；指纹变化视为状态
// 变化：更新标题/正文并清 read_at（让用户重新注意到）。弱引用字段
// （environment/thread/run）只存值不建外键，目标删除后通知保留。
type NotificationSink struct {
	db *gorm.DB
}

func NewNotificationSink(db *gorm.DB) *NotificationSink {
	return &NotificationSink{db: db}
}

// NotificationInput 一次通知发布的内容。ProblemKey 缺省用 run_id，
// StateKey 缺省用 title+body。
type NotificationInput struct {
	UserID        string
	Kind          model.NotificationKind
	EnvironmentID string
	ThreadID      string
	RunID         string
	ProblemKey    string
	StateKey      string
	Title         string
	Body          string
}

// PublishTx 在调用方事务内写通知；事务回滚则通知一并消失（与触发同生命周期）。
func (s *NotificationSink) PublishTx(tx *gorm.DB, in NotificationInput) error {
	if in.UserID == "" || in.Title == "" {
		return errs.New(errs.ErrInvalidParams.Code, "通知缺少 user 或标题")
	}
	if !isValidNotificationKind(in.Kind) {
		return errs.New(errs.ErrInvalidParams.Code, "未知的通知类型 "+string(in.Kind))
	}
	problemKey := in.ProblemKey
	if problemKey == "" {
		problemKey = in.RunID
	}
	stateKey := in.StateKey
	if stateKey == "" {
		stateKey = in.Title + "\n" + in.Body
	}
	dedupKey := notificationHash(in.EnvironmentID, in.ThreadID, string(in.Kind), problemKey)
	fingerprint := notificationHash("state", stateKey)

	now := time.Now().UTC()
	var existing model.Notification
	err := tx.Where("user_id = ? AND dedup_key = ?", in.UserID, dedupKey).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&model.Notification{
			ID:               uuid.NewString(),
			UserID:           in.UserID,
			Kind:             in.Kind,
			DedupKey:         dedupKey,
			StateFingerprint: fingerprint,
			Title:            in.Title,
			Body:             in.Body,
			EnvironmentID:    in.EnvironmentID,
			ThreadID:         in.ThreadID,
			RunID:            in.RunID,
			CreatedAt:        now,
			UpdatedAt:        now,
			LastSeenAt:       now,
		}).Error
	case err != nil:
		return err
	}

	// 同键：指纹相同只续 last_seen_at；变化则更新内容并清已读。
	if existing.StateFingerprint == fingerprint {
		return tx.Model(&model.Notification{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"updated_at": now, "last_seen_at": now}).Error
	}
	return tx.Model(&model.Notification{}).Where("id = ?", existing.ID).
		Updates(map[string]any{
			"state_fingerprint": fingerprint,
			"title":             in.Title,
			"body":              in.Body,
			"updated_at":        now,
			"last_seen_at":      now,
			"read_at":           nil,
		}).Error
}

// Publish 非事务入口：独立小事务场景。
func (s *NotificationSink) Publish(in NotificationInput) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.PublishTx(tx, in)
	})
}

func notificationHash(parts ...string) string {
	h := sha256.New()
	for i, part := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		fmt.Fprintf(h, "%s", part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isValidNotificationKind(kind model.NotificationKind) bool {
	switch kind {
	case model.NotificationReviewDispatchFailed,
		model.NotificationReviewRunTimeout,
		model.NotificationReviewCleanupFailed,
		model.NotificationReviewRedeliveryFailed,
		model.NotificationBotBudgetThreshold:
		return true
	}
	return false
}

// ---- JWT 通知查询面（设置页/铃铛） ----

type NotificationService struct {
	repo *repository.NotificationRepository
}

func NewNotificationService(repo *repository.NotificationRepository) *NotificationService {
	return &NotificationService{repo: repo}
}

func (s *NotificationService) List(ctx context.Context, userID string, limit int, unreadOnly bool) ([]NotificationResponse, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	items, err := s.repo.ListByUserID(ctx, userID, limit, unreadOnly)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]NotificationResponse, 0, len(items))
	for i := range items {
		n := &items[i]
		out = append(out, NotificationResponse{
			Id:            n.ID,
			Kind:          api.NotificationKind(n.Kind),
			Title:         n.Title,
			Body:          n.Body,
			EnvironmentId: api.NonEmptyPtr(&n.EnvironmentID),
			ThreadId:      api.NonEmptyPtr(&n.ThreadID),
			RunId:         api.NonEmptyPtr(&n.RunID),
			CreatedAt:     n.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:     n.UpdatedAt.UTC().Format(time.RFC3339Nano),
			LastSeenAt:    n.LastSeenAt.UTC().Format(time.RFC3339Nano),
			ReadAt:        nonEmptyTimePtr(n.ReadAt),
		})
	}
	return out, nil
}

func (s *NotificationService) UnreadCount(ctx context.Context, userID string) (int64, error) {
	return s.repo.CountUnread(ctx, userID)
}

// MarkRead 幂等：已读的通知再标记一次仍返回成功（404 只针对不存在/非本人）。
func (s *NotificationService) MarkRead(ctx context.Context, userID, id string) error {
	if _, err := s.repo.FindByIDAndUserID(ctx, id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrNotificationNotFound
		}
		return errs.ErrInternal
	}
	if err := s.repo.MarkRead(ctx, id, userID, time.Now().UTC()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 并发已读，幂等成功
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *NotificationService) MarkAllRead(ctx context.Context, userID string) error {
	if _, err := s.repo.MarkAllRead(ctx, userID, time.Now().UTC()); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func nonEmptyTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return api.Ptr(t.UTC().Format(time.RFC3339Nano))
}
