package service

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 开轮与排队（INT-135）：防抖到点后在同一短事务里开轮——轮次分配、总账
// 推进、每个代码组合一行运行（唯一索引幂等）、同事务排队提升。

// 审查超时：代码 1h、视觉 30m（自提升为 dispatched 的时刻起算）。
const (
	reviewCodeTimeout   = time.Hour
	reviewVisualTimeout = 30 * time.Minute
)

// reviewMaxInflightPerUser 每用户同时在途（dispatched+running）的审查运行上限。
const reviewMaxInflightPerUser = 5

// reviewTimeout 按运行类型取超时。
func reviewTimeout(kind model.ReviewKind) time.Duration {
	if kind == model.ReviewKindVisual {
		return reviewVisualTimeout
	}
	return reviewCodeTimeout
}

// openRoundTx 在一个事务里开一轮代码审查（视觉运行不在此创建——它们由代码
// 全部干净落地后的视觉衔接阶段按条件创建）。
// 轮次：manual 恒新轮（同 head 也要新轮，否则去重索引吃掉）；其余情况同
// (project,repo,pr,head,集成) 已有运行沿其 round_number，否则 1+max(round)。
// 集成身份取设置行的归一快照（防抖消费/rerun 都在同事务重读过设置行）。
// meta 由防抖行/rerun 传入的 PR 快照字段。
func (s *ReviewService) openRoundTx(
	tx *gorm.DB,
	project *model.Project,
	settings *model.ProjectReviewSettings,
	repo string, prNumber int,
	headSHA, action, prTitle, prURL, baseRef, headRef string,
	installationID int64,
	prUpdatedAt *time.Time,
	now time.Time,
) (int, error) {
	integrationID := reviewIntegrationID(settings.IntegrationID)
	round, err := nextReviewRoundTx(tx, integrationID, project.ID, repo, prNumber, headSHA, action)
	if err != nil {
		return 0, err
	}

	// 总账推进到本轮 head/round 并置 sync_pending（无行则建）。总账按
	// (项目, 集成) 分区：只读本行，其他项目/其他集成的同仓库账本不受影响。
	var ledger model.ReviewPullRequest
	err = tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		project.ID, repo, prNumber, integrationID).First(&ledger).Error
	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		return 0, err
	case err != nil: // 不存在 → 建
		ledger = model.ReviewPullRequest{
			RepoFullName: repo, PRNumber: prNumber,
			ProjectID: project.ID, UserID: project.UserID,
			IntegrationID: integrationID,
		}
	}
	ledger.ProjectID = project.ID
	ledger.UserID = project.UserID
	ledger.IntegrationID = integrationID
	// 与 markPRSyncPendingTx 同口径：payload 未带 installation.id（0）时保留
	// 总账旧值，运行行也用这个生效值。
	if installationID == 0 {
		installationID = ledger.InstallationID
	}
	ledger.InstallationID = installationID
	// commit status 按 SHA 归属：开新轮后旧轮发布的结论不再是新轮的去重依据，
	// 无条件清空，让同步重新发 pending/结论。
	ledger.CodeStatus = ""
	ledger.VisualStatus = ""
	ledger.HeadSHA = headSHA
	// 受理时刻的实拉快照随 head 一起推进（滞后时序判定基准，见模型注释）。
	ledger.PRUpdatedAt = prUpdatedAt
	ledger.RoundNumber = round
	ledger.SyncPending = true
	ledger.SyncEpoch = ledger.SyncEpoch + 1
	ledger.UpdatedAt = now
	// 主键 (project_id,repo,pr,集成) 存在即更新，否则插入——Save 两种情况都覆盖。
	if err := tx.Save(&ledger).Error; err != nil {
		return 0, err
	}

	// 每个代码组合一行运行；唯一索引 (project_id,repo,pr,head,round,kind,组合)
	// 吃掉重复。
	combos, err := parseCombinations(settings.CodeCombinationsJSON)
	if err != nil {
		return 0, err
	}
	for i := range combos {
		c := &combos[i]
		t3ProjectID := ""
		if c.T3ProjectId != nil {
			t3ProjectID = *c.T3ProjectId
		}
		run := &model.ReviewRun{
			ID:               uuid.NewString(),
			UserID:           project.UserID,
			ProjectID:        project.ID,
			Kind:             model.ReviewKindCode,
			RepoFullName:     repo,
			PRNumber:         prNumber,
			HeadSHA:          headSHA,
			RoundNumber:      round,
			InstallationID:   installationID,
			IntegrationID:    integrationID,
			TriggerAction:    action,
			PRTitle:          prTitle,
			PRURL:            prURL,
			BaseRef:          baseRef,
			HeadRef:          headRef,
			EnvironmentID:    c.EnvironmentId,
			T3ProjectID:      t3ProjectID,
			InstanceID:       c.InstanceId,
			Model:            c.Model,
			ModelOptionsJSON: canonicalOptionsJSON(c.Options),
			ThreadID:         uuid.NewString(),
			LaunchCommandID:  uuid.NewString(),
			InitialMessageID: uuid.NewString(),
			Status:           model.ReviewRunQueued,
			DeadlineAt:       now, // 占位：提升为 dispatched 时按提升时刻重设
			WorktreeState:    model.ReviewWorktreeNone,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run).Error; err != nil {
			return 0, err
		}
	}

	// 同事务排队提升：刚开轮的运行可立即占名额。
	return round, s.promoteQueuedRunsTx(tx, project.UserID, now)
}

// nextReviewRoundTx 轮次分配：manual 与 comment（INT-179 评论唤醒）恒
// 1+max——同 head 也要新轮，否则同 (project,repo,pr,head,round,组合) 唯一
// 索引吃掉运行行，唤醒不会产生任何新运行；其余同 (project,repo,pr,head,
// 集成) 已有运行沿其 round_number，否则 1+max。轮次只在 (项目, 集成) 内
// 分配——接管或改绑后新键不受旧键已跑轮次的影响。
func nextReviewRoundTx(tx *gorm.DB, integrationID, projectID, repo string, prNumber int, headSHA, action string) (int, error) {
	if action != "manual" && action != "comment" {
		var rounds []int
		if err := tx.Model(&model.ReviewRun{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
				projectID, repo, prNumber, headSHA, integrationID).
			Limit(1).Pluck("round_number", &rounds).Error; err != nil {
			return 0, err
		}
		if len(rounds) > 0 {
			return rounds[0], nil
		}
	}
	var maxRound *int
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			projectID, repo, prNumber, integrationID).
		Select("MAX(round_number)").Scan(&maxRound).Error; err != nil {
		return 0, err
	}
	if maxRound == nil {
		return 1, nil
	}
	return *maxRound + 1, nil
}

// promoteQueuedRunsTx 同事务的排队提升：在途 <5 时按创建时间把最老 queued
// 提升为 dispatched，deadline 按提升时刻 + kind 超时重设。
func (s *ReviewService) promoteQueuedRunsTx(tx *gorm.DB, userID string, now time.Time) error {
	var active int64
	if err := tx.Model(&model.ReviewRun{}).
		Where("user_id = ? AND status IN ?", userID,
			[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
		Count(&active).Error; err != nil {
		return err
	}
	room := reviewMaxInflightPerUser - int(active)
	if room <= 0 {
		return nil
	}
	var queued []model.ReviewRun
	if err := tx.Where("user_id = ? AND status = ?", userID, model.ReviewRunQueued).
		Order("created_at ASC, id ASC").Limit(room).Find(&queued).Error; err != nil {
		return err
	}
	for _, run := range queued {
		deadline := now.Add(reviewTimeout(run.Kind))
		res := tx.Model(&model.ReviewRun{}).
			Where("id = ? AND status = ?", run.ID, model.ReviewRunQueued).
			Updates(map[string]any{
				"status":      model.ReviewRunDispatched,
				"deadline_at": deadline,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
	}
	return nil
}
