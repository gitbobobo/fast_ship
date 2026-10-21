package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GitHub 产出（INT-135）：sync_pending 的 PR 总账逐行同步到 GitHub——
// commit status（代码审查/视觉审查两个固定 context）、一条合并顶层评论、
// 每组合一条行内 review、视觉衔接。失败只写 sync_error 下轮重试；
// 成功后按起始 epoch 条件清 sync_pending。

// errReviewSyncDeferred 表示本轮同步主动让路（例如远端 head 已前进、等 webhook
// 推进账本）：不改写 sync_error，也不清 sync_pending。
var errReviewSyncDeferred = errors.New("review: sync deferred")

const (
	reviewCodeStatusContext   = "代码审查"
	reviewVisualStatusContext = "视觉审查"
	reviewCodeCommentMarker   = "<!-- fast-ship-code-review -->"
	reviewVisualCommentMarker = "<!-- fast-ship-visual-review -->"
)

// statusDesc commit status 的 description 截断（GitHub 上限 140 字节）：
// 按 rune 边界累计到 ≤137 字节再加 "..."——按字节下标切会劈断中文等
// 多字节 UTF-8 序列，产出非法字符串被 GitHub 拒收。
func statusDesc(s string) string {
	const max = 140
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s { // i 是各 rune 的起始字节下标；s[:i] 恒在字符边界
		if i > max-3 {
			break
		}
		cut = i
	}
	return s[:cut] + "..."
}

// errReviewSyncAbandoned 同步期间目标已不是本轮的活（账本 epoch 被推进，或项目
// 停用审查/换走审查仓库）：静默放弃本轮，不发 GitHub 产出、不写 sync_error——
// 与「让路」（errReviewSyncDeferred）不同，本行不再属于这次同步。
var errReviewSyncAbandoned = errors.New("review sync abandoned: target changed")

// syncTargetStillCurrent 同步中途复核本轮仍有效：账本 epoch 未被推进，且项目
// enabled 设置行的仓库快照仍是本仓库（项目改仓库/停用审查会同事务改这两处）。
func (s *ReviewService) syncTargetStillCurrent(ledger *model.ReviewPullRequest, epoch int) error {
	current, err := s.ledgerEpochCurrent(ledger, epoch)
	if err != nil {
		return err
	}
	if !current {
		return errReviewSyncAbandoned
	}
	var settings model.ProjectReviewSettings
	err = s.db.Select("repo_full_name").
		Where("project_id = ? AND enabled", ledger.ProjectID).First(&settings).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 设置行没了（项目被删/从未启用）：不再属于本轮。
		return errReviewSyncAbandoned
	case err != nil:
		// DB 故障不是「目标失效」：与上面的 epoch 复核同口径往外抛，让同步
		// 记 sync_error 下轮重试，不要静默吞掉一个真正的错误。
		return err
	}
	if settings.RepoFullName != ledger.RepoFullName {
		return errReviewSyncAbandoned
	}
	return nil
}

// guardedSyncClient 在每次 GitHub 写之前复核本轮仍有效：远端副作用无法回滚，
// 复核只能放在写之前。读操作原样透传（Embedding 提供），只有会改变 GitHub
// 状态的四个方法被拦。
type guardedSyncClient struct {
	githubapi.GitHubClient
	check func() error
}

func (c guardedSyncClient) CreateCommitStatus(ctx context.Context, installationID int64, repoFullName, sha, state, statusContext, description, targetURL string) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.GitHubClient.CreateCommitStatus(ctx, installationID, repoFullName, sha, state, statusContext, description, targetURL)
}

func (c guardedSyncClient) CreatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (int64, error) {
	if err := c.check(); err != nil {
		return 0, err
	}
	return c.GitHubClient.CreatePullRequestComment(ctx, installationID, repoFullName, number, body)
}

func (c guardedSyncClient) UpdatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.GitHubClient.UpdatePullRequestComment(ctx, installationID, repoFullName, commentID, body)
}

func (c guardedSyncClient) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in githubapi.ReviewInput) (int64, error) {
	if err := c.check(); err != nil {
		return 0, err
	}
	return c.GitHubClient.SubmitReview(ctx, installationID, repoFullName, number, in)
}

// lockForPR 每 PR 一把进程内锁。
func (s *ReviewService) lockForPR(repo string, prNumber int) *sync.Mutex {
	key := repo + "#" + strconv.Itoa(prNumber)
	s.prLocksMu.Lock()
	defer s.prLocksMu.Unlock()
	lock, ok := s.prLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.prLocks[key] = lock
	}
	return lock
}

// syncPendingPRs 逐行处理 sync_pending 的 PR 总账。只处理项目设置仍 enabled
// 的账本行：被接管/停用项目的残留行不再发 GitHub 产出——同一仓库的
// 状态与评论由当前 enabled 项目独占。
func (s *ReviewService) syncPendingPRs(now time.Time) {
	var rows []model.ReviewPullRequest
	// 三重要求：pending、所属项目仍启用、且账本仓库就是该项目 enabled 设置行
	// 的 repo_full_name 快照（审查仓库的唯一真相）。项目换过仓库后旧仓库的
	// ledger 可能因旧运行收尾再次置 pending，不能在新仓库的项目上把它同步
	// 出去；公开仓库 ≠ 代码仓库时，公开仓库的账本行同样不再同步。
	if err := s.db.Where(`sync_pending
		AND EXISTS (
			SELECT 1 FROM project_review_settings s
			WHERE s.project_id = review_pull_requests.project_id
			  AND s.enabled
			  AND s.repo_full_name = review_pull_requests.repo_full_name
		)`).
		Order("updated_at ASC").Limit(reviewStageBatch).
		Find(&rows).Error; err != nil {
		s.logger.Error("查询待同步 PR 失败", zap.Error(err))
		return
	}
	for i := range rows {
		s.syncOnePR(&rows[i])
	}
}

// syncOnePR 一个 PR 的 GitHub 同步（同 PR 串行）。重读账本后先做准入复核
// （pending 未清、epoch 未推进、项目 enabled 设置行的快照仍是本仓库）：从
// syncPendingPRs 选出到执行之间项目可能已换审查仓库或停用审查，不再属于
// 本轮的活直接放弃，不发任何 GitHub 请求也不写库；通过后 epoch 即重读值，
// 失败写 sync_error（epoch 守卫），成功按 epoch 清 pending。
func (s *ReviewService) syncOnePR(ledger *model.ReviewPullRequest) {
	key := ledger.RepoFullName + "#" + strconv.Itoa(ledger.PRNumber)
	lock := s.lockForPR(ledger.RepoFullName, ledger.PRNumber)
	lock.Lock()
	defer func() {
		// 摘除锁条目：syncOnePR 只被单进程调度循环串行调用，不存在等在同
		// 一把锁上的并发 goroutine——本轮处理完即摘除，prLocks 大小只随
		// "正在同步的 PR"增长而不是随历史 PR 数无界增长。
		s.prLocksMu.Lock()
		if s.prLocks[key] == lock {
			delete(s.prLocks, key)
		}
		s.prLocksMu.Unlock()
		lock.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var cur model.ReviewPullRequest
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).First(&cur).Error; err != nil {
		return
	}
	// 准入复核：项目换仓库/停用审查会同事务清 pending、推进 epoch、刷新设置行
	// 快照，任一不成立说明本行已被移出本轮，直接放弃（这是放弃，不是让路）。
	var settings model.ProjectReviewSettings
	if err := s.db.Select("repo_full_name").
		Where("project_id = ? AND enabled", cur.ProjectID).
		First(&settings).Error; err != nil {
		return
	}
	if !cur.SyncPending || cur.SyncEpoch != ledger.SyncEpoch ||
		settings.RepoFullName != cur.RepoFullName {
		return
	}
	epoch := cur.SyncEpoch
	now := time.Now().UTC()

	syncErr := s.doSyncPR(ctx, &cur, epoch, now)
	if errors.Is(syncErr, errReviewSyncDeferred) {
		// 主动让路（远端 head 已前进、等 webhook）：不算同步失败，不清 pending。
		return
	}
	if errors.Is(syncErr, errReviewSyncAbandoned) {
		// 同步中途目标失效：静默放弃，不写 sync_error（本行已不属这次同步）。
		return
	}
	if syncErr != nil {
		s.db.Model(&model.ReviewPullRequest{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
				cur.ProjectID, cur.RepoFullName, cur.PRNumber, epoch).
			Updates(map[string]any{"sync_error": syncErr.Error(), "updated_at": now})
		s.logger.Warn("PR 审查同步失败（下轮重试）",
			zap.String("repo", cur.RepoFullName), zap.Int("pr", cur.PRNumber), zap.Error(syncErr))
		return
	}
	s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
			cur.ProjectID, cur.RepoFullName, cur.PRNumber, epoch).
		Updates(map[string]any{"sync_pending": false, "sync_error": "", "updated_at": now})
}

// doSyncPR 同步主体：读 PR 实时数据 → 代码汇总状态/评论/行内 → 视觉衔接。
// epoch 为进入 syncOnePR 时捕获的总账快照；所有中段写库带 sync_epoch 条件
// ——同步期间总账被推进（新 webhook/重跑/取消）时条件不命中，立即放弃本
// 轮落库，陈旧快照的结论不得写回（sync_pending 留给新 epoch 的下轮重试）。
func (s *ReviewService) doSyncPR(ctx context.Context, ledger *model.ReviewPullRequest, epoch int, now time.Time) error {
	if ledger.HeadSHA == "" {
		return nil
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return err
	}
	// 每次 GitHub 写之前复核本轮仍有效（账本 epoch 未推进、项目审查仓库仍是
	// 本仓库）：同步可能持续数十秒，期间项目改仓库/停用审查不该把产出写到
	// 已切换掉的旧仓库，而远端副作用发出去就收不回来。
	client = guardedSyncClient{
		GitHubClient: client,
		check:        func() error { return s.syncTargetStillCurrent(ledger, epoch) },
	}
	pr, err := client.GetPullRequest(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 PR 失败: "+err.Error())
	}
	// 首次远端写之前的复核（读 PR 是只读，不算产出）。
	if err := s.syncTargetStillCurrent(ledger, epoch); err != nil {
		return err
	}
	// GitHub 上已经前进、但 webhook 还没处理时，账本与运行仍是旧 SHA：此时
	// 不能拿新 PR 描述/SHA 去发布旧轮结论或建视觉运行。保留 pending 等 webhook
	// 推进（webhook 会清 pending 并推进 head）。
	if ledger.HeadSHA != "" && pr.HeadSHA != "" && !strings.EqualFold(pr.HeadSHA, ledger.HeadSHA) {
		s.logger.Info("PR head 已前进但 webhook 未处理，跳过本次同步",
			zap.String("repo", ledger.RepoFullName), zap.Int("pr", ledger.PRNumber),
			zap.String("ledger", ledger.HeadSHA), zap.String("remote", pr.HeadSHA))
		return errReviewSyncDeferred
	}
	targetURL := pr.HTMLURL
	if targetURL == "" {
		targetURL = ledger.RepoFullName + "/pull/" + strconv.Itoa(ledger.PRNumber)
	}

	var codeRuns, visualRuns []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND round_number = ? AND kind = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA, ledger.RoundNumber, model.ReviewKindCode).
		Order("created_at ASC").Find(&codeRuns).Error; err != nil {
		return errs.ErrInternal
	}
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND round_number = ? AND kind = ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA, ledger.RoundNumber, model.ReviewKindVisual).
		Order("created_at ASC").Find(&visualRuns).Error; err != nil {
		return errs.ErrInternal
	}

	// 1. 代码 commit status（状态变化才写）。
	codeState, codeDesc := summarizeCodeRuns(codeRuns)
	if codeState != ledger.CodeStatus {
		if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
			ledger.HeadSHA, codeState, reviewCodeStatusContext, statusDesc(codeDesc), targetURL); err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "写代码审查状态失败: "+err.Error())
		}
		ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"code_status": codeState})
		if err != nil {
			return errs.New(errs.ErrInternal.Code, "更新代码审查状态失败: "+err.Error())
		}
		if !ok {
			return nil
		}
		ledger.CodeStatus = codeState
	}

	// 2. 视觉衔接决策（仅当全部代码运行终态后才有意义；视觉运行已建时走汇总）。
	visualCarry := false
	visualPending := len(visualRuns) == 0
	if len(visualRuns) == 0 {
		decision, fingerprint, screensReason, priorVisuals, decErr := s.decideVisualForPR(ctx, ledger, pr, codeRuns)
		if decErr != nil {
			return decErr
		}
		switch decision {
		case visualScreensUnavailable:
			// 截图全部下载失败：不建视觉运行，给「视觉审查」写一条明确的
			// failure。指纹不入账——重试发生在下一次同步事件（新推送产生
			// 新 SHA、或手动重跑；reopened 对同一 SHA 不会再次进入同步，
			// webhook 把已有运行的事件一律忽略），系统不为「截图不可用」
			// 单独排定时重试。每次事件都写当前原因（与 visualCarryFailure
			// 分支一致）：沿用上一轮的旧描述会保留语义不同的文案。
			visualPending = false
			// 上一轮视觉意见尚未处理时，代码顶层评论同样要带「截图复核意见
			// 尚未处理」——与 visualCarryFailure 分支口径一致；上一轮通过或
			// 没有上一轮时不置真（不能拿不存在的遗留去催复核者）。
			visualCarry = priorVisualFailure(priorVisuals)
			reason := screensReason
			if reason == "" {
				reason = "截图全部下载失败"
			}
			desc := "截图无法下载，未做视觉审查：" + reason
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, "failure", reviewVisualStatusContext, statusDesc(desc), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": "failure"})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = "failure"
			// 上一轮有视觉运行就说明 PR 上应有视觉评论（上一轮结论/carry 分支
			// 发过）：必须改写——新 head 的截图集合已换且全部下不来，「截图未
			// 变化」的说法不再成立。只看 priorVisuals 而不看账本 id：id 写回
			// 失败（upsertPRComment 末尾的 Update 不查错误）会留下
			// VisualCommentID == 0，按 VisualCommentID 短路会让旧评论原样留下，
			// 与 failure status 互相矛盾；savedID 为 0 时 upsertPRComment 按
			// marker 找回既有评论改写，找不到才创建，正是想要的语义。没有上一
			// 轮视觉运行时不新发——从未做过视觉审查的 head 不值得多一条只有
			// 说明的评论（status 已表达结论）。
			if len(priorVisuals) > 0 {
				if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
					return errs.New(errs.ErrInternal.Code, "复核同步 epoch 失败: "+err.Error())
				} else if !ok {
					return nil
				}
				body := buildScreensUnavailableComment(priorVisuals)
				if err := s.upsertPRComment(ctx, client, ledger, reviewVisualCommentMarker,
					body, "visual_comment_id", ledger.VisualCommentID); err != nil {
					return err
				}
			}
		case visualStart:
			visualPending = true
			created, aborted, createErr := s.createVisualRuns(ledger, pr, codeRuns, now)
			if createErr != nil {
				return createErr
			}
			if aborted {
				return nil // 快照已被推进（新 webhook/重跑/取消），放弃本轮同步
			}
			if len(created) == 0 {
				// 没有真正建出视觉运行（例如同步期间被停用/组合为空）：
				// 不能发「视觉审查中」，交给下一轮重新决策。
				visualPending = false
				break
			}
			visualRuns = created
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, "pending", reviewVisualStatusContext, statusDesc("视觉审查中"), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{
				"visual_status":              "pending",
				"visual_screens_fingerprint": fingerprint,
			})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = "pending"
			ledger.VisualScreensFingerprint = fingerprint
		case visualCarryFailure:
			visualPending = false
			visualCarry = true
			desc := visualCarryDescription(priorVisuals)
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, "failure", reviewVisualStatusContext, statusDesc(desc), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": "failure"})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = "failure"
			if err := s.upsertVisualComment(ctx, client, ledger, nil, priorVisuals, true, now); err != nil {
				return err
			}
		default:
			visualPending = false
			// 截图没变、无需重启视觉审查：上一轮视觉已通过时把 success 带到
			// 新 head（开轮清空了 visual_status），否则新提交会没有视觉结论。
			if len(priorVisuals) > 0 && priorVisualPassed(priorVisuals) {
				if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
					ledger.HeadSHA, "success", reviewVisualStatusContext,
					statusDesc("审查通过（沿用上一轮，截图未变化）"), targetURL); err != nil {
					return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
				}
				ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": "success"})
				if err != nil {
					return errs.ErrInternal
				}
				if !ok {
					return nil
				}
				ledger.VisualStatus = "success"
			}
		}
	} else {
		visualPending = false
	}

	// 3. 代码顶层评论（含视觉待办/遗留提示）。写之前复核 epoch：同步期间
	// 有新 SHA 进来时，旧快照的评论与行内 review 都不应再发。
	if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
		return errs.New(errs.ErrInternal.Code, "复核同步 epoch 失败: "+err.Error())
	} else if !ok {
		return nil
	}
	codeBody := buildCodeComment(codeRuns, visualPending, visualCarry)
	if err := s.upsertPRComment(ctx, client, ledger, reviewCodeCommentMarker, codeBody, "code_comment_id", ledger.CodeCommentID); err != nil {
		return err
	}

	// 4. 行内 review：每个 completed 且未 posted 的代码运行，有效行发 review。
	if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
		return errs.New(errs.ErrInternal.Code, "复核同步 epoch 失败: "+err.Error())
	} else if !ok {
		return nil
	}
	if err := s.postInlineReviews(ctx, client, ledger, codeRuns, epoch, now); err != nil {
		return err
	}

	// 5. 视觉运行存在时的状态与评论。
	if len(visualRuns) > 0 {
		if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
			return errs.ErrInternal
		} else if !ok {
			return nil
		}
		vs, vdesc := summarizeVisualRuns(visualRuns)
		if vs != ledger.VisualStatus {
			if err := client.CreateCommitStatus(ctx, ledger.InstallationID, ledger.RepoFullName,
				ledger.HeadSHA, vs, reviewVisualStatusContext, statusDesc(vdesc), targetURL); err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "写视觉审查状态失败: "+err.Error())
			}
			ok, err := s.updateLedgerEpoch(ledger, epoch, map[string]any{"visual_status": vs})
			if err != nil {
				return errs.ErrInternal
			}
			if !ok {
				return nil
			}
			ledger.VisualStatus = vs
		}
		if vs != "pending" {
			if err := s.upsertVisualComment(ctx, client, ledger, visualRuns, nil, false, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// updateLedgerEpoch 同步中段的总账写回：sync_epoch 守卫——命中返回 true；
// 不命中说明同步期间总账被推进（epoch 已变），本次快照作废，调用方应中止。
// ledgerEpochCurrent 同步中途复核总账 epoch：新 webhook 推进过就停止本次
// 剩余的 GitHub 写（评论/行内 review/视觉状态），避免把被取代 SHA 的结论
// 继续写出去。
func (s *ReviewService) ledgerEpochCurrent(ledger *model.ReviewPullRequest, epoch int) (bool, error) {
	var cur int
	if err := s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
		Select("sync_epoch").Scan(&cur).Error; err != nil {
		return false, err
	}
	return cur == epoch, nil
}

func (s *ReviewService) updateLedgerEpoch(ledger *model.ReviewPullRequest, epoch int, fields map[string]any) (bool, error) {
	res := s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND sync_epoch = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, epoch).
		Updates(fields)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// upsertPRComment 优先按已存 comment id 直接更新（savedID）；更新撞 404
// （评论被删）时回退 marker 分页找回，仍找不到才创建。写回 comment id 列。
func (s *ReviewService) upsertPRComment(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, marker, body, idColumn string, savedID int64) error {
	commentID := savedID
	found := false
	if savedID != 0 {
		found = true
		if err := client.UpdatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, savedID, body); err != nil {
			if githubapi.IsNotFound(err) {
				found = false // 已存评论被删，回退按 marker 找回
			} else {
				return errs.New(errs.ErrGitHubAPI.Code, "更新顶层评论失败: "+err.Error())
			}
		} else {
			return nil // id 不变，无需写回
		}
	}
	if !found {
		var err error
		commentID, found, err = client.FindPullRequestComment(ctx, ledger.InstallationID,
			ledger.RepoFullName, ledger.PRNumber, marker)
		if err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "查找顶层评论失败: "+err.Error())
		}
		if found {
			if err := client.UpdatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, commentID, body); err != nil {
				if githubapi.IsNotFound(err) {
					found = false
				} else {
					return errs.New(errs.ErrGitHubAPI.Code, "更新顶层评论失败: "+err.Error())
				}
			}
		}
	}
	if !found {
		newID, err := client.CreatePullRequestComment(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber, body)
		if err != nil {
			return errs.New(errs.ErrGitHubAPI.Code, "创建顶层评论失败: "+err.Error())
		}
		commentID = newID
	}
	s.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
		Update(idColumn, commentID)
	return nil
}

// upsertVisualComment 视觉顶层评论：本 head 的视觉运行结论或沿用提醒。
func (s *ReviewService) upsertVisualComment(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, visualRuns []model.ReviewRun, priorRuns []model.ReviewRun, carried bool, now time.Time) error {
	body := buildVisualComment(visualRuns, priorRuns, carried)
	return s.upsertPRComment(ctx, client, ledger, reviewVisualCommentMarker, body, "visual_comment_id", ledger.VisualCommentID)
}

// summarizeCodeRuns 代码审查汇总状态（status 描述 ≤140 字符由调用方截断）。
func summarizeCodeRuns(runs []model.ReviewRun) (state, description string) {
	if len(runs) == 0 {
		return "pending", "审查中"
	}
	for _, r := range runs {
		if !r.Status.Terminal() {
			return "pending", "审查中"
		}
	}
	for _, r := range runs {
		if r.Status != model.ReviewRunCompleted {
			return "error", "审查未完成：" + runCombinationLabel(&r) + " 失败"
		}
	}
	counts := map[string]int{"P0": 0, "P1": 0, "P2": 0}
	for _, r := range runs {
		var result api.ReviewCodeResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			switch f.Level {
			case api.P0:
				counts["P0"]++
			case api.P1:
				counts["P1"]++
			case api.P2:
				counts["P2"]++
			}
		}
	}
	total := counts["P0"] + counts["P1"] + counts["P2"]
	if total > 0 {
		var parts []string
		for _, lvl := range []string{"P0", "P1", "P2"} {
			if counts[lvl] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d 条", lvl, counts[lvl]))
			}
		}
		return "failure", "发现问题：" + strings.Join(parts, "，")
	}
	return "success", "审查通过"
}

// summarizeVisualRuns 视觉审查汇总：active → pending；未完成的运行 → error；
// blocker/worth_fixing → failure；否则 success。
func summarizeVisualRuns(runs []model.ReviewRun) (state, description string) {
	for _, r := range runs {
		if !r.Status.Terminal() {
			return "pending", "视觉审查中"
		}
	}
	for _, r := range runs {
		if r.Status != model.ReviewRunCompleted {
			return "error", "视觉审查未完成：" + runCombinationLabel(&r) + " 失败"
		}
	}
	n := 0
	for _, r := range runs {
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
				n++
			}
		}
	}
	if n > 0 {
		return "failure", fmt.Sprintf("发现 %d 条问题", n)
	}
	return "success", "审查通过"
}

// buildCodeComment 代码审查顶层评论正文：marker + 按组合分节 + 发现排序
// P0→P1→P2→Note + 视觉待办/遗留提示 + 复审说明。
func buildCodeComment(codeRuns []model.ReviewRun, visualPending, visualCarryFailure bool) string {
	var b strings.Builder
	b.WriteString(reviewCodeCommentMarker + "\n代码审查结论：\n\n")
	allCompleted, hasFindings := true, false
	for _, r := range codeRuns {
		fmt.Fprintf(&b, "### %s\n", runCombinationLabel(&r))
		switch {
		case r.Status == model.ReviewRunCompleted:
			var result api.ReviewCodeResult
			if json.Unmarshal([]byte(r.ResultJSON), &result) == nil {
				if result.Summary != "" {
					b.WriteString(result.Summary + "\n")
				}
				sort.SliceStable(result.Findings, func(i, j int) bool {
					return codeLevelRank(result.Findings[i].Level) < codeLevelRank(result.Findings[j].Level)
				})
				for _, f := range result.Findings {
					hasFindings = hasFindings || f.Level != api.Note
					loc := f.File
					if f.Line != nil && *f.Line > 0 {
						loc = fmt.Sprintf("%s:%d", f.File, *f.Line)
					}
					fmt.Fprintf(&b, "- %s %s %s\n", f.Level, loc, f.Body)
				}
			}
		case r.Status.Terminal():
			allCompleted = false
			fmt.Fprintf(&b, "- 本组合未完成（%s）：%s\n", r.Status, r.ErrorMessage)
		default:
			allCompleted = false
			b.WriteString("- 审查中…\n")
		}
		b.WriteString("\n")
	}
	if allCompleted && !hasFindings {
		b.WriteString("本轮未发现 P0/P1/P2 问题。\n\n")
	}
	if visualPending {
		b.WriteString("视觉审查尚未结束，将复核截图。\n\n")
	}
	if visualCarryFailure {
		b.WriteString("截图复核意见尚未处理\n\n")
	}
	b.WriteString("复审说明：推送新提交后会自动重新审查。")
	return b.String()
}

func codeLevelRank(level api.ReviewCodeFindingLevel) int {
	switch level {
	case api.P0:
		return 0
	case api.P1:
		return 1
	case api.P2:
		return 2
	}
	return 3
}

// appendVisualFindings 把视觉运行的意见条目渲染进正文：按严重度排序，带截图
// 标签/维度/改法。buildVisualComment 与 buildScreensUnavailableComment 共用
// 这段渲染，不复制两份。
func appendVisualFindings(b *strings.Builder, runs []model.ReviewRun) {
	rank := func(s api.ReviewVisualFindingSeverity) int {
		switch s {
		case api.Blocker:
			return 0
		case api.WorthFixing:
			return 1
		}
		return 2
	}
	for _, r := range runs {
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		fmt.Fprintf(b, "### %s\n", runCombinationLabel(&r))
		if result.Summary != "" {
			b.WriteString(result.Summary + "\n")
		}
		sort.SliceStable(result.Findings, func(i, j int) bool {
			return rank(result.Findings[i].Severity) < rank(result.Findings[j].Severity)
		})
		for _, f := range result.Findings {
			fmt.Fprintf(b, "- %s · %s · %s：%s\n  改法：%s\n", f.Severity, f.Screenshot, f.Dimension, f.Body, f.Fix)
		}
		for _, u := range result.Unmeasurable {
			fmt.Fprintf(b, "- 无法判断 · %s：%s\n", u.Screenshot, u.Reason)
		}
		b.WriteString("\n")
	}
}

// buildVisualComment 视觉审查顶层评论：按严重度排序，带截图标签/维度/改法；
// carried 时正文说明沿用了上一轮（截图未变）。
func buildVisualComment(visualRuns, priorRuns []model.ReviewRun, carried bool) string {
	var b strings.Builder
	b.WriteString(reviewVisualCommentMarker + "\n视觉审查意见：\n\n")
	if carried {
		b.WriteString("沿用上一轮视觉审查结论（截图未变化，意见尚未处理）。\n\n")
	}
	source := visualRuns
	if carried {
		source = priorRuns
	}
	appendVisualFindings(&b, source)
	return strings.TrimRight(b.String(), "\n")
}

// buildScreensUnavailableComment 截图全部无法下载时的视觉评论正文：沿用同一
// marker，说明本轮未做视觉审查的原因，并保留上一轮的视觉意见条目——上一轮
// carry 发的「截图未变化」评论不能原样留在新 head 上（截图集合已换），但上一
// 轮的意见本身仍未处理，丢掉会让复核者失去线索。
func buildScreensUnavailableComment(priorRuns []model.ReviewRun) string {
	var b strings.Builder
	b.WriteString(reviewVisualCommentMarker + "\n视觉审查意见：\n\n")
	b.WriteString("本轮 PR 描述里的截图全部无法下载，未做视觉审查。以下为上一轮的视觉审查意见：\n\n")
	appendVisualFindings(&b, priorRuns)
	return strings.TrimRight(b.String(), "\n")
}

// postInlineReviews 每个 completed 且 posted_at IS NULL 的代码运行：
// P0–P2 且行号落在 diff 右侧有效行内 → SubmitReview；posted_at=now。
func (s *ReviewService) postInlineReviews(ctx context.Context, client githubapi.GitHubClient,
	ledger *model.ReviewPullRequest, codeRuns []model.ReviewRun, epoch int, now time.Time) error {
	var pending []model.ReviewRun
	for _, r := range codeRuns {
		if r.Status == model.ReviewRunCompleted && r.PostedAt == nil {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	// 对账：上一轮可能已经成功提交 review、但响应丢失（posted_at 没写回）。
	// 先拉一次已有 review，命中「同 commit + 同 summary」就认领为已发布，
	// 避免同一批行内意见每轮重复提交。
	existing, err := client.ListReviews(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 review 列表失败: "+err.Error())
	}
	posted := map[string][]int64{} // summary -> review ids（旧评论没有运行标记时的兜底）
	postedByRun := map[string]int64{}
	for _, rev := range existing {
		if !strings.EqualFold(rev.CommitID, ledger.HeadSHA) {
			continue
		}
		if id := reviewRunMarkerID(rev.Body); id != "" {
			postedByRun[id] = rev.ID
		}
		if rev.Body != "" {
			posted[rev.Body] = append(posted[rev.Body], rev.ID)
		}
	}
	diff, err := client.GetPullRequestDiff(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 diff 失败: "+err.Error())
	}
	valid := parseDiffRightLines(diff)
	for _, r := range pending {
		// 复核运行仍是 completed：同步期间新 SHA 会把旧运行置 cancelled，
		// 内存里的 codeRuns 是快照，不能据此继续发旧结论。
		var status model.ReviewRunStatus
		if err := s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
			Select("status").Scan(&status).Error; err != nil {
			return errs.New(errs.ErrInternal.Code, "复核运行状态失败: "+err.Error())
		}
		if status != model.ReviewRunCompleted {
			continue
		}
		var result api.ReviewCodeResult
		_ = json.Unmarshal([]byte(r.ResultJSON), &result)
		var comments []githubapi.ReviewInlineComment
		for _, f := range result.Findings {
			if f.Level == api.Note || f.Line == nil || *f.Line < 1 {
				continue
			}
			if lines, ok := valid[f.File]; ok && lines[*f.Line] {
				comments = append(comments, githubapi.ReviewInlineComment{
					Path: f.File, Line: *f.Line, Side: "RIGHT",
					Body: fmt.Sprintf("%s：%s", f.Level, f.Body),
				})
			}
		}
		if id, ok := postedByRun[r.ID]; ok {
			s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
				Updates(map[string]any{"posted_at": now, "review_id": id, "updated_at": now})
			continue
		}
		// 兼容没有运行标记的旧 review：同一 summary 只对应一条待发运行时才认领，
		// 否则无法区分是哪个组合的（宁可重发也不吞掉另一个组合的意见）。
		if ids := posted[r.Summary]; r.Summary != "" && len(ids) == 1 {
			pendingWithSummary := 0
			for _, other := range pending {
				if other.Summary == r.Summary {
					pendingWithSummary++
				}
			}
			if pendingWithSummary == 1 {
				s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
					Updates(map[string]any{"posted_at": now, "review_id": ids[0], "updated_at": now})
				continue
			}
		}
		var reviewID int64
		if len(comments) > 0 {
			// 读 diff 是慢调用：提交前再复核一次 epoch，避免把旧 SHA 的
			// 发现作为新 review 发出去（supersede 不会把已完成的运行改状态）。
			if ok, err := s.ledgerEpochCurrent(ledger, epoch); err != nil {
				return errs.ErrInternal
			} else if !ok {
				return nil
			}
			id, err := client.SubmitReview(ctx, ledger.InstallationID, ledger.RepoFullName, ledger.PRNumber,
				githubapi.ReviewInput{
					Event: "COMMENT", CommitID: ledger.HeadSHA, Comments: comments,
					Body: r.Summary + reviewRunMarker(r.ID),
				})
			if err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "提交行内 review 失败: "+err.Error())
			}
			reviewID = id
		}
		s.db.Model(&model.ReviewRun{}).Where("id = ?", r.ID).
			Updates(map[string]any{"posted_at": now, "review_id": reviewID, "updated_at": now})
	}
	return nil
}

// reviewRunMarker 行内 review 的运行标记：响应丢失后下一轮按运行 id 对账，
// 避免同 summary 的多个组合互相吞掉。
func reviewRunMarker(runID string) string {
	return "\n\n<!-- fast-ship-review-run:" + runID + " -->"
}

// reviewRunMarkerID 从 review 正文里取运行 id；没有标记返回空串。
func reviewRunMarkerID(body string) string {
	const prefix = "<!-- fast-ship-review-run:"
	start := strings.Index(body, prefix)
	if start < 0 {
		return ""
	}
	rest := body[start+len(prefix):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// parseDiffRightLines 解析 unified diff：返回每个文件在 HEAD 右侧可被行内
// 评论的有效行号集合（hunk 内的 context 与 + 行）。
func parseDiffRightLines(diff string) map[string]map[int]bool {
	valid := map[string]map[int]bool{}
	var curFile string
	var newLine int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			curFile = strings.TrimPrefix(strings.TrimSpace(line), "+++ ")
			curFile = strings.TrimPrefix(curFile, "b/")
			if curFile == "/dev/null" {
				curFile = ""
			}
		case strings.HasPrefix(line, "@@ "):
			// @@ -a,b +c,d @@ — 取 +c 为新侧起始行。
			if idx := strings.Index(line, "+"); idx >= 0 {
				rest := line[idx+1:]
				num := rest
				if cut := strings.IndexAny(rest, ", "); cut >= 0 {
					num = rest[:cut]
				}
				newLine, _ = strconv.Atoi(strings.TrimSpace(num))
				newLine-- // hunk 首行自身先预减，循环里 ++ 后即 c
			}
		case curFile != "" && strings.HasPrefix(line, "+"):
			newLine++
			if valid[curFile] == nil {
				valid[curFile] = map[int]bool{}
			}
			valid[curFile][newLine] = true
		case curFile != "" && strings.HasPrefix(line, "-"):
			// 删除行不占新侧行号
		case curFile != "" && strings.HasPrefix(line, " "):
			// context 行才占新侧行号；diff 末尾的空行（结尾 \n 分裂出的
			// 空串）不是右侧有效行，不能计数。
			newLine++
			if valid[curFile] == nil {
				valid[curFile] = map[int]bool{}
			}
			valid[curFile][newLine] = true
		}
	}
	return valid
}

// ------ 视觉衔接 ------

type visualDecision int

const (
	visualNone               visualDecision = iota // 不启动
	visualStart                                    // 建视觉运行
	visualCarryFailure                             // 不新建，沿用旧 failure 到新 head
	visualScreensUnavailable                       // 截图全部下载失败，未启动视觉运行
)

// visualDecisionInput decideVisual 的纯输入（便于单测）。
type visualDecisionInput struct {
	CodeAllTerminalOK           bool // 全部代码运行 completed 且无 P0/P1/P2
	HasVisualCombos             bool
	HasScreenshots              bool
	AnyUIChanged                bool
	ScreensChanged              bool // 本次截图指纹 ≠ 总账 fingerprint
	AnyUIChangedSinceLastVisual bool
	PriorVisualFailed           bool // 上一轮视觉含 blocker/worth_fixing
	// ScreensUnavailable 截图探测全部失败。探测只在 ScreensChanged ||
	// AnyUIChangedSinceLastVisual 成立（本轮真的要开视觉）时发生，所以它只
	// 会把 visualStart 改判成 visualScreensUnavailable，不是独立短路——截图
	// 未变时为假，沿用/带过上一轮结论的路径照常。
	ScreensUnavailable       bool
	ScreensUnavailableReason string
}

// decideVisual 视觉衔接判定（顺序即优先级）：
// 代码未全净、无视觉组合、无 UI 变化、无截图 → 不启动；
// 截图变了或上轮视觉后又有 UI 改动（本轮真的要开视觉）→ 探测全部失败则报
// 「截图无法下载」（不走沿用旧 failure 的分支——那会拿上一轮不相关的结论
// 解释这次的失败），否则启动；
// 截图未变且上一轮视觉有遗留 → 沿用 failure 到新 head。
func decideVisual(in visualDecisionInput) visualDecision {
	if !in.CodeAllTerminalOK || !in.HasVisualCombos || !in.AnyUIChanged || !in.HasScreenshots {
		return visualNone
	}
	if in.ScreensChanged || in.AnyUIChangedSinceLastVisual {
		if in.ScreensUnavailable {
			return visualScreensUnavailable
		}
		return visualStart
	}
	if in.PriorVisualFailed {
		return visualCarryFailure
	}
	return visualNone
}

// decideVisualForPR 组装真实输入并调 decideVisual；返回决策、本次截图指纹、
// 截图不可用原因（决策为 visualScreensUnavailable 时非空）、上一轮视觉运行
// （沿用场景用）。
func (s *ReviewService) decideVisualForPR(ctx context.Context, ledger *model.ReviewPullRequest,
	pr *githubapi.PullRequest, codeRuns []model.ReviewRun) (visualDecision, string, string, []model.ReviewRun, error) {
	in := visualDecisionInput{CodeAllTerminalOK: true}
	anyUI, anySinceLast := false, false
	for _, r := range codeRuns {
		if r.Status != model.ReviewRunCompleted {
			in.CodeAllTerminalOK = false
			continue
		}
		var result api.ReviewCodeResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			in.CodeAllTerminalOK = false
			continue
		}
		for _, f := range result.Findings {
			if f.Level == api.P0 || f.Level == api.P1 || f.Level == api.P2 {
				in.CodeAllTerminalOK = false
			}
		}
		anyUI = anyUI || result.UiChanged
		anySinceLast = anySinceLast || result.UiChangedSinceLastVisual
	}
	in.AnyUIChanged = anyUI
	in.AnyUIChangedSinceLastVisual = anySinceLast

	// 视觉组合是否配置。
	var settings model.ProjectReviewSettings
	if err := s.db.Where("project_id = ?", ledger.ProjectID).First(&settings).Error; err != nil {
		return visualNone, "", "", nil, errs.ErrInternal
	}
	visualCombos, err := parseCombinations(settings.VisualCombinationsJSON)
	if err != nil {
		return visualNone, "", "", nil, errs.ErrInternal
	}
	in.HasVisualCombos = len(visualCombos) > 0

	refs := collectScreenshotURLs(pr.Body)
	in.HasScreenshots = len(refs) > 0
	fingerprint := screenshotFingerprint(refs)
	in.ScreensChanged = fingerprint != ledger.VisualScreensFingerprint

	// 截图能否下载只在「本轮真的要开视觉」时才探测（ScreensChanged 或
	// AnyUIChangedSinceLastVisual 成立，决策才可能走到 visualStart）：指纹
	// 未变时不拉图——那走的是沿用/带过上一轮结论的路径，探测失败只会把
	// 上一轮的 failure 带过分支误写成「截图不可用」。探测第一张成功即停、
	// 不落账；全部失败时不建视觉运行（决策改报 visualScreensUnavailable），
	// 且指纹不入账——下次同步事件指纹仍不同，会重新尝试下载。
	if in.CodeAllTerminalOK && in.HasVisualCombos && in.AnyUIChanged && in.HasScreenshots &&
		(in.ScreensChanged || in.AnyUIChangedSinceLastVisual) {
		ok, reason := s.probeScreenshots(ctx, ledger.InstallationID, pr.Body)
		if !ok {
			in.ScreensUnavailable = true
			in.ScreensUnavailableReason = reason
		}
	}

	// 上一轮（更早 round）视觉运行是否带 blocker/worth_fixing。历史只看
	// 本项目账本——其他项目同仓库的残留运行不构成「上一轮」。
	var allPrior []model.ReviewRun
	if err := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND kind = ? AND round_number < ?",
		ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, model.ReviewKindVisual, ledger.RoundNumber).
		Order("round_number DESC, created_at ASC").Find(&allPrior).Error; err != nil {
		return visualNone, "", "", nil, errs.ErrInternal
	}
	lastRound := 0
	var priorRuns []model.ReviewRun
	for _, r := range allPrior {
		if lastRound == 0 {
			lastRound = r.RoundNumber
		}
		if r.RoundNumber != lastRound {
			break // 只看最近一轮
		}
		priorRuns = append(priorRuns, r)
		if r.Status == model.ReviewRunCompleted && visualResultHasFailure(r.ResultJSON) {
			in.PriorVisualFailed = true
		}
	}
	return decideVisual(in), fingerprint, in.ScreensUnavailableReason, priorRuns, nil
}

// priorVisualPassed 上一轮视觉运行是否全部成功完成且无 blocker/worth_fixing
// ——只有这种结果才能沿用到新 head；失败/超时/取消的轮次不能当通过。
func priorVisualPassed(runs []model.ReviewRun) bool {
	for i := range runs {
		if runs[i].Status != model.ReviewRunCompleted {
			return false
		}
		if visualResultHasFailure(runs[i].ResultJSON) {
			return false
		}
	}
	return len(runs) > 0
}

// priorVisualFailure 上一轮视觉运行是否含 blocker/worth_fixing。
func priorVisualFailure(runs []model.ReviewRun) bool {
	for i := range runs {
		if runs[i].Status == model.ReviewRunCompleted && visualResultHasFailure(runs[i].ResultJSON) {
			return true
		}
	}
	return false
}

// visualResultHasFailure 视觉结论是否含 blocker/worth_fixing。
func visualResultHasFailure(raw string) bool {
	var result api.ReviewVisualResult
	if json.Unmarshal([]byte(raw), &result) != nil {
		return false
	}
	for _, f := range result.Findings {
		if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
			return true
		}
	}
	return false
}

// visualCarryDescription 沿用旧视觉 failure 时的 status 描述。
func visualCarryDescription(priorRuns []model.ReviewRun) string {
	n := 0
	lastRound := 0
	for _, r := range priorRuns {
		if lastRound == 0 {
			lastRound = r.RoundNumber
		}
		if r.RoundNumber != lastRound {
			break
		}
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
				n++
			}
		}
	}
	if n == 0 {
		return "截图复核意见尚未处理"
	}
	return fmt.Sprintf("截图复核意见尚未处理（上轮 %d 条）", n)
}

// createVisualRuns 为当前 round 建视觉运行（每组合一行，queued 等提升）。
// 事务内先复核快照：同步期间总账被推进（新 webhook、手动重跑）或该 head
// 的代码运行被取消时，建出来的是陈旧视觉运行——aborted=true 让调用方
// 放弃本轮同步（新 sync_pending 会重试）。
func (s *ReviewService) createVisualRuns(ledger *model.ReviewPullRequest,
	pr *githubapi.PullRequest, codeRuns []model.ReviewRun, now time.Time) ([]model.ReviewRun, bool, error) {
	var settings model.ProjectReviewSettings
	if err := s.db.Where("project_id = ?", ledger.ProjectID).First(&settings).Error; err != nil {
		return nil, false, errs.ErrInternal
	}
	combos, err := parseCombinations(settings.VisualCombinationsJSON)
	if err != nil {
		return nil, false, errs.ErrInternal
	}
	var runs []model.ReviewRun
	aborted := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var cur model.ReviewPullRequest
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber).
			First(&cur).Error; err != nil {
			return err
		}
		if cur.HeadSHA != ledger.HeadSHA || cur.RoundNumber != ledger.RoundNumber ||
			cur.SyncEpoch != ledger.SyncEpoch {
			aborted = true
			return nil
		}
		var cancelled int64
		if err := tx.Model(&model.ReviewRun{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND kind = ? AND status = ?",
				ledger.ProjectID, ledger.RepoFullName, ledger.PRNumber, ledger.HeadSHA,
				model.ReviewKindCode, model.ReviewRunCancelled).
			Count(&cancelled).Error; err != nil {
			return err
		}
		if cancelled > 0 {
			aborted = true
			return nil
		}
		for i := range combos {
			c := &combos[i]
			t3ProjectID := ""
			if c.T3ProjectId != nil {
				t3ProjectID = *c.T3ProjectId
			}
			run := &model.ReviewRun{
				ID:               uuid.NewString(),
				UserID:           ledger.UserID,
				ProjectID:        ledger.ProjectID,
				Kind:             model.ReviewKindVisual,
				RepoFullName:     ledger.RepoFullName,
				PRNumber:         ledger.PRNumber,
				HeadSHA:          ledger.HeadSHA,
				RoundNumber:      ledger.RoundNumber,
				InstallationID:   ledger.InstallationID,
				TriggerAction:    "visual",
				PRTitle:          pr.Title,
				PRURL:            pr.HTMLURL,
				BaseRef:          pr.BaseRef,
				HeadRef:          pr.HeadRef,
				EnvironmentID:    c.EnvironmentId,
				T3ProjectID:      t3ProjectID,
				InstanceID:       c.InstanceId,
				Model:            c.Model,
				ModelOptionsJSON: canonicalOptionsJSON(c.Options),
				ThreadID:         uuid.NewString(),
				LaunchCommandID:  uuid.NewString(),
				InitialMessageID: uuid.NewString(),
				Status:           model.ReviewRunQueued,
				DeadlineAt:       now,
				WorktreeState:    model.ReviewWorktreeNone,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(run)
			if res.Error != nil {
				return res.Error
			}
			runs = append(runs, *run)
		}
		return s.promoteQueuedRunsTx(tx, ledger.UserID, now)
	})
	if err != nil {
		return nil, false, errs.ErrInternal
	}
	return runs, aborted, nil
}

// screenshotFingerprint 排序后 URL 列表的 SHA-256 前 16 位（hex）。
func screenshotFingerprint(refs []screenshotRef) string {
	urls := make([]string, 0, len(refs))
	for _, r := range refs {
		urls = append(urls, r.URL)
	}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}
