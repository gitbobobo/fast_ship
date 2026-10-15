package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHub PR 自动评审（INT-93）入口：webhook 事务、防抖、排队提升。所有 GitHub
// 调用都在事务外（webhook 事务内调用数为 0）；复杂性收敛在 githubapp 适配器，
// 这里的协调仍是一个「10 秒循环 + kick」。

// prReviewMaxInflightPerUser 用户并发上限：PR 触发同时最多 5 个审查运行（排队不占名额）。
const prReviewMaxInflightPerUser = 5

const (
	// prReviewDebounceDefaultSeconds 防抖缺省 60 秒；0=立即；上限 1 天。
	prReviewDebounceDefaultSeconds = 60
	prReviewDebounceMaxSeconds     = 86400

	// prReviewModelsMin/Max 一条 PR 规则的模型数边界。
	prReviewModelsMin = 1
	prReviewModelsMax = 10

	// prReviewDeliveryRetention webhook 幂等账保留时长。
	prReviewDeliveryRetention = 7 * 24 * time.Hour

	// prReviewCommentMarker 机器人顶层评论的隐藏标记（失效后按此找回）。
	prReviewCommentMarker = "<!-- fast-ship-pr-review -->"
)

// prModelSelection 多模型规则的元素形状（与单个 model_selection 一致）。
type prModelSelection = api.AutomationModelSelection

// prTriggerTypes PR 触发类型清单（查询过滤用）。
var prTriggerTypes = []model.AutomationTriggerType{
	model.AutomationTriggerGitHubPROpened,
	model.AutomationTriggerGitHubPRSynchronize,
}

// parsePRModelSelections 解析规则的 model_selections_json；空串/损坏返回空数组。
func parsePRModelSelections(raw string) ([]prModelSelection, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []prModelSelection
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PRReviewService GitHub PR 评审的协调面：webhook 事务、防抖到期开轮、排队
// 提升、评论/check 同步。由 webhook handler 与调度循环共同驱动；kick 把
// debounce=0 的立即触发交给调度循环（循环仍是唯一执行者）。
type PRReviewService struct {
	db         *gorm.DB
	autos      *repository.AutomationRepository
	runs       *repository.AutomationRunRepository
	projects   *repository.ProjectRepository
	deliveries *repository.GitHubWebhookDeliveryRepository
	debounces  *repository.GitHubPRDebounceRepository
	reviews    *repository.GitHubPRReviewRepository
	sink       *NotificationSink
	cfg        *config.Config
	logger     *zap.Logger

	// gh 评审链路的 GitHub 访问面（真实安装令牌客户端或测试/自测 fake）。
	gh githubapp.PullRequestGitHub
	// threads 取消路径的中断出口（StopThread 同口径）；可 nil（单测）。
	threads prThreadStopper

	kick func() // 调度循环 kick；可 nil（单测直调路径）

	// prLocks 每 PR 一把进程内锁（评论/check 同步串行）。
	prLocks   map[string]*sync.Mutex
	prLocksMu sync.Mutex
}

func NewPRReviewService(
	db *gorm.DB,
	autos *repository.AutomationRepository,
	runs *repository.AutomationRunRepository,
	projects *repository.ProjectRepository,
	deliveries *repository.GitHubWebhookDeliveryRepository,
	debounces *repository.GitHubPRDebounceRepository,
	reviews *repository.GitHubPRReviewRepository,
	gh githubapp.PullRequestGitHub,
	threads prThreadStopper,
	sink *NotificationSink,
	cfg *config.Config,
	logger *zap.Logger,
) *PRReviewService {
	return &PRReviewService{
		db: db, autos: autos, runs: runs, projects: projects,
		deliveries: deliveries, debounces: debounces, reviews: reviews,
		gh: gh, threads: threads, sink: sink, cfg: cfg, logger: logger,
		prLocks: map[string]*sync.Mutex{},
	}
}

// SetKick 绑定调度循环 kick（缓冲 1、非阻塞）。循环未起时安全为 nil。
func (s *PRReviewService) SetKick(fn func()) { s.kick = fn }

// TriggerTypeForAction GitHub action → 触发类型；只认 opened/synchronize。
func TriggerTypeForAction(action string) (model.AutomationTriggerType, bool) {
	switch action {
	case "opened":
		return model.AutomationTriggerGitHubPROpened, true
	case "synchronize":
		return model.AutomationTriggerGitHubPRSynchronize, true
	}
	return "", false
}

// gitHubPRPayload GitHub pull_request 事件 payload 的所需子集（字段名与官方一致）。
type gitHubPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Draft   bool    `json:"draft"`
		HTMLURL string  `json:"html_url"`
		Title   string  `json:"title"`
		Body    *string `json:"body"`
		Head    struct {
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
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ErrDeliveryDuplicated 同一 X-GitHub-Delivery 的重复投递：202 但不再开轮。
var ErrDeliveryDuplicated = errors.New("pr_review: delivery duplicated")

// webhookPRKey 从 payload 取出 per-PR 锁键。number 为 0 或仓库名为空时调用方不取锁。
func webhookPRKey(p *gitHubPRPayload) (repo string, pr int) {
	repo = p.Repository.FullName
	if repo == "" {
		repo = p.PullRequest.Head.Repo.FullName
	}
	if repo == "" {
		repo = p.PullRequest.Base.Repo.FullName
	}
	return repo, p.Number
}

// HandleGitHubWebhook webhook 入口：一个事务内完成幂等登记、过滤、取消旧轮、
// 防抖/立即开轮；事务内 GitHub 调用数为 0。重复投递返回 ErrDeliveryDuplicated
// （handler 仍回 202）。能解析出仓库+PR 时先拿 per-PR 锁再开事务，与 agent
// 代发（SubmitReview / UpdateCheck）串行，避免「取消已提交、审查仍发出」。
func (s *PRReviewService) HandleGitHubWebhook(ctx context.Context, event, deliveryID string, body []byte) error {
	var payload gitHubPRPayload
	// payload 解析失败按忽略处理（已验签说明来自 GitHub，格式异常不重试）。
	_ = json.Unmarshal(body, &payload)

	now := nowUTC()
	runTx := func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.handleWebhookTx(tx, event, deliveryID, &payload, now)
		})
	}

	// 先锁后事务：agent 持锁期间会读库再调 GitHub；若 webhook 先开事务再等锁，
	// 两边会交叉等待。锁只包本事务（无 GitHub 调用），kick 在解锁之后。
	repo, pr := webhookPRKey(&payload)
	var err error
	if repo != "" && pr != 0 {
		s.withPRLock(repo, pr, func() { err = runTx() })
	} else {
		err = runTx()
	}
	if err != nil {
		if errors.Is(err, ErrDeliveryDuplicated) {
			return err
		}
		s.logger.Error("处理 GitHub webhook 失败",
			zap.String("delivery", deliveryID), zap.String("event", event), zap.Error(err))
		return err
	}
	s.notifyLoop()
	return nil
}

// notifyLoop 非阻塞 kick（缓冲 1）；循环活着时立即补一轮 tick。
func (s *PRReviewService) notifyLoop() {
	if s.kick != nil {
		s.kick()
	}
}

func (s *PRReviewService) handleWebhookTx(tx *gorm.DB, event, deliveryID string, p *gitHubPRPayload, now time.Time) error {
	// 幂等账：重复投递直接短路。
	inserted, err := s.deliveries.InsertTx(tx, &model.GitHubWebhookDelivery{
		DeliveryID: deliveryID,
		Event:      event,
		ReceivedAt: now,
	})
	if err != nil {
		return err
	}
	if !inserted {
		return ErrDeliveryDuplicated
	}

	// 只处理 pull_request 的 opened/synchronize；其余事件只记账。
	triggerType, ok := TriggerTypeForAction(p.Action)
	if event != "pull_request" || !ok {
		return nil
	}

	// draft 与 fork 不审：draft 打开后标可审的 PR，等下一次非 draft、非 fork 的
	// synchronize；fork 的 head 仓库与 base 仓库不同名同样跳过。
	head := p.PullRequest.Head
	if p.PullRequest.Draft || head.Repo.Fork ||
		!strings.EqualFold(head.Repo.FullName, p.PullRequest.Base.Repo.FullName) {
		return nil
	}

	repoFullName := p.Repository.FullName
	if repoFullName == "" {
		repoFullName = head.Repo.FullName
	}
	if repoFullName == "" || p.Number == 0 {
		return nil
	}

	// 仓库范围：GitHub App 安装到的仓库中，已和 Fast Ship 项目关联、且该项目
	// 绑定了 GitHub Token 的才审查——只填仓库名、不持有 Token 的用户不能借用
	// 共享 App 安装令牌在别人的仓上发评论 / check / REQUEST_CHANGES。
	linked, err := listProjectsByGitHubRepoTx(tx, repoFullName)
	if err != nil {
		return err
	}
	if len(linked) == 0 {
		return nil // 不关联不取消、不开轮
	}

	// 迟到的 opened/synchronize 可能带着已被更新 head 取代的 SHA。先判过期再
	// 取消：否则 delayed opened(A) 会把当前 B 的在途运行取消并重开 A。
	stale, err := isStalePRHeadTx(tx, repoFullName, p.Number, head.SHA, p.Action)
	if err != nil {
		return err
	}
	if stale {
		return nil
	}
	if err := s.reviews.RecordSeenSHATx(tx, repoFullName, p.Number, head.SHA, now); err != nil {
		return err
	}

	// SHA 变化：该 PR 下旧 SHA 的全部在途运行一律取消（跨规则跨用户跨模型），
	// 名额随终态释放；不登记 settle/archive。顺带丢掉旧 SHA 的防抖行——opened
	// 规则的窗口不能在 synchronize 新 SHA 之后再开火。
	cancelled, err := s.runs.CancelSupersededPRRunsTx(tx, repoFullName, p.Number, head.SHA, now)
	if err != nil {
		return err
	}
	if err := s.runs.ConsumeRunTokensTx(tx, cancelled, now); err != nil {
		return err
	}
	if err := s.debounces.DeleteStaleSHATx(tx, repoFullName, p.Number, head.SHA); err != nil {
		return err
	}
	// 有取消就置同步标记：即使本次没有规则开新轮，评论/check 也要显示「已取消」。
	if err := markReviewPendingAfterCancelTx(tx, repoFullName, p.Number, cancelled, now); err != nil {
		return err
	}

	// 规则匹配：trigger_type 与 action 一致且启用；issue_project_id 非空必须匹配
	// 该项目，为空则该用户任一关联项目命中。按 automation id 只开一轮。
	matched, err := matchPRRules(tx, triggerType, linked, now)
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		return nil
	}

	for i := range matched {
		rule := &matched[i]
		if rule.a.DebounceSeconds > 0 {
			err = s.debounces.UpsertTx(tx, &model.GitHubPRDebounce{
				AutomationID:   rule.a.ID,
				RepoFullName:   repoFullName,
				PRNumber:       p.Number,
				UserID:         rule.a.UserID,
				ProjectID:      rule.projectID,
				InstallationID: p.Installation.ID,
				Action:         p.Action,
				HeadSHA:        head.SHA,
				PRTitle:        p.PullRequest.Title,
				PRURL:          p.PullRequest.HTMLURL,
				BaseRef:        p.PullRequest.Base.Ref,
				HeadRef:        head.Ref,
				FireAt:         now.Add(time.Duration(rule.a.DebounceSeconds) * time.Second),
				UpdatedAt:      now,
			})
			if err != nil {
				return err
			}
			continue
		}
		// debounce=0：本事务内直接开一轮并提升排队。
		if err := s.openPRRoundTx(tx, rule.a, rule.projectID, p.Installation.ID, repoFullName, p, head.SHA, now); err != nil {
			return err
		}
	}
	return nil
}

// isStalePRHeadTx 判断迟到 webhook 的 head SHA 是否已经过期。当前 head 优先取
// 未开火的防抖行，否则取评审账；本次 SHA 就是当前 head → 接受。已经受理过、
// 但不是当前 head 的 SHA 一律拒绝（含「A 已有 review、B 还在 debounce」时迟到
// 的 A，以及 force-push 回已见过的 SHA）。其余视为新 SHA。
func isStalePRHeadTx(tx *gorm.DB, repoFullName string, prNumber int, sha, action string) (bool, error) {
	latest, err := currentPRHeadSHATx(tx, repoFullName, prNumber)
	if err != nil {
		return false, err
	}
	if latest == sha {
		return false, nil
	}
	var seen int64
	if err := tx.Model(&model.GitHubPRSeenSHA{}).
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?", repoFullName, prNumber, sha).
		Count(&seen).Error; err != nil {
		return false, err
	}
	if seen > 0 {
		return true, nil
	}
	if action == "opened" && latest != "" {
		return true, nil
	}
	if latest == "" {
		return false, nil
	}
	var n int64
	if err := tx.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND pr_number = ? AND head_sha = ?", repoFullName, prNumber, sha).
		Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// currentPRHeadSHATx 当前受理的 head：未开火的防抖行优先（窗口内的新 SHA 比
// 已开轮的评审账更新）；没有 debounce 才看评审账。
func currentPRHeadSHATx(tx *gorm.DB, repoFullName string, prNumber int) (string, error) {
	var d model.GitHubPRDebounce
	err := tx.Where("repo_full_name = ? AND pr_number = ?", repoFullName, prNumber).
		Order("updated_at DESC").First(&d).Error
	if err == nil && d.HeadSHA != "" {
		return d.HeadSHA, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	var rev model.GitHubPRReview
	err = tx.Where("repo_full_name = ? AND pr_number = ?", repoFullName, prNumber).First(&rev).Error
	if err == nil {
		return rev.HeadSHA, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return "", err
}

// prRuleMatch 命中的规则与其宿主项目。
type prRuleMatch struct {
	a         *model.Automation
	projectID string
}

// matchPRRules 按项目集合匹配启用规则；同一 automation 只留一条。查询失败
// 返回错误（delivery 已入账，吞掉会导致「已受理但漏开轮」且 GitHub 不再重试）。
func matchPRRules(tx *gorm.DB, triggerType model.AutomationTriggerType, projects []model.Project, now time.Time) ([]prRuleMatch, error) {
	seen := map[string]bool{}
	var out []prRuleMatch
	for i := range projects {
		project := &projects[i]
		var autos []model.Automation
		if err := tx.
			Where("user_id = ? AND enabled = ? AND trigger_type = ? AND (issue_project_id IS NULL OR issue_project_id = ?)",
				project.UserID, true, triggerType, project.ID).
			Find(&autos).Error; err != nil {
			return nil, err
		}
		for j := range autos {
			a := &autos[j]
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			out = append(out, prRuleMatch{a: a, projectID: project.ID})
		}
	}
	return out, nil
}

// listProjectsByGitHubRepoTx 反查关联项目（owner/repo 大小写不敏感）。
// 必须绑定公开或代码仓库 Token：项目创建允许「只填仓库不选 Token」，那种行
// 不能当作对本仓的授权。webhook 事务内不调 GitHub，无法现场核对该 Token 是否
// 真能访问此仓——那是项目绑定时的既有缺口。
func listProjectsByGitHubRepoTx(tx *gorm.DB, repoFullName string) ([]model.Project, error) {
	owner, name, ok := strings.Cut(repoFullName, "/")
	if !ok {
		return nil, nil
	}
	var projects []model.Project
	err := tx.
		Where("LOWER(github_owner) = LOWER(?) AND LOWER(github_repo) = LOWER(?)", owner, name).
		Find(&projects).Error
	if err != nil {
		return nil, err
	}
	out := projects[:0]
	for i := range projects {
		if projects[i].PublicGitHubTokenID != "" || projects[i].CodeGitHubTokenID != "" {
			out = append(out, projects[i])
		}
	}
	return out, nil
}

// ---- 开轮与排队提升 ----

// prTriggerContext PR 运行的触发上下文（提示词占位与派发渲染需要；PR 元信息
// 同时落在运行行的专用列上，API 响应以列为准）。
type prTriggerContext struct {
	ProjectID   string `json:"project_id"`
	Action      string `json:"action"`
	Repo        string `json:"repo_full_name"`
	PRNumber    int    `json:"pr_number"`
	PRTitle     string `json:"pr_title"`
	PRURL       string `json:"pr_url"`
	HeadSHA     string `json:"head_sha"`
	BaseRef     string `json:"base_ref"`
	HeadRef     string `json:"head_ref"`
	RoundNumber int    `json:"round_number"`
}

// openPRRoundTx 开一轮：同一 SHA 复用已有轮次号，每个模型一行 queued；
// 同一 SHA+模型被 (automation_id, trigger_key) 唯一键吃掉。零插入不推进
// github_pr_reviews.round_number（否则同步会查到空轮并清掉 pending）。随后提升排队。
func (s *PRReviewService) openPRRoundTx(tx *gorm.DB, a *model.Automation, projectID string, installationID int64, repoFullName string, p *gitHubPRPayload, headSHA string, now time.Time) error {
	round, err := repository.RoundNumberForHeadSHATx(tx, repoFullName, p.Number, headSHA)
	if err != nil {
		return err
	}
	models, err := parsePRModelSelections(a.ModelSelectionsJSON)
	if err != nil || len(models) == 0 {
		// 配置损坏（正常路径被 CRUD 校验拦住）：跳过该规则，不阻断其它规则。
		s.logger.Warn("PR 规则的模型选择缺失或损坏，跳过开轮",
			zap.String("automation_id", a.ID), zap.Error(err))
		return nil
	}

	head := p.PullRequest.Head
	ctxJSON, err := json.Marshal(prTriggerContext{
		ProjectID:   projectID,
		Action:      p.Action,
		Repo:        repoFullName,
		PRNumber:    p.Number,
		PRTitle:     p.PullRequest.Title,
		PRURL:       p.PullRequest.HTMLURL,
		HeadSHA:     headSHA,
		BaseRef:     p.PullRequest.Base.Ref,
		HeadRef:     head.Ref,
		RoundNumber: round,
	})
	if err != nil {
		return err
	}

	inserted := 0
	for _, sel := range models {
		run := s.newPRRunRow(a, sel, repoFullName, p.Number, headSHA, round, installationID, ctxJSON, now)
		if err := tx.Create(run).Error; err != nil {
			if !isRunDuplicateTriggerKey(err) {
				return err
			}
			continue
		}
		inserted++
	}

	if inserted > 0 {
		// 每 PR 一行的同步账推进到本轮；评论/check 由事务外同步收敛。
		review := &model.GitHubPRReview{
			RepoFullName:   repoFullName,
			PRNumber:       p.Number,
			InstallationID: installationID,
			RoundNumber:    round,
			HeadSHA:        headSHA,
			SyncPending:    true,
			UpdatedAt:      now,
		}
		if err := s.reviews.UpsertTx(tx, review); err != nil {
			return err
		}
	}

	return s.promoteQueuedPRRunsTx(tx, a.UserID, now)
}

// newPRRunRow 构造一个模型的排队运行：config_snapshot.model_selection 是这一个
// 模型的单对象（现有 launch 路径无需改动），deadline 先按 triggered_at 起算，
// 提升时重置为真正开始派发的时间 + timeout。
func (s *PRReviewService) newPRRunRow(a *model.Automation, sel prModelSelection, repoFullName string, prNumber int, headSHA string, round int, installationID int64, ctxJSON []byte, now time.Time) *model.AutomationRun {
	// 快照复用 cron/issue 的构造，但 model_selection 换成这一个模型。
	perModel := *a
	if raw, err := json.Marshal(sel); err == nil {
		perModel.ModelSelectionJSON = string(raw)
	}
	return &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerType:        a.TriggerType,
		TriggerKey:         prTriggerKey(repoFullName, prNumber, headSHA, sel.InstanceId, sel.Model),
		TriggerContextJSON: string(ctxJSON),
		TriggeredAt:        now,
		ConfigSnapshotJSON: snapshotJSON(&perModel),
		EnvironmentID:      a.EnvironmentID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             model.AutomationRunQueued,
		DeadlineAt:         now.Add(time.Duration(a.TimeoutSeconds) * time.Second),
		PostState:          model.AutomationPostStateNone,
		RepoFullName:       repoFullName,
		PRNumber:           prNumber,
		HeadSHA:            headSHA,
		RoundNumber:        round,
		InstallationID:     installationID,
		ModelInstanceID:    sel.InstanceId,
		ModelName:          sel.Model,
	}
}

// prTriggerKey `pr:<repo>:<number>:<sha>:<instance_id>:<model>`；同一 SHA+模型被
// UNIQUE(automation_id, trigger_key) 吃掉（重复投递/并发开轮收敛为一行）。
func prTriggerKey(repo string, number int, sha, instanceID, model string) string {
	return "pr:" + url.PathEscape(repo) + ":" +
		url.PathEscape(strconv.Itoa(number)) + ":" +
		url.PathEscape(sha) + ":" +
		url.PathEscape(instanceID) + ":" +
		url.PathEscape(model)
}

// promoteQueuedPRRunsTx 排队提升：在途数（”/dispatched/running）不足上限时按
// triggered_at 把最老 queued 改为 ”，deadline 重置为 now+timeout（超时从真正
// 开始派发算）。排队中的运行不进 ListActivePastDeadlineTx，不会被误判超时。
func (s *PRReviewService) promoteQueuedPRRunsTx(tx *gorm.DB, userID string, now time.Time) error {
	for {
		inflight, err := s.runs.CountPRInflightTx(tx, userID)
		if err != nil {
			return err
		}
		if inflight >= prReviewMaxInflightPerUser {
			return nil
		}
		oldest, err := s.runs.ListOldestQueuedPRRunsTx(tx, userID, 1)
		if err != nil {
			return err
		}
		if len(oldest) == 0 {
			return nil
		}
		run := oldest[0]
		timeout := parseAutomationSnapshot(run.ConfigSnapshotJSON).TimeoutSeconds
		if timeout <= 0 {
			timeout = automationTimeoutDefaultSeconds
		}
		n, err := s.runs.UpdateColumnsWhereTx(tx, run.ID,
			"status = ?", []any{model.AutomationRunQueued},
			map[string]any{
				"status":      string(model.RunStatusPendingRun),
				"deadline_at": now.Add(time.Duration(timeout) * time.Second),
			})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // 状态被并发改写（防御；单连接事务内不应发生）
		}
		// 跨 PR 提升：PR A 释放名额后 PR B 从 queued 变为待认领，必须置 B 的
		// sync_pending，否则评论/check 会一直停在「排队中」直到 B 自己终态。
		if err := markPRReviewPendingTx(tx, &run, now); err != nil {
			return err
		}
	}
}

// PromoteQueuedPRRuns 调度步骤：名额释放后把 queued 提升为待认领。开轮路径
// 只提升当时涉及的用户；超时/结束/派发失败发生在其它事务，必须由循环扫一遍。
func (s *PRReviewService) PromoteQueuedPRRuns(now time.Time) {
	ids, err := s.runs.ListUserIDsWithQueuedPRRunsTx(s.db)
	if err != nil {
		s.logger.Error("查询排队用户失败", zap.Error(err))
		return
	}
	for _, userID := range ids {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			return s.promoteQueuedPRRunsTx(tx, userID, now)
		})
		if err != nil {
			s.logger.Error("提升排队运行失败", zap.String("user_id", userID), zap.Error(err))
		}
	}
}

// ---- 调度循环步骤（事务外驱动，短事务执行） ----

// prDebounceBatchLimit 单轮 tick 最多处理的到期防抖行数。
const prDebounceBatchLimit = 50

// ProcessDueDebounces 调度步骤：扫描到期防抖行，逐行短事务核对规则仍有效后
// 开轮并提升；顺带清理过期 delivery。每行独立容错。
func (s *PRReviewService) ProcessDueDebounces(now time.Time) {
	due, err := s.debounces.ListDueTx(s.db, now, prDebounceBatchLimit)
	if err != nil {
		s.logger.Error("查询到期防抖失败", zap.Error(err))
		return
	}
	for i := range due {
		s.fireDebounce(&due[i], now)
	}
	if _, err := s.deliveries.DeleteOlderThanTx(s.db, now.Add(-prReviewDeliveryRetention)); err != nil {
		s.logger.Warn("清理 webhook 幂等账失败", zap.Error(err))
	}
}

func (s *PRReviewService) fireDebounce(d *model.GitHubPRDebounce, now time.Time) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 消费防抖行（条件删除：0 行=已被并发处理）。
		res := tx.Where("automation_id = ? AND repo_full_name = ? AND pr_number = ? AND fire_at <= ?",
			d.AutomationID, d.RepoFullName, d.PRNumber, now).
			Delete(&model.GitHubPRDebounce{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}

		var a model.Automation
		if err := tx.Where("id = ?", d.AutomationID).First(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 规则已删除：防抖行随之消失
			}
			return err
		}
		triggerType, ok := TriggerTypeForAction(d.Action)
		if !a.Enabled || !ok || a.TriggerType != triggerType {
			return nil // 规则已停用/改类型：丢弃本次窗口
		}

		payload := gitHubPRPayload{
			Action: d.Action,
			Number: d.PRNumber,
		}
		payload.PullRequest.Head.SHA = d.HeadSHA
		payload.PullRequest.Head.Ref = d.HeadRef
		payload.PullRequest.Head.Repo.FullName = d.RepoFullName
		payload.PullRequest.Base.Ref = d.BaseRef
		payload.PullRequest.Base.Repo.FullName = d.RepoFullName
		payload.PullRequest.Title = d.PRTitle
		payload.PullRequest.HTMLURL = d.PRURL
		payload.Repository.FullName = d.RepoFullName
		payload.Installation.ID = d.InstallationID

		return s.openPRRoundTx(tx, &a, d.ProjectID, d.InstallationID, d.RepoFullName, &payload, d.HeadSHA, now)
	})
	if err != nil {
		s.logger.Error("防抖开轮失败",
			zap.String("automation_id", d.AutomationID),
			zap.String("repo", d.RepoFullName),
			zap.Int("pr", d.PRNumber), zap.Error(err))
	}
}
