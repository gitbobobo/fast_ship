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

// prWebhookPayload pull_request 事件的最小字段面（其余字段原样丢弃）。GitHub
// 与 Forgejo 的 pull_request payload 同形，共用这一份解析。
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

// reviewWebhookTrigger 触发事实的中性形态（payload 解析之后）：webhook 与
// 轮询补漏共用同一条受理路径，判定逻辑只维护一份。
type reviewWebhookTrigger struct {
	action       string
	number       int
	repoFullName string // base/repository 全名（payload 大小写原样）
	headSHA      string
	draft        bool
	headRepoFull string // head 仓库全名
	baseRepoFull string // base 仓库全名
	title        string
	htmlURL      string
	baseRef      string
	headRef      string
	updatedAt    *time.Time
	// tiedBase 复核绑定：调用方现拉实时 PR 复核「同刻异 head」歧义时
	// 观察到的已受理 head（空串 = 未复核）。重进事务时总账必须仍停在
	// 该 head 才放行——复核与重进之间若有第三个同秒 push 抢先受理，
	// tiedBase 失配，守卫再次报歧义交调用方按新快照重验，复核结论不
	// 能跨一次总账前进沿用（否则无条件旁路照样回滚到新 head）。
	tiedBase string
	// installationID 仅 GitHub 有：App 安装快照。Forgejo 恒 0（无安装语义，
	// advancePRHeadTx 对 0 不覆盖既有快照）。
	installationID int64
}

// errTiedHeadAmbiguous 触发 head 与已受理 head 同刻异 SHA：直接丢弃丢的
// 可能是真 head（R17 修复面），直接受理又会把总账回滚到旧 head、取消新
// head 的在途轮次并把新 head 留成「seen 但非当前」永久漏审——交给调用方
// 现拉实时 PR 分辨（webhook 与轮询共用此歧义），复核通过以 tiedBase 绑定
// 观察到的总账 head 重进，实时数据不走同刻守卫。
var errTiedHeadAmbiguous = errors.New("trigger head tied with accepted head")

// tiedHeadError 同刻歧义哨兵：携带守卫观察到的已受理 head，调用方复核后
// 以此绑定重进（复核结论只对该快照有效）。
type tiedHeadError struct{ observedHead string }

func (e *tiedHeadError) Error() string {
	return "trigger head tied with accepted head " + e.observedHead
}

func (e *tiedHeadError) Is(target error) bool { return target == errTiedHeadAmbiguous }

// normalizeReviewAction 平台动作名归一：Forgejo/Gitea 的推送动作叫
// synchronized（GitHub 是 synchronize），触发面（可受理判定、勾选位映射、
// 防抖行、开轮记录）统一按 GitHub 拼法落库。GitHub 的动作集合里没有
// synchronized，此归一对 GitHub 入口零影响。
func normalizeReviewAction(action string) string {
	if action == "synchronized" {
		return "synchronize"
	}
	return action
}

// handleWebhookTx delivery 已落账后的触发处理（同一事务内）。忽略场景一律
// 返回 nil——未关联仓库/未勾选动作/draft/fork/过期 SHA 不向平台区分。
// integrationID 是触发链路的集成身份（归一值）：GitHub 入口固定传内置
// github-default，Forgejo 入口传 forge_integrations.id；反查命中后所有键控
// 读写（seen/总账/运行/防抖）都以它过滤——反查既然按它命中设置行，其余键
// 与设置行必然同值。
func (s *ReviewService) handleWebhookTx(tx *gorm.DB, integrationID string, event string, body []byte, now time.Time, tiedBase string) error {
	// Forgejo/Gitea 的推送更新是独立订阅项 pull_request_sync（payload 与
	// pull_request 同形，action=synchronized）。新实例的 X-Forgejo-Event
	// 头送归一名 pull_request，但旧 Gitea/自定义投递会把细型放进 Event
	// 头——两个名字都受理，GitHub 从不发此名所以 GitHub 面零影响。webhook
	// 订阅仍要把 Pull Request Sync 一并勾上（文档有指引），否则平台根本
	// 不投递这条。
	if event != "pull_request" && event != "pull_request_sync" {
		return nil
	}
	var p prWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	return s.handleReviewTriggerTx(tx, integrationID, &reviewWebhookTrigger{
		action:         p.Action,
		number:         p.Number,
		repoFullName:   p.Repository.FullName,
		headSHA:        p.PR.Head.SHA,
		draft:          p.PR.Draft,
		headRepoFull:   p.PR.Head.Repo.FullName,
		baseRepoFull:   p.PR.Base.Repo.FullName,
		title:          p.PR.Title,
		htmlURL:        p.PR.HTMLURL,
		baseRef:        p.PR.Base.Ref,
		headRef:        p.PR.Head.Ref,
		updatedAt:      p.PR.UpdatedAt,
		tiedBase:       tiedBase,
		installationID: p.Installation.ID,
	}, now)
}

// handleReviewTriggerTx 受理判定与写库（同一事务内）：closed 取消在途、draft/
// fork 忽略、过期/重复 SHA 去重、登记 seen、推进总账 head、写防抖行。轮询
// 补漏构造同一形态的 trigger 走这条路径，seen 去重保证同一 SHA 只受理一次。
func (s *ReviewService) handleReviewTriggerTx(tx *gorm.DB, integrationID string, t *reviewWebhookTrigger, now time.Time) error {
	t.action = normalizeReviewAction(t.action)
	if t.number <= 0 || t.repoFullName == "" || t.headSHA == "" {
		return nil
	}
	// PR 关闭/合并：取消该 PR 的防抖与在途运行（含排队），不再开轮。关闭事件
	// 不区分触发条件——继续审一个已关闭的 PR 没有意义。
	if t.action == "closed" {
		project, err := s.findEnabledProjectByRepoTx(tx, integrationID, t.repoFullName)
		if err != nil || project == nil {
			return err
		}
		repo := strings.ToLower(t.repoFullName)
		if err := s.supersedePRRunsTx(tx, integrationID, project.ID, repo, t.number, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, t.number, integrationID).Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		// 保留账本 head（记录页仍显示最后审过的提交），只清 pending 并推进
		// epoch，让在途同步停下。
		return tx.Model(&model.ReviewPullRequest{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
				project.ID, repo, t.number, integrationID).
			Updates(map[string]any{
				"sync_pending": false,
				"sync_epoch":   gorm.Expr("sync_epoch + 1"),
				"updated_at":   now,
			}).Error
	}
	if !reviewTriggerableAction(t.action) {
		return nil
	}
	// draft 与跨仓 fork 一律忽略。判定用 head/base 仓库全名比对，不用
	// head.repo.fork 标志：本仓库自身是 fork 时该标志恒 true，同仓分支的
	// PR 会被误杀；已删 fork 的 head.repo 为 null，全名不等也命中。
	if t.draft || !strings.EqualFold(t.headRepoFull, t.baseRepoFull) {
		return nil
	}

	// 反查启用项目：同一集成内同仓库最多一个 enabled；project_review_settings
	// 联查 projects。
	project, settings, err := s.findEnabledReviewProjectTx(tx, integrationID, t.repoFullName)
	if err != nil || project == nil {
		return err
	}

	// 写库键统一小写：payload 里是平台规范大小写，rerun 侧用的是项目里
	// 用户输入的原样大小写——SQLite BINARY 排序下大小写不同即两套账本，
	// 旧 SHA 取消、去重、幂等全部失效。
	repo, prNumber, headSHA := strings.ToLower(t.repoFullName), t.number, t.headSHA

	// 当前 head：未开火的防抖行优先，否则用总账 head。
	currentHead, err := s.currentAcceptedHeadTx(tx, integrationID, project.ID, repo, prNumber)
	if err != nil {
		return err
	}

	// 过期 SHA 判定：trigger.updated_at 严格早于已受理 head 的 pr_updated_at
	// → 忽略（迟到的旧快照）；时间戳相等时按 head 区分——Forgejo updated_at
	// 是秒级粒度，同一秒内连推两个不同 head 会带相同时间戳，「同时刻不同
	// head」是歧义：可能真新（同秒连推）也可能真旧（乱序投递/旧快照），
	// 未复核一律交调用方现拉分辨，丢了或盲收都会漏审/回滚总账；同时刻
	// 同 head 是同一状态的重复投递，早退省一次去重查询。
	// 账本按 (项目, 集成) 分区：seen/总账/运行一律带 project_id 与
	// integration_id，其他项目/其他集成的同仓库残留行不进入本项目判定。
	if currentHead != "" {
		var accepted model.ReviewSeenSHA
		err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
			project.ID, repo, prNumber, currentHead, integrationID).First(&accepted).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if accepted.PRUpdatedAt != nil && t.updatedAt != nil {
			// 同刻异 head：tiedBase 必须命中当前已受理 head 才算复核有效；
			// 总账在复核与重进之间前进了就重新报歧义，按新快照再验。
			if t.updatedAt.Equal(*accepted.PRUpdatedAt) && headSHA != currentHead && t.tiedBase != currentHead {
				return &tiedHeadError{observedHead: currentHead}
			}
			if t.updatedAt.Before(*accepted.PRUpdatedAt) ||
				(t.updatedAt.Equal(*accepted.PRUpdatedAt) && headSHA == currentHead) {
				return nil
			}
		}
	}

	// 该 SHA 已受理过但不是当前 head（force-push 回旧 SHA）→ 忽略。
	if headSHA != currentHead {
		var n int64
		if err := tx.Model(&model.ReviewSeenSHA{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
				project.ID, repo, prNumber, headSHA, integrationID).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	// 迟到的 opened（已有受理 head）→ 忽略。轮询补漏的动作按「从未受理过 →
	// opened，已受理过 → synchronize」归类（见 review_forgejo_poll.go），这条
	// 守卫同时兜住补漏读 head 与受理落库之间 webhook 抢先受理的竞态窗口。
	if t.action == "opened" && currentHead != "" {
		return nil
	}
	// 该 SHA 已有运行 → 忽略。
	var runCount int64
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
			project.ID, repo, prNumber, headSHA, integrationID).
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
		IntegrationID: integrationID,
		PRUpdatedAt:   t.updatedAt, AcceptedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(seen).Error; err != nil {
		return err
	}

	// 新 head ≠ 当前 head：取消该 PR 全部未终态运行并消费令牌、删防抖行，并把
	// 总账 head 立刻推进到新 SHA（清掉旧轮残留的 pending 并 epoch+1）。防抖窗口
	// 内不再同步旧轮：否则调度会拿新 head 的 PR 描述给旧 SHA 建视觉运行，而
	// 这些运行不会被随后开轮的新 SHA 取消。
	if headSHA != currentHead {
		if err := s.supersedePRRunsTx(tx, integrationID, project.ID, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, integrationID).
			Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		if err := s.advancePRHeadTx(tx, project, repo, prNumber, headSHA, integrationID, t.installationID, now); err != nil {
			return err
		}
	}

	// 未勾选的动作不开新轮、也不发平台产出：只登记 seen、取消旧运行、
	// 删防抖、推进 head（同 SHA 的下一次已勾选动作不会被当成回旧 SHA 忽略）。
	if !reviewTriggerEnabled(settings, t.action) {
		return nil
	}
	// sync_pending 由开轮（防抖到期后的 openRoundTx）置位：此刻还没有新轮的
	// 运行，提前标 pending 会让调度拿空运行集写「审查中」。

	// 写/更新防抖行：窗口内新推送覆盖 head/action/meta 并推后 fire_at。
	debounce := &model.ReviewDebounce{
		ProjectID:      project.ID,
		RepoFullName:   repo,
		PRNumber:       prNumber,
		IntegrationID:  integrationID,
		HeadSHA:        headSHA,
		Action:         t.action,
		InstallationID: t.installationID,
		PRTitle:        t.title,
		PRURL:          t.htmlURL,
		BaseRef:        t.baseRef,
		HeadRef:        t.headRef,
		FireAt:         now.Add(reviewDebounceSeconds * time.Second),
		UpdatedAt:      now,
	}
	return tx.Save(debounce).Error
}

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
func (s *ReviewService) findEnabledProjectByRepoTx(tx *gorm.DB, integrationID, repoFullName string) (*model.Project, error) {
	project, _, err := s.findEnabledReviewProjectTx(tx, integrationID, repoFullName)
	return project, err
}

// findEnabledReviewProjectTx 按 (integration_id, 仓库全名大小写不敏感) 找到
// 启用审查的项目及其设置；无命中返回 (nil, nil, nil)。按设置行的
// (integration_id, repo_full_name) 快照反查：同仓库唯一索引建在这两列上，
// 判定与约束用同两列；快照与项目行 live 值由 ProjectRepository.Update /
// UpdateSettings 同事务保持一致（读取口径见 docs/internal/reviews.md）。
// integrationID 传归一值（GitHub 入口固定 github-default，Forgejo 入口传真实
// 集成 id）：绑定 GitHub 的项目快照是 github-default，绑定 Forgejo 的是真实
// 集成 id，两边的同名仓库投递互不误触。
func (s *ReviewService) findEnabledReviewProjectTx(tx *gorm.DB, integrationID, repoFullName string) (*model.Project, *model.ProjectReviewSettings, error) {
	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, nil, nil
	}
	var rows []model.ProjectReviewSettings
	if err := tx.
		Where("project_review_settings.enabled AND project_review_settings.repo_full_name = ? AND project_review_settings.integration_id = ?",
			strings.ToLower(repoFullName), integrationID).
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
func (s *ReviewService) currentAcceptedHeadTx(tx *gorm.DB, integrationID, projectID, repo string, prNumber int) (string, error) {
	var debounce model.ReviewDebounce
	err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		projectID, repo, prNumber, integrationID).First(&debounce).Error
	switch {
	case err == nil:
		return debounce.HeadSHA, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", err
	}
	var ledger model.ReviewPullRequest
	err = tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		projectID, repo, prNumber, integrationID).First(&ledger).Error
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
// 只动本 (项目, 集成) 的运行——账本分区后其他项目/其他集成的同仓库运行由
// 各自的接管/超时收敛。
func (s *ReviewService) supersedePRRunsTx(tx *gorm.DB, integrationID, projectID, repo string, prNumber int, now time.Time) error {
	active := []model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}
	var runIDs []string
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND status IN ?",
			projectID, repo, prNumber, integrationID, active).
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
func (s *ReviewService) advancePRHeadTx(tx *gorm.DB, project *model.Project, repo string, prNumber int, headSHA, integrationID string, installationID int64, now time.Time) error {
	var ledger model.ReviewPullRequest
	err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		project.ID, repo, prNumber, integrationID).First(&ledger).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&model.ReviewPullRequest{
			ProjectID: project.ID, RepoFullName: repo, PRNumber: prNumber,
			IntegrationID: integrationID,
			UserID:        project.UserID, InstallationID: installationID,
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
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, integrationID).
		Updates(updates).Error
}
