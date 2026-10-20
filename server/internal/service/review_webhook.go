package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// webhook 触发（INT-135）：事务内纯库操作。任何忽略场景都返回 nil（handler
// 恒 202），GitHub/T3 调用只允许出现在调度阶段。

// reviewDebounceSeconds 防抖窗口（固定，不提供配置）。
const reviewDebounceSeconds = 60

// prWebhookPayload pull_request 事件的最小字段面（其余字段原样丢弃）。
type prWebhookPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	PR     struct {
		Draft     bool       `json:"draft"`
		HTMLURL   string     `json:"html_url"`
		Title     string     `json:"title"`
		UpdatedAt *time.Time `json:"updated_at"`
		Head      struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
			Repo struct {
				Fork     bool   `json:"fork"`
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handleWebhookTx delivery 已落账后的触发处理（同一事务内）。忽略场景一律
// 返回 nil——未关联仓库/未勾选动作/draft/fork/过期 SHA 不向 GitHub 区分。
func (s *ReviewService) handleWebhookTx(tx *gorm.DB, event string, body []byte, now time.Time) error {
	if event != "pull_request" {
		return nil
	}
	var p prWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	if p.Number <= 0 || p.Repository.FullName == "" || p.PR.Head.SHA == "" {
		return nil
	}
	// PR 关闭/合并：取消该 PR 的防抖与在途运行（含排队），不再开轮。关闭事件
	// 不区分触发条件——继续审一个已关闭的 PR 没有意义。
	if p.Action == "closed" {
		project, err := s.findEnabledProjectByRepoTx(tx, p.Repository.FullName)
		if err != nil || project == nil {
			return err
		}
		repo := strings.ToLower(p.Repository.FullName)
		if err := s.supersedePRRunsTx(tx, project.ID, repo, p.Number, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			project.ID, repo, p.Number).Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		// 保留账本 head（记录页仍显示最后审过的提交），只清 pending 并推进
		// epoch，让在途同步停下。
		return tx.Model(&model.ReviewPullRequest{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", project.ID, repo, p.Number).
			Updates(map[string]any{
				"sync_pending": false,
				"sync_epoch":   gorm.Expr("sync_epoch + 1"),
				"updated_at":   now,
			}).Error
	}
	if !reviewTriggerableAction(p.Action) {
		return nil
	}
	// draft 与 fork（head 仓库 ≠ base 仓库）一律忽略。
	if p.PR.Draft || p.PR.Head.Repo.Fork ||
		!strings.EqualFold(p.PR.Head.Repo.FullName, p.PR.Base.Repo.FullName) {
		return nil
	}

	// 反查启用项目：同仓库最多一个 enabled；project_review_settings 联查 projects。
	project, settings, err := s.findEnabledReviewProjectTx(tx, p.Repository.FullName)
	if err != nil || project == nil {
		return err
	}

	// 写库键统一小写：payload 里是 GitHub 规范大小写，rerun 侧用的是项目里
	// 用户输入的原样大小写——SQLite BINARY 排序下大小写不同即两套账本，
	// 旧 SHA 取消、去重、幂等全部失效。
	repo, prNumber, headSHA := strings.ToLower(p.Repository.FullName), p.Number, p.PR.Head.SHA

	// 当前 head：未开火的防抖行优先，否则用总账 head。
	currentHead, err := s.currentAcceptedHeadTx(tx, project.ID, repo, prNumber)
	if err != nil {
		return err
	}

	// 过期 SHA 判定：payload.updated_at ≤ 已受理 head 的 pr_updated_at → 忽略。
	// 账本按项目分区：seen/总账/运行一律带 project_id，其他项目的同仓库
	// 残留行不进入本项目判定。
	if currentHead != "" {
		var accepted model.ReviewSeenSHA
		err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			project.ID, repo, prNumber, currentHead).First(&accepted).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if accepted.PRUpdatedAt != nil && p.PR.UpdatedAt != nil &&
			!p.PR.UpdatedAt.After(*accepted.PRUpdatedAt) {
			return nil
		}
	}

	// 该 SHA 已受理过但不是当前 head（force-push 回旧 SHA）→ 忽略。
	if headSHA != currentHead {
		var n int64
		if err := tx.Model(&model.ReviewSeenSHA{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
				project.ID, repo, prNumber, headSHA).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	// 迟到的 opened（已有受理 head）→ 忽略。
	if p.Action == "opened" && currentHead != "" {
		return nil
	}
	// 该 SHA 已有运行 → 忽略。
	var runCount int64
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ?",
			project.ID, repo, prNumber, headSHA).
		Count(&runCount).Error; err != nil {
		return err
	}
	if runCount > 0 {
		return nil
	}

	// 受理：登记 seen SHA（重复防御交给主键冲突）。
	seen := &model.ReviewSeenSHA{
		ProjectID:    project.ID,
		RepoFullName: repo, PRNumber: prNumber, HeadSHA: headSHA,
		PRUpdatedAt: p.PR.UpdatedAt, AcceptedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(seen).Error; err != nil {
		return err
	}

	// 新 head ≠ 当前 head：取消该 PR 全部未终态运行并消费令牌、删防抖行，并把
	// 总账 head 立刻推进到新 SHA（清掉旧轮残留的 pending 并 epoch+1）。防抖窗口
	// 内不再同步旧轮：否则调度会拿新 head 的 PR 描述给旧 SHA 建视觉运行，而
	// 这些运行不会被随后开轮的新 SHA 取消。
	if headSHA != currentHead {
		if err := s.supersedePRRunsTx(tx, project.ID, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			project.ID, repo, prNumber).
			Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		if err := s.advancePRHeadTx(tx, project, repo, prNumber, headSHA, p.Installation.ID, now); err != nil {
			return err
		}
	}

	// 未勾选的动作不开新轮、也不发 GitHub 产出：只登记 seen、取消旧运行、
	// 删防抖、推进 head（同 SHA 的下一次已勾选动作不会被当成回旧 SHA 忽略）。
	if !reviewTriggerEnabled(settings, p.Action) {
		return nil
	}
	// sync_pending 由开轮（防抖到期后的 openRoundTx）置位：此刻还没有新轮的
	// 运行，提前标 pending 会让调度拿空运行集写「审查中」。

	// 写/更新防抖行：窗口内新推送覆盖 head/action/meta 并推后 fire_at。
	debounce := &model.ReviewDebounce{
		ProjectID:      project.ID,
		RepoFullName:   repo,
		PRNumber:       prNumber,
		HeadSHA:        headSHA,
		Action:         p.Action,
		InstallationID: p.Installation.ID,
		PRTitle:        p.PR.Title,
		PRURL:          p.PR.HTMLURL,
		BaseRef:        p.PR.Base.Ref,
		HeadRef:        p.PR.Head.Ref,
		FireAt:         now.Add(reviewDebounceSeconds * time.Second),
		UpdatedAt:      now,
	}
	return tx.Save(debounce).Error
}

// splitRepoName "owner/name" → 两段；不合法返回空串。
func splitRepoName(full string) (string, string) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok {
		return "", ""
	}
	return owner, name
}

func equalFoldStr(a, b string) bool { return strings.EqualFold(a, b) }

// reviewTriggerableAction 只有这四种 action 会进入判定。
func reviewTriggerableAction(action string) bool {
	switch action {
	case "opened", "synchronize", "ready_for_review", "reopened":
		return true
	}
	return false
}

// reviewTriggerEnabled 动作对应的设置勾选位。
func reviewTriggerEnabled(s *model.ProjectReviewSettings, action string) bool {
	switch action {
	case "opened":
		return s.TriggerOpened
	case "synchronize":
		return s.TriggerSynchronize
	case "ready_for_review":
		return s.TriggerReadyForReview
	case "reopened":
		return s.TriggerReopened
	}
	return false
}

// findEnabledProjectByRepoTx 只取启用审查的项目（关闭事件不需要设置内容）。
func (s *ReviewService) findEnabledProjectByRepoTx(tx *gorm.DB, repoFullName string) (*model.Project, error) {
	project, _, err := s.findEnabledReviewProjectTx(tx, repoFullName)
	return project, err
}

// findEnabledReviewProjectTx 按仓库全名（大小写不敏感）找到启用审查的项目
// 及其设置；无命中返回 (nil, nil, nil)。联查 projects 保证仓库归属仍有效。
func (s *ReviewService) findEnabledReviewProjectTx(tx *gorm.DB, repoFullName string) (*model.Project, *model.ProjectReviewSettings, error) {
	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, nil, nil
	}
	var rows []model.ProjectReviewSettings
	if err := tx.
		Joins("JOIN projects p ON p.id = project_review_settings.project_id").
		Where("project_review_settings.enabled AND lower(p.github_owner) = ? AND lower(p.github_repo) = ?",
			strings.ToLower(owner), strings.ToLower(repo)).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}
	settings := &rows[0]
	var project model.Project
	if err := tx.Where("id = ?", settings.ProjectID).First(&project).Error; err != nil {
		return nil, nil, err
	}
	return &project, settings, nil
}

// currentAcceptedHeadTx 当前已受理 head：未开火的防抖行优先，否则总账 head_sha。
func (s *ReviewService) currentAcceptedHeadTx(tx *gorm.DB, projectID, repo string, prNumber int) (string, error) {
	var debounce model.ReviewDebounce
	err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		projectID, repo, prNumber).First(&debounce).Error
	switch {
	case err == nil:
		return debounce.HeadSHA, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", err
	}
	var ledger model.ReviewPullRequest
	err = tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		projectID, repo, prNumber).First(&ledger).Error
	switch {
	case err == nil:
		return ledger.HeadSHA, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", nil
	default:
		return "", err
	}
}

// supersedePRRunsTx 取消该 PR 全部未终态运行（含 queued/dispatched/running），
// error_code=superseded；运行令牌一并消费。线程中断由调度阶段补发。
// 只动本项目的运行——账本分区后其他项目的同仓库运行由各自的接管/超时收敛。
func (s *ReviewService) supersedePRRunsTx(tx *gorm.DB, projectID, repo string, prNumber int, now time.Time) error {
	active := []model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}
	var runIDs []string
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND status IN ?",
			projectID, repo, prNumber, active).
		Pluck("id", &runIDs).Error; err != nil {
		return err
	}
	if len(runIDs) == 0 {
		return nil
	}
	if err := tx.Model(&model.ReviewRun{}).
		Where("id IN ? AND status IN ?", runIDs, active).
		Updates(map[string]any{
			"status":      model.ReviewRunCancelled,
			"error_code":  "superseded",
			"finished_at": now,
			"updated_at":  now,
		}).Error; err != nil {
		return err
	}
	return tx.Model(&model.ReviewRunToken{}).
		Where("run_id IN ? AND consumed_at IS NULL", runIDs).
		Update("consumed_at", now).Error
}

// advancePRHeadTx 只推进总账 head（不置 sync_pending）：动作未勾选时不该
// 发布旧轮结论，但 head 必须前进，供后续已勾选动作按同一 SHA 正常开轮。
func (s *ReviewService) advancePRHeadTx(tx *gorm.DB, project *model.Project, repo string, prNumber int, headSHA string, installationID int64, now time.Time) error {
	var ledger model.ReviewPullRequest
	err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		project.ID, repo, prNumber).First(&ledger).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&model.ReviewPullRequest{
			ProjectID: project.ID, RepoFullName: repo, PRNumber: prNumber,
			UserID: project.UserID, InstallationID: installationID,
			HeadSHA: headSHA, UpdatedAt: now,
		}).Error
	case err != nil:
		return err
	}
	// 前进 head 时清掉可能残留的 pending 并推进 epoch：旧轮的重试不能拿新
	// head 去发布（此时还没有新轮的运行），在途同步也会因 epoch 不符中止。
	updates := map[string]any{
		"head_sha":     headSHA,
		"sync_pending": false,
		"sync_epoch":   gorm.Expr("sync_epoch + 1"),
		"updated_at":   now,
	}
	if installationID != 0 {
		updates["installation_id"] = installationID
	}
	return tx.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			project.ID, repo, prNumber).
		Updates(updates).Error
}
