package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// GitHub PR 自动评审（INT-93）三张支撑表的仓储。全部走调用方传入的 tx/db，
// 与自动化调度共用「MaxOpenConns=1、事务内只碰 tx」的纪律。

type GitHubWebhookDeliveryRepository struct {
	db *gorm.DB
}

func NewGitHubWebhookDeliveryRepository(db *gorm.DB) *GitHubWebhookDeliveryRepository {
	return &GitHubWebhookDeliveryRepository{db: db}
}

// InsertTx 插入 delivery 幂等账；撞 PK（重复投递）返回 false 且不视为错误。
func (r *GitHubWebhookDeliveryRepository) InsertTx(tx *gorm.DB, d *model.GitHubWebhookDelivery) (bool, error) {
	err := tx.Create(d).Error
	if err != nil && isUniqueConstraintError(err) {
		return false, nil
	}
	return err == nil, err
}

// isUniqueConstraintError SQLite 唯一约束冲突（本包不引 service 层的同名判定）。
func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// DeleteOlderThanTx 清理 7 天前的 delivery 行（调度循环顺带执行）。
func (r *GitHubWebhookDeliveryRepository) DeleteOlderThanTx(tx *gorm.DB, cutoff time.Time) (int64, error) {
	res := tx.Where("received_at < ?", cutoff).Delete(&model.GitHubWebhookDelivery{})
	return res.RowsAffected, res.Error
}

type GitHubPRDebounceRepository struct {
	db *gorm.DB
}

func NewGitHubPRDebounceRepository(db *gorm.DB) *GitHubPRDebounceRepository {
	return &GitHubPRDebounceRepository{db: db}
}

// UpsertTx 防抖行：一行只留最新 SHA，窗口内新推送重置 fire_at。
// 全列覆盖写（PK 冲突时按 PK 更新），保证 fire_at/action/head_sha 同步推进。
func (r *GitHubPRDebounceRepository) UpsertTx(tx *gorm.DB, d *model.GitHubPRDebounce) error {
	return tx.Save(d).Error
}

// ListDueTx 取到期待触发的防抖行。
func (r *GitHubPRDebounceRepository) ListDueTx(db *gorm.DB, now time.Time, limit int) ([]model.GitHubPRDebounce, error) {
	var items []model.GitHubPRDebounce
	err := db.Where("fire_at <= ?", now).Order("fire_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

// DeleteTx 消费防抖行（到点开轮后删除；条件命中 0 行=已被并发消费）。
func (r *GitHubPRDebounceRepository) DeleteTx(tx *gorm.DB, automationID, repoFullName string, prNumber int) error {
	return tx.Where("automation_id = ? AND repo_full_name = ? AND pr_number = ?",
		automationID, repoFullName, prNumber).Delete(&model.GitHubPRDebounce{}).Error
}

// DeleteStaleSHATx 丢掉该 PR 下旧 SHA 的防抖行（opened 规则的窗口不能在
// synchronize 新 SHA 之后再开一轮）。
func (r *GitHubPRDebounceRepository) DeleteStaleSHATx(tx *gorm.DB, repoFullName string, prNumber int, currentSHA string) error {
	return tx.Where("repo_full_name = ? AND pr_number = ? AND head_sha <> ?",
		repoFullName, prNumber, currentSHA).Delete(&model.GitHubPRDebounce{}).Error
}

type GitHubPRReviewRepository struct {
	db *gorm.DB
}

func NewGitHubPRReviewRepository(db *gorm.DB) *GitHubPRReviewRepository {
	return &GitHubPRReviewRepository{db: db}
}

// UpsertTx 每 PR 一行的同步账：推进轮次/SHA 并置 sync_pending（等待事务外同步）。
// 已有 comment_id 必须保留——新一轮开轮时调用方不带评论 ID，Save 全列覆盖会把它清零，
// 随后只能靠标记找回，标记失效就会再发一条顶层评论。
func (r *GitHubPRReviewRepository) UpsertTx(tx *gorm.DB, rev *model.GitHubPRReview) error {
	var existing model.GitHubPRReview
	err := tx.Where("repo_full_name = ? AND pr_number = ?", rev.RepoFullName, rev.PRNumber).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if rev.SyncEpoch == 0 {
			rev.SyncEpoch = 1
		}
		return tx.Create(rev).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&existing).Updates(map[string]any{
		"installation_id": rev.InstallationID,
		"round_number":    rev.RoundNumber,
		"head_sha":        rev.HeadSHA,
		"sync_pending":    true,
		"sync_epoch":      existing.SyncEpoch + 1,
		"sync_error":      "",
		"updated_at":      rev.UpdatedAt,
	}).Error
}

// ListPendingSyncTx 取待同步的 PR（调度循环事务外逐个同步评论/check）。
func (r *GitHubPRReviewRepository) ListPendingSyncTx(db *gorm.DB, limit int) ([]model.GitHubPRReview, error) {
	var items []model.GitHubPRReview
	err := db.Where("sync_pending = ?", true).Order("updated_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

// ClearPendingSync 同步成功后清位。只清开始同步时的 sync_epoch：期间又有
// webhook/终态会 +1，本帧 0 行，保留 pending 给下轮。不用 updated_at 相等——
// 写 comment_id 也会碰 updated_at，会把清位条件打空。
func (r *GitHubPRReviewRepository) ClearPendingSync(ctx context.Context, rev *model.GitHubPRReview, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND sync_epoch = ? AND sync_pending = ?",
			rev.RepoFullName, rev.PRNumber, rev.SyncEpoch, true).
		Updates(map[string]any{"sync_pending": false, "sync_error": "", "updated_at": now}).Error
}

// SetSyncError 同步失败记错误（下轮 tick 重试，不改运行终态）。同样只写本帧。
func (r *GitHubPRReviewRepository) SetSyncError(ctx context.Context, rev *model.GitHubPRReview, syncErr string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
			rev.RepoFullName, rev.PRNumber, rev.SyncEpoch).
		Updates(map[string]any{"sync_error": syncErr, "updated_at": now}).Error
}

// FindByRepoAndPR 读取单行。
func (r *GitHubPRReviewRepository) FindByRepoAndPR(ctx context.Context, repoFullName string, prNumber int) (*model.GitHubPRReview, error) {
	var rev model.GitHubPRReview
	if err := r.db.WithContext(ctx).
		Where("repo_full_name = ? AND pr_number = ?", repoFullName, prNumber).
		First(&rev).Error; err != nil {
		return nil, err
	}
	return &rev, nil
}

// SaveRunCheckRunIDTx 条件写回运行的 check_run_id（NULL 才写，避免覆盖并发写入）。
func (r *GitHubPRReviewRepository) SaveRunCheckRunIDTx(tx *gorm.DB, runID string, checkRunID int64) (int64, error) {
	res := tx.Model(&model.AutomationRun{}).
		Where("id = ? AND check_run_id IS NULL", runID).
		Update("check_run_id", checkRunID)
	return res.RowsAffected, res.Error
}

// ClearRunCheckRunIDTx 退回合并 check 时清掉单模型 check 引用。
func (r *GitHubPRReviewRepository) ClearRunCheckRunIDTx(tx *gorm.DB, runID string) error {
	return tx.Model(&model.AutomationRun{}).
		Where("id = ?", runID).
		Update("check_run_id", nil).Error
}

// RoundNumberForHeadSHATx 同一 SHA 共用一轮：该 repo+pr+sha 已有运行则复用其
// 轮次，避免多规则/重复开轮把 github_pr_reviews.round_number 推到空轮。
func RoundNumberForHeadSHATx(tx *gorm.DB, repoFullName string, prNumber int, headSHA string) (int, error) {
	var existing *int
	if err := tx.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?", repoFullName, prNumber, headSHA).
		Select("MAX(round_number)").Scan(&existing).Error; err != nil {
		return 0, err
	}
	if existing != nil && *existing > 0 {
		return *existing, nil
	}
	return NextRoundNumberTx(tx, repoFullName, prNumber)
}

// NextRoundNumberTx 一轮的轮次号：repo+pr 下 runs 与 reviews 已有最大轮次 +1。
func NextRoundNumberTx(tx *gorm.DB, repoFullName string, prNumber int) (int, error) {
	var maxRun, maxRev *int
	if err := tx.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND pr_number = ?", repoFullName, prNumber).
		Select("MAX(round_number)").Scan(&maxRun).Error; err != nil {
		return 0, err
	}
	if err := tx.Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ?", repoFullName, prNumber).
		Select("MAX(round_number)").Scan(&maxRev).Error; err != nil {
		return 0, err
	}
	next := 1
	if maxRun != nil && *maxRun >= next {
		next = *maxRun + 1
	}
	if maxRev != nil && *maxRev >= next {
		next = *maxRev + 1
	}
	return next, nil
}
