package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Forgejo 轮询补漏（INT-167 任务 3）：webhook 可能因对端故障/网络抖动丢投递，
// 每 5 分钟对启用审查且绑定 Forgejo 的项目拉一遍 open PR，把 webhook 没来得及
// 落账的 SHA 经与 webhook 完全相同的受理路径补上。去重由现有机制保证——
// seen_sha/防抖/总账都按 (项目, 集成) 分区，webhook 先受理的 SHA 轮询再见到
// 会被 runCount/seen 判定跳过，不会开出第二轮。

const reviewForgejoPollInterval = 5 * time.Minute

// reviewForgejoPollTimeout 单项目一轮的网络预算：open PR 分页 + 若干次受理
// 事务，与调度循环的网络阶段预算同量级；超时只放弃本项目，不拖累其他项目。
const reviewForgejoPollTimeout = 60 * time.Second

// openPRLister 轮询补漏的可选扩展面：只有 Forgejo 适配器实现，不进
// forge.ReviewClient 方法面（GitHub 的对应能力由 webhook 事件保证，不需要
// 轮询）。返回的 PR 已由适配器过滤 fork head 与已删 fork。
type openPRLister interface {
	ListOpenPullRequests(ctx context.Context, repoFullName string) ([]forge.PullRequest, error)
}

// singlePRFetcher 单 PR 实时拉取：轮询快照与已受理 head 同刻异 SHA 时用来
// 复核——列表快照可能在 webhook 受理前读到旧 head，平局歧义只能现拉分辨。
// 与 openPRLister 同由 Forgejo 适配器实现。
type singlePRFetcher interface {
	GetPullRequest(ctx context.Context, repoFullName string, number int) (*forge.PullRequest, error)
}

// forgejoPollLoop 轮询循环：与调度循环共用同一个 cancel，tick 驱动，启动不
// 立即跑一轮（进程刚起来时 webhook 投递 backlog 还在路上，立即拉一遍只会
// 抢跑；5 分钟后的第一个 tick 才开始补漏）。
func (s *ReviewService) forgejoPollLoop(cancel <-chan struct{}) {
	ticker := time.NewTicker(reviewForgejoPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.pollForgejoOpenPRs(now.UTC())
		}
	}
}

// pollForgejoOpenPRs 每 tick 一轮：绑定 Forgejo 的启用审查项目逐个补漏。单
// 项目/单 PR 失败只记日志继续下一个，不中断整轮。
func (s *ReviewService) pollForgejoOpenPRs(now time.Time) {
	var rows []model.ProjectReviewSettings
	if err := s.db.
		Where("project_review_settings.enabled AND project_review_settings.integration_id <> ?",
			model.ForgeIntegrationBuiltinGitHubID).
		Find(&rows).Error; err != nil {
		s.logger.Error("查询 Forgejo 轮询项目失败", zap.Error(err))
		return
	}
	for i := range rows {
		if err := s.pollForgejoProject(&rows[i], now); err != nil {
			s.logger.Warn("Forgejo 轮询补漏单项目失败",
				zap.String("project", rows[i].ProjectID), zap.Error(err))
		}
	}
}

// pollForgejoProject 单项目补漏：联查项目行复核「仍绑该集成 + 代码仓未变更」
// （设置行快照与项目行由 repository.Update/UpdateSettings 同步，这里读一遍
// 防漂移），再拉 open PR 逐个受理。
func (s *ReviewService) pollForgejoProject(settings *model.ProjectReviewSettings, now time.Time) error {
	var project model.Project
	if err := s.db.Where("id = ?", settings.ProjectID).First(&project).Error; err != nil {
		return err
	}
	repo := settings.RepoFullName
	if repo == "" ||
		reviewIntegrationID(project.CodeIntegrationID) != settings.IntegrationID ||
		strings.ToLower(project.CodeRepoFullName()) != repo {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewForgejoPollTimeout)
	defer cancel()
	client, err := s.reviewClient(ctx, settings.IntegrationID, repo, 0)
	if err != nil {
		return s.disableReviewOnDeterministic(settings, err)
	}
	lister, ok := client.(openPRLister)
	if !ok {
		// GitHub 适配器未实现该面：天然跳过（该断言只在误把 GitHub 项目放进
		// 查询结果时触发）。
		return nil
	}
	prs, err := lister.ListOpenPullRequests(ctx, repo)
	if err != nil {
		return s.disableReviewOnDeterministic(settings, err)
	}
	fetcher, _ := client.(singlePRFetcher)
	for _, pr := range prs {
		if pr.Draft || pr.HeadSHA == "" {
			continue
		}
		if err := s.pollAcceptPR(ctx, fetcher, settings, repo, &pr, now); err != nil {
			s.logger.Warn("Forgejo 轮询受理 PR 失败",
				zap.String("project", settings.ProjectID), zap.Int("pr", pr.Number), zap.Error(err))
			continue
		}
	}
	return nil
}

// pollAcceptPR 单个 PR 的补漏受理：动作按「该 PR 从未受理过任何 head →
// opened（受 TriggerOpened 勾选位约束），已受理过 → synchronize（受
// TriggerSynchronize 约束）」归类——补漏不该绕过用户没勾选的动作。归类与
// 去重都在受理事务内复核：事务外先读一遍只用来组装，竞态窗口（webhook 在
// 读取与受理之间抢先）由 handleReviewTriggerTx 的「迟到 opened 忽略 +
// runCount 去重」与事务内 current 重读关掉。
func (s *ReviewService) pollAcceptPR(ctx context.Context, fetcher singlePRFetcher, settings *model.ProjectReviewSettings, repo string, pr *forge.PullRequest, now time.Time) error {
	err := s.pollAcceptSnapshotTx(settings, repo, pr, true, now)
	if !errors.Is(err, errPollSnapshotTied) {
		return err
	}
	// 同刻异 head：列表快照不可信，现拉实时 PR 分辨——实时 head 与总账
	// 相同则无事可补，不同则是平台当前真 head，按正常受理路径进（不再
	// 走同刻守卫）。拉不了的适配器丢快照保总账：真实时序里落后的是
	// 快照，下次 tick 用更新的列表再判。
	if fetcher == nil {
		return nil
	}
	live, err := fetcher.GetPullRequest(ctx, repo, pr.Number)
	if err != nil {
		return err
	}
	if live == nil || live.Draft || live.HeadSHA == "" {
		return nil
	}
	return s.pollAcceptSnapshotTx(settings, repo, live, false, now)
}

// pollAcceptSnapshotTx 单个快照的补漏受理事务：fromPoll 标记数据来源是
// 列表快照（受理事务内对「同刻异 head」报 errPollSnapshotTied 由调用方
// 复核），实时拉到的数据传 false 走与 webhook 相同的判定。
func (s *ReviewService) pollAcceptSnapshotTx(settings *model.ProjectReviewSettings, repo string, pr *forge.PullRequest, fromPoll bool, now time.Time) error {
	trigger := &reviewWebhookTrigger{
		number:       pr.Number,
		repoFullName: repo,
		headSHA:      pr.HeadSHA,
		// 补漏只处理同仓库分支的 PR（适配器已过滤跨仓 fork），head/base 同仓。
		headRepoFull: repo,
		baseRepoFull: repo,
		title:        pr.Title,
		htmlURL:      pr.HTMLURL,
		baseRef:      pr.BaseRef,
		headRef:      pr.HeadRef,
		// 平台 updated_at 是过期判定的凭据：轮询快照读到后 webhook 可能已
		// 受理更新的 head，没带时间戳的旧 head 会绕过 seen 去重把总账退回
		// 旧 SHA（新 SHA 反而因「seen 但非当前 head」被永久忽略）。
		updatedAt: pr.UpdatedAt,
		fromPoll:  fromPoll,
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 事务内重读当前 head：与受理同一读快照，归类/跳过判定才可靠。
		current, err := s.currentAcceptedHeadTx(tx, settings.IntegrationID, settings.ProjectID, repo, pr.Number)
		if err != nil {
			return err
		}
		// head 没变：没有新事件可补。曾经受理但开轮被勾选位挡下的 SHA
		// 会在这里停住——同 SHA 归成 synchronize 等于绕过 opened 勾选位。
		if pr.HeadSHA == current {
			return nil
		}
		trigger.action = "synchronize"
		if current == "" {
			trigger.action = "opened"
		}
		return s.handleReviewTriggerTx(tx, settings.IntegrationID, trigger, now)
	})
}

// disableReviewOnDeterministic 轮询确定性失败时停用该项目审查：集成误删/
// 密文损坏/凭证被拒后每 tick 都只会得到同一错误——继续 enabled 既无产出又
// 按固定节律刷错，压回 disabled 让设置页可见、人工修复后可重新开启（与
// repository.Update 改绑压回 disabled 同一先例）。谓词带上本轮快照的
// (integration_id, repo_full_name)：轮询在途期间用户已改绑/换仓库的话，
// 旧快照的错误不能关掉新绑定。停用与显式路径同口径收尾：取消在途/排队
// 运行、消费令牌、删防抖、推进 epoch 清 sync_pending——否则调度仍按
// status 捡走残留运行继续开线程。瞬时错误原样透出照常 warn 重试。
func (s *ReviewService) disableReviewOnDeterministic(settings *model.ProjectReviewSettings, err error) error {
	if !deterministicReviewAccessErr(err) {
		return err
	}
	now := time.Now().UTC()
	disabled := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ProjectReviewSettings{}).
			Where(`project_id = ? AND enabled
				AND integration_id = ? AND repo_full_name = ?`,
				settings.ProjectID, settings.IntegrationID, settings.RepoFullName).
			Updates(map[string]any{"enabled": false, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // 快照已过期：新绑定/已停用都不归本次轮询管
		}
		disabled = true
		return cancelProjectReviewRunsTx(tx, settings.ProjectID, "disabled", now)
	})
	if txErr != nil {
		s.logger.Error("确定性失败后停用审查设置失败",
			zap.String("project", settings.ProjectID), zap.Error(txErr))
		return txErr
	}
	if disabled {
		s.logger.Error("Forgejo 轮询确定性失败，已停用该项目 PR 审查",
			zap.String("project", settings.ProjectID),
			zap.String("integration", settings.IntegrationID), zap.Error(err))
	}
	return nil
}
