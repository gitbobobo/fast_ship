package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GitHub PR 自动评审（INT-93）支撑表的仓储。全部走调用方传入的 tx/db，
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
// 主键含验签身份，同一（触发器, 仓库, PR）的不同身份各占一行、互不覆盖；
// 冲突按主键全列覆盖推进（fire_at/action/head_sha 同步前进）。显式 OnConflict
// 而不是 Save：config 身份的 app_user_id 是空串（主键零值），Save 会把整行
// 当「无主键」直接 INSERT，窗口内第二次推送就撞主键。
func (r *GitHubPRDebounceRepository) UpsertTx(tx *gorm.DB, d *model.GitHubPRDebounce) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "trigger_id"}, {Name: "repo_full_name"}, {Name: "pr_number"}, {Name: "app_user_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"user_id", "project_id", "installation_id", "action", "head_sha",
			"pr_title", "pr_url", "base_ref", "head_ref", "fire_at", "updated_at",
		}),
	}).Create(d).Error
}

// ListDueTx 取到期待触发的防抖行。
func (r *GitHubPRDebounceRepository) ListDueTx(db *gorm.DB, now time.Time, limit int) ([]model.GitHubPRDebounce, error) {
	var items []model.GitHubPRDebounce
	err := db.Where("fire_at <= ?", now).Order("fire_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

// DeleteTx 消费防抖行（到点开轮后删除；条件命中 0 行=已被并发消费）。
// 键按（触发器, 验签身份）：同一自动化的不同触发器、同一仓库+PR 的不同身份
// 各有自己的窗口，删除不得越界。
func (r *GitHubPRDebounceRepository) DeleteTx(tx *gorm.DB, triggerID, repoFullName string, prNumber int, appUserID string) error {
	return tx.Where("trigger_id = ? AND repo_full_name = ? AND pr_number = ? AND app_user_id = ?",
		triggerID, repoFullName, prNumber, appUserID).Delete(&model.GitHubPRDebounce{}).Error
}

// DeleteStaleSHATx 丢掉该身份下旧 SHA 的防抖行（opened 规则的窗口不能在
// synchronize 新 SHA 之后再开一轮）。按验签身份分区：其他身份的窗口不受影响。
func (r *GitHubPRDebounceRepository) DeleteStaleSHATx(tx *gorm.DB, repoFullName string, prNumber int, currentSHA, appUserID string) error {
	return tx.Where("repo_full_name = ? AND pr_number = ? AND head_sha <> ? AND app_user_id = ?",
		repoFullName, prNumber, currentSHA, appUserID).Delete(&model.GitHubPRDebounce{}).Error
}

type GitHubPRReviewRepository struct {
	db *gorm.DB
}

func NewGitHubPRReviewRepository(db *gorm.DB) *GitHubPRReviewRepository {
	return &GitHubPRReviewRepository{db: db}
}

// UpsertTx 每（PR，验签身份）一行的同步账：推进轮次/SHA 并置 sync_pending
// （等待事务外同步）。已有 comment_id 必须保留——新一轮开轮时调用方不带评论
// ID，Save 全列覆盖会把它清零，随后只能靠标记找回，标记失效就会再发一条顶层
// 评论。
func (r *GitHubPRReviewRepository) UpsertTx(tx *gorm.DB, rev *model.GitHubPRReview) error {
	var existing model.GitHubPRReview
	err := tx.Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ?",
		rev.RepoFullName, rev.PRNumber, rev.AppUserID).
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
	// 更新条件显式带全三列主键：经 &existing 走 GORM 主键条件时，空串身份
	// （config）会被当作零值从条件里省掉，退化成按 (repo, pr) 更新所有身份行，
	// config 开轮会污染用户行的 installation/SHA/轮次/同步状态。
	return tx.Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ?",
			rev.RepoFullName, rev.PRNumber, rev.AppUserID).
		Updates(map[string]any{
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
	// 只取空身份（= 部署级 App）：INT-119 遗留的非空身份行属于已停用的用户级 App，
	// 同步会用现在的共享 App 去发它们的评论/check。在查询里排除而不是在循环里跳过——
	// 否则一堆遗留行会占满每轮批次，正常行永远轮不到。
	err := db.Where("sync_pending = ? AND app_user_id = ?", true, "").
		Order("updated_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

// ClearPendingSync 同步成功后清位。只清开始同步时的 sync_epoch：期间又有
// webhook/终态会 +1，本帧 0 行，保留 pending 给下轮。不用 updated_at 相等——
// 写 comment_id 也会碰 updated_at，会把清位条件打空。
func (r *GitHubPRReviewRepository) ClearPendingSync(ctx context.Context, rev *model.GitHubPRReview, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ? AND sync_epoch = ? AND sync_pending = ?",
			rev.RepoFullName, rev.PRNumber, rev.AppUserID, rev.SyncEpoch, true).
		Updates(map[string]any{"sync_pending": false, "sync_error": "", "updated_at": now}).Error
}

// SetSyncError 同步失败记错误（下轮 tick 重试，不改运行终态）。同样只写本帧。
func (r *GitHubPRReviewRepository) SetSyncError(ctx context.Context, rev *model.GitHubPRReview, syncErr string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&model.GitHubPRReview{}).
		Where("repo_full_name = ? AND pr_number = ? AND app_user_id = ? AND sync_epoch = ?",
			rev.RepoFullName, rev.PRNumber, rev.AppUserID, rev.SyncEpoch).
		Updates(map[string]any{"sync_error": syncErr, "updated_at": now}).Error
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

// RecordSeenSHATx 记下该身份已受理的 head SHA；重复插入忽略。prUpdatedAt 是
// payload 里 pull_request.updated_at，用来拒绝比已受理 head 更旧的迟到
// synchronize。已见账按验签身份分区（空串=config 全局身份）。
func (r *GitHubPRReviewRepository) RecordSeenSHATx(tx *gorm.DB, repoFullName string, prNumber int, sha, appUserID string, now time.Time, prUpdatedAt *time.Time) error {
	err := tx.Create(&model.GitHubPRSeenSHA{
		RepoFullName: repoFullName, PRNumber: prNumber, HeadSHA: sha, AppUserID: appUserID,
		SeenAt: now, PRUpdatedAt: prUpdatedAt,
	}).Error
	if isUniqueConstraintError(err) {
		return nil
	}
	return err
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
