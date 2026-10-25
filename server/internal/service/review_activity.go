package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

// 跨项目 PR 审查动态（INT-141）：/reviews 首页的数据源。分组（进行中 /
// 需要处理 / 最近通过）、两段进度、发现计数与待处理原因全部由服务端算好，
// 前端只负责展示。只读，不做任何 GitHub/T3 调用。

const (
	reviewActivityDefaultLimit = 50
	reviewActivityMaxLimit     = 200
	// reviewActivityPassedWindow 最近通过分组只保留近 7 天有结论的 PR。
	reviewActivityPassedWindow = 7 * 24 * time.Hour
)

// 待处理 issue 的短原因文案（契约固定；具体说明写在 message，不猜环境原因）。
const (
	reviewIssueReasonDispatchFailed = "派发失败"
	reviewIssueReasonTimeout        = "超时"
	reviewIssueReasonRunFailed      = "审查失败"
	reviewIssueReasonUnparsed       = "结论无法解析"
	reviewIssueReasonCodeFailure    = "审查未通过"
	reviewIssueReasonNotFinished    = "审查未完成"
	reviewIssueReasonVisualFailure  = "视觉审查未通过"
)

// ListPullRequestActivity 首页动态：范围是当前用户已开启审查（enabled）的
// 项目，总账只取与设置行同仓库的行；project_id 筛选可选且必须属于当前用户。
// counts/total 是过滤范围内全量 PR 的统计，items 按排序后切片返回。
func (s *ReviewService) ListPullRequestActivity(ctx context.Context, userID, projectID string, limit, offset int) (*api.ReviewPullRequestActivityPage, error) {
	if projectID != "" {
		if _, err := s.ownedProject(userID, projectID); err != nil {
			return nil, err
		}
	}
	if limit <= 0 {
		limit = reviewActivityDefaultLimit
	}
	if limit > reviewActivityMaxLimit {
		limit = reviewActivityMaxLimit
	}
	if offset < 0 {
		offset = 0
	}

	page := &api.ReviewPullRequestActivityPage{Items: []api.ReviewPullRequestActivity{}}

	// 范围：已开启审查的项目（enabled），可选按项目收窄。关联数据在同一个
	// 读取事务里取，保证账本、防抖、运行是同一个快照：否则防抖消费/开轮的
	// 事务刚好落在两次查询之间时，旧账本配不上新轮运行，正在开轮的 PR 会被
	// 误判成「没有运行」而暂时消失（唯一进行中的 PR 消失会让前端停掉轮询）。
	var (
		settings              []model.ProjectReviewSettings
		projects              []model.Project
		prs                   []model.ReviewPullRequest
		debounces             []model.ReviewDebounce
		runs                  []model.ReviewRun
		scoped                []*model.ReviewPullRequest
		runKeys               []string
		settingsByProject     = map[string]model.ProjectReviewSettings{}
		nameByProject         = map[string]string{}
		visualCombosByProject = map[string]int{}
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settingsQuery := tx.Where("user_id = ? AND enabled", userID)
		if projectID != "" {
			settingsQuery = settingsQuery.Where("project_id = ?", projectID)
		}
		if err := settingsQuery.Find(&settings).Error; err != nil {
			return err
		}
		if len(settings) == 0 {
			return nil
		}
		projectIDs := make([]string, 0, len(settings))
		repoSet := map[string]struct{}{}
		for _, row := range settings {
			settingsByProject[row.ProjectID] = row
			projectIDs = append(projectIDs, row.ProjectID)
			if row.RepoFullName != "" {
				repoSet[row.RepoFullName] = struct{}{}
			}
		}
		repos := make([]string, 0, len(repoSet))
		for repo := range repoSet {
			repos = append(repos, repo)
		}

		if err := tx.Where("id IN ?", projectIDs).Find(&projects).Error; err != nil {
			return err
		}
		for _, p := range projects {
			nameByProject[p.ID] = p.Name
		}
		for id, row := range settingsByProject {
			if combos, err := parseCombinations(row.VisualCombinationsJSON); err == nil {
				visualCombosByProject[id] = len(combos)
			}
		}

		// 总账按 (project, repo, pr) 全量取，仓库与设置行不一致的残留行在
		// 内存过滤。
		if err := tx.
			Where("project_id IN ? AND repo_full_name IN ?", projectIDs, repos).
			Order("updated_at DESC").
			Find(&prs).Error; err != nil {
			return err
		}
		scoped = make([]*model.ReviewPullRequest, 0, len(prs))
		runKeys = make([]string, 0, len(prs))
		for i := range prs {
			pr := &prs[i]
			row, ok := settingsByProject[pr.ProjectID]
			if !ok || pr.RepoFullName != row.RepoFullName {
				continue
			}
			scoped = append(scoped, pr)
			// 当前轮运行的匹配键：项目 + 轮次 + head 必须同时等于账本值，新
			// head 已推进但未开轮时旧轮 runs 不得混入（只查当前轮，不查历史
			// 轮）。项目 id 也进键：同仓库换项目接管后，两个项目的同号
			// PR/同轮/同 head 运行不能合并。
			runKeys = append(runKeys, pr.ProjectID+"#"+pr.RepoFullName+"#"+strconv.Itoa(pr.PRNumber)+"#"+
				strconv.Itoa(pr.RoundNumber)+"#"+pr.HeadSHA)
		}
		if len(scoped) == 0 {
			return nil
		}

		// 防抖行批量取（按 project#repo#pr 建索引）。
		if err := tx.
			Where("project_id IN ? AND repo_full_name IN ?", projectIDs, repos).
			Find(&debounces).Error; err != nil {
			return err
		}

		// 当前轮运行批量取（键见 runKeys）。
		return tx.
			Where("(project_id || '#' || repo_full_name || '#' || pr_number || '#' || round_number || '#' || head_sha) IN ?", runKeys).
			Order("created_at ASC").
			Find(&runs).Error
	})
	if err != nil {
		return nil, errs.ErrInternal
	}
	if len(settings) == 0 || len(scoped) == 0 {
		return page, nil
	}

	// 防抖行按 project#repo#pr 建索引。
	debounceByPR := map[string]*model.ReviewDebounce{}
	for i := range debounces {
		d := &debounces[i]
		debounceByPR[d.ProjectID+"#"+d.RepoFullName+"#"+strconv.Itoa(d.PRNumber)] = d
	}

	runsByPR := map[string][]model.ReviewRun{}
	for _, r := range runs {
		key := r.ProjectID + "#" + r.RepoFullName + "#" + strconv.Itoa(r.PRNumber) + "#" + strconv.Itoa(r.RoundNumber) + "#" + r.HeadSHA
		runsByPR[key] = append(runsByPR[key], r)
	}

	// 全量分组后再切片：counts/total 是过滤范围内全部 PR 的统计。
	now := time.Now().UTC()
	// PR 链接兜底按 GitHub 身份构造：审查执行本期只有 GitHub 一条链路，
	// 历史运行的 forge 身份就是 github——项目改绑 forgejo 后历史链接不
	// 该改写过去（INT-166 方向审计结论；P1 接入 Forgejo 执行时随运行记
	// 录存 forge 身份）。
	ghLinks := githubLinks(githubWebBase(s.cfg))
	entries := make([]reviewActivityEntry, 0, len(scoped))
	for _, pr := range scoped {
		key := pr.ProjectID + "#" + pr.RepoFullName + "#" + strconv.Itoa(pr.PRNumber) + "#" + strconv.Itoa(pr.RoundNumber) + "#" + pr.HeadSHA
		item, sortAt, ok := buildPullRequestActivity(pr, nameByProject[pr.ProjectID],
			settingsByProject[pr.ProjectID], visualCombosByProject[pr.ProjectID],
			debounceByPR[pr.ProjectID+"#"+pr.RepoFullName+"#"+strconv.Itoa(pr.PRNumber)],
			runsByPR[key], now, ghLinks)
		if !ok {
			continue
		}
		entries = append(entries, reviewActivityEntry{
			item: item, order: reviewActivityGroupOrder(item.Group), sortAt: sortAt,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := &entries[i], &entries[j]
		if a.order != b.order {
			return a.order < b.order
		}
		if !a.sortAt.Equal(b.sortAt) {
			return a.sortAt.After(b.sortAt)
		}
		if a.item.ProjectId != b.item.ProjectId {
			return a.item.ProjectId < b.item.ProjectId
		}
		if a.item.RepoFullName != b.item.RepoFullName {
			return a.item.RepoFullName < b.item.RepoFullName
		}
		return a.item.PrNumber < b.item.PrNumber
	})
	for i := range entries {
		switch entries[i].item.Group {
		case api.ReviewPullRequestActivityGroupInProgress:
			page.Counts.InProgress++
		case api.ReviewPullRequestActivityGroupNeedsAction:
			page.Counts.NeedsAction++
		case api.ReviewPullRequestActivityGroupRecentlyPassed:
			page.Counts.RecentlyPassed++
		}
	}
	page.Total = len(entries)
	if offset < len(entries) {
		end := offset + limit
		if end > len(entries) {
			end = len(entries)
		}
		for _, e := range entries[offset:end] {
			page.Items = append(page.Items, e.item)
		}
	}
	return page, nil
}

// reviewActivityEntry 动态行 + 排序键：order 是组序（进行中→需要处理→
// 最近通过）；sortAt 在进行中/需要处理组是账本 updated_at，最近通过组是
// 结论完成时刻（缺失回退账本 updated_at）。
type reviewActivityEntry struct {
	item   api.ReviewPullRequestActivity
	order  int
	sortAt time.Time
}

// reviewActivityGroupOrder 组间排序序号。
func reviewActivityGroupOrder(g api.ReviewPullRequestActivityGroup) int {
	switch g {
	case api.ReviewPullRequestActivityGroupNeedsAction:
		return 1
	case api.ReviewPullRequestActivityGroupRecentlyPassed:
		return 2
	}
	return 0
}

// buildPullRequestActivity 单个总账行 → 动态行（契约「数据分组算法」）。
// 返回是否纳入首页与组内排序时间；防抖优先，其次按当前轮运行的执行状态判定。
// links 按项目代码仓集成解析，PR 标题/URL 快照缺失时兜底构造 PR 链接。
func buildPullRequestActivity(pr *model.ReviewPullRequest, projectName string,
	settings model.ProjectReviewSettings, visualComboCount int,
	debounce *model.ReviewDebounce, runs []model.ReviewRun, now time.Time,
	links forgeLinks,
) (api.ReviewPullRequestActivity, time.Time, bool) {
	// 防抖优先：窗口内的最新推送只显示「约 N 秒后开始」，不看任何运行。
	if debounce != nil {
		item := api.ReviewPullRequestActivity{
			ProjectId:      pr.ProjectID,
			ProjectName:    projectName,
			RepoFullName:   pr.RepoFullName,
			PrNumber:       pr.PRNumber,
			PrTitle:        debounce.PRTitle,
			PrUrl:          debounce.PRURL,
			Group:          api.ReviewPullRequestActivityGroupInProgress,
			Stage:          api.Ptr(api.ReviewPullRequestActivityStageDebouncing),
			DebounceFireAt: api.Ptr(debounce.FireAt.UTC().Format(time.RFC3339Nano)),
			RoundNumber:    pr.RoundNumber,
			HeadSha:        debounce.HeadSHA,
			UpdatedAt:      pr.UpdatedAt.UTC().Format(time.RFC3339Nano),
			StartedAt:      debounce.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Stages:         []api.ReviewStageProgress{},
			Issues:         []api.ReviewActivityIssue{},
			CanRerun:       reviewActivityCanRerun(settings, pr),
		}
		if item.PrUrl == "" {
			item.PrUrl = links.PullURL(pr.RepoFullName, pr.PRNumber)
		}
		return item, pr.UpdatedAt, true
	}

	// 没有任何当前轮 run（动作未勾选、head 已推进但未开轮）→ 不返回。
	if len(runs) == 0 {
		return api.ReviewPullRequestActivity{}, time.Time{}, false
	}
	// cancelled 运行单独记账：同轮可能还有仍在跑/已失败的组合，不能整行丢弃
	// （tokens 提交时 head 已变只取消当前一条，其余组合还在继续）。只有
	// 「其余都是干净 completed、又夹着 cancelled」时才不返回。
	anyCancelled := false

	var codeRuns, visualRuns []model.ReviewRun
	var meta *model.ReviewRun
	startedAt := runs[0].CreatedAt
	var latestFinished *time.Time
	for i := range runs {
		r := &runs[i]
		if r.Kind == model.ReviewKindVisual {
			visualRuns = append(visualRuns, *r)
		} else {
			codeRuns = append(codeRuns, *r)
		}
		// 标题/URL/head 取最近一次运行的快照。
		if meta == nil || r.CreatedAt.After(meta.CreatedAt) {
			meta = r
		}
		if r.CreatedAt.Before(startedAt) {
			startedAt = r.CreatedAt
		}
		if r.FinishedAt != nil && (latestFinished == nil || r.FinishedAt.After(*latestFinished)) {
			t := *r.FinishedAt
			latestFinished = &t
		}
	}

	group := api.ReviewPullRequestActivityGroupInProgress
	var stage *api.ReviewPullRequestActivityStage
	active := false
	allQueued := true
	for i := range runs {
		if runs[i].Status == model.ReviewRunCancelled {
			anyCancelled = true
			continue
		}
		if runs[i].Status.Terminal() {
			// 已有终态说明这一段至少开始过，不能再整段标成「排队中」。
			allQueued = false
			continue
		}
		active = true
		if runs[i].Status != model.ReviewRunQueued {
			allQueued = false
		}
	}
	if active {
		// 执行状态只看 runs：账本 pending 不能把已终态 run 判成进行中。
		switch {
		case allQueued:
			stage = api.Ptr(api.ReviewPullRequestActivityStageQueued)
		default:
			stage = api.Ptr(api.ReviewPullRequestActivityStageReviewing)
		}
	}

	findings := api.ReviewFindingCounts{}
	issues := []api.ReviewActivityIssue{}
	handoff := false
	if !active {
		reviewCollectRunIssues(codeRuns, api.ReviewActivityIssueKindCode, &findings, &issues)
		reviewCollectRunIssues(visualRuns, api.ReviewActivityIssueKindVisual, &findings, &issues)
		// 账本兜底（保守，不猜具体原因）：结论被同步丢失/沿用等场景的兜底。
		hasKindIssue := func(kind api.ReviewActivityIssueKind) bool {
			for _, it := range issues {
				if it.Kind == kind {
					return true
				}
			}
			return false
		}
		codeFindings := findings.P0 + findings.P1 + findings.P2
		visualFindings := findings.Blocker + findings.WorthFixing
		if pr.CodeStatus == "failure" && codeFindings == 0 && !hasKindIssue(api.ReviewActivityIssueKindCode) {
			issues = append(issues, api.ReviewActivityIssue{
				Kind: api.ReviewActivityIssueKindCode, Reason: reviewIssueReasonCodeFailure,
				Message: "代码审查结论为发现问题，请查看 GitHub",
			})
		}
		if pr.CodeStatus == "error" && !hasKindIssue(api.ReviewActivityIssueKindCode) {
			issues = append(issues, api.ReviewActivityIssue{
				Kind: api.ReviewActivityIssueKindCode, Reason: reviewIssueReasonNotFinished,
				Message: "代码审查未完成",
			})
		}
		if pr.VisualStatus == "failure" && visualFindings == 0 && !hasKindIssue(api.ReviewActivityIssueKindVisual) {
			issues = append(issues, api.ReviewActivityIssue{
				Kind: api.ReviewActivityIssueKindVisual, Reason: reviewIssueReasonVisualFailure,
				Message: "视觉结论未通过（可能是截图不可用，或上一轮意见尚未处理）",
			})
		}
		if pr.VisualStatus == "error" && !hasKindIssue(api.ReviewActivityIssueKindVisual) {
			issues = append(issues, api.ReviewActivityIssue{
				Kind: api.ReviewActivityIssueKindVisual, Reason: reviewIssueReasonNotFinished,
				Message: "视觉审查未完成",
			})
		}

		if codeFindings+visualFindings > 0 || len(issues) > 0 {
			group = api.ReviewPullRequestActivityGroupNeedsAction
		} else {
			// 通过：至少有一条 completed 的代码运行（全 cancelled 的轮次不算
			// 通过，也不进首页）；夹着 cancelled 的干净轮次同样不返回（另一个
			// 组合没有结论）。近 7 天按当前轮 runs 的最晚 finished_at（缺失回退
			// 账本 updated_at）；更早的不返回。
			if reviewCompletedCount(codeRuns) == 0 || anyCancelled {
				return api.ReviewPullRequestActivity{}, time.Time{}, false
			}
			passedAt := pr.UpdatedAt
			if latestFinished != nil {
				passedAt = *latestFinished
			}
			if passedAt.Before(now.Add(-reviewActivityPassedWindow)) {
				return api.ReviewPullRequestActivity{}, time.Time{}, false
			}
			// 视觉衔接窗口：本应判通过，但视觉是否启动还没落定（sync_pending），
			// 先留在进行中，避免列表停掉轮询后看不到随后创建的视觉运行。同步
			// 清掉 pending 后仍无视觉运行，才是真的「未启动视觉」。
			if visualComboCount > 0 && len(visualRuns) == 0 && pr.SyncPending {
				group = api.ReviewPullRequestActivityGroupInProgress
				stage = api.Ptr(api.ReviewPullRequestActivityStageReviewing)
				handoff = true
			} else {
				group = api.ReviewPullRequestActivityGroupRecentlyPassed
			}
		}
	}

	// 进度分母只看仍有效的运行：cancelled 组合不会完成，计进 total 会让
	// 「N/M 完成」永远到不了 M。
	codeForStage := reviewNonCancelled(codeRuns)
	visualForStage := reviewNonCancelled(visualRuns)
	codeStage := api.ReviewStageProgress{
		Kind:      api.ReviewStageProgressKindCode,
		Total:     len(codeForStage),
		Completed: reviewCompletedCount(codeForStage),
		Status:    reviewStageStatus(codeForStage),
	}
	stages := []api.ReviewStageProgress{codeStage}
	if visualStage, ok := reviewVisualStage(visualForStage, visualComboCount, &codeStage, pr, &findings, handoff); ok {
		stages = append(stages, visualStage)
	}

	item := api.ReviewPullRequestActivity{
		ProjectId:    pr.ProjectID,
		ProjectName:  projectName,
		RepoFullName: pr.RepoFullName,
		PrNumber:     pr.PRNumber,
		PrTitle:      meta.PRTitle,
		PrUrl:        meta.PRURL,
		Group:        group,
		Stage:        stage,
		RoundNumber:  meta.RoundNumber,
		HeadSha:      meta.HeadSHA,
		UpdatedAt:    pr.UpdatedAt.UTC().Format(time.RFC3339Nano),
		StartedAt:    startedAt.UTC().Format(time.RFC3339Nano),
		Stages:       stages,
		Findings:     findings,
		Issues:       issues,
		CanRerun:     reviewActivityCanRerun(settings, pr),
	}
	if item.PrUrl == "" {
		item.PrUrl = links.PullURL(pr.RepoFullName, pr.PRNumber)
	}
	sortAt := pr.UpdatedAt
	if group == api.ReviewPullRequestActivityGroupRecentlyPassed && latestFinished != nil {
		sortAt = *latestFinished
	}
	return item, sortAt, true
}

// reviewActivityCanRerun 设置行 enabled 且账本属于当前审查仓库（契约第 6 步）。
func reviewActivityCanRerun(settings model.ProjectReviewSettings, pr *model.ReviewPullRequest) bool {
	return settings.Enabled && settings.RepoFullName != "" &&
		strings.EqualFold(pr.RepoFullName, settings.RepoFullName)
}

// reviewCollectRunIssues 遍历当前轮同段运行：completed 的解析 result_json 计
// 发现计数（解析失败加「结论无法解析」issue）；failed/timed_out 生成待处理
// issue；cancelled 是系统动作，一律忽略。
func reviewCollectRunIssues(runs []model.ReviewRun, kind api.ReviewActivityIssueKind,
	findings *api.ReviewFindingCounts, issues *[]api.ReviewActivityIssue) {
	for i := range runs {
		r := &runs[i]
		switch r.Status {
		case model.ReviewRunCompleted:
			if !reviewCountResult(r.Kind, r.ResultJSON, findings) {
				*issues = append(*issues, api.ReviewActivityIssue{
					Kind: kind, Reason: reviewIssueReasonUnparsed,
					Message: "审查结论无法解析，请查看 GitHub",
				})
			}
		case model.ReviewRunFailed, model.ReviewRunTimedOut:
			*issues = append(*issues, reviewRunIssue(r, kind))
		}
	}
}

// reviewCountResult 把一条结论计入发现计数（代码计 P0/P1/P2，视觉计
// blocker/worth_fixing）；result_json 无法解析返回 false。视觉分支把 prior
// 里 status=open 且按 priorOpenBlocks 阻塞的条目一并计入——与 GitHub status
// 的红绿判定（visualFailureCount）同一口径，否则 Web 侧「需要处理」会漏掉
// 模型只写进 prior(open) 的未解决项。
func reviewCountResult(kind model.ReviewKind, raw string, findings *api.ReviewFindingCounts) bool {
	if kind == model.ReviewKindVisual {
		var result api.ReviewVisualResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return false
		}
		for _, f := range result.Findings {
			switch f.Severity {
			case api.Blocker:
				findings.Blocker++
			case api.WorthFixing:
				findings.WorthFixing++
			}
		}
		for _, p := range result.Prior {
			if p.Status != api.ReviewPriorDecisionStatusOpen || !priorOpenBlocks(p.Level) {
				continue
			}
			// 计数桶只有两格：blocker 归 Blocker，其余阻塞级别（含 nil 兜底与
			// P0/P1 等未知形态）都归 WorthFixing——阻塞的必须有位置可落。
			if p.Level != nil && *p.Level == string(api.Blocker) {
				findings.Blocker++
			} else {
				findings.WorthFixing++
			}
		}
		return true
	}
	var result api.ReviewCodeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return false
	}
	for _, f := range result.Findings {
		switch f.Level {
		case api.P0:
			findings.P0++
		case api.P1:
			findings.P1++
		case api.P2:
			findings.P2++
		}
	}
	return true
}

// reviewRunIssue 失败/超时运行的待处理 issue：短原因按 error_code 归类
// （dispatch_failed→派发失败、timeout→超时、其它→审查失败）。
func reviewRunIssue(r *model.ReviewRun, kind api.ReviewActivityIssueKind) api.ReviewActivityIssue {
	reason := reviewIssueReasonRunFailed
	switch r.ErrorCode {
	case "dispatch_failed":
		reason = reviewIssueReasonDispatchFailed
	case "timeout":
		reason = reviewIssueReasonTimeout
	}
	message := r.ErrorMessage
	if message == "" {
		message = reason
	}
	combination := r.Model + " · " + r.InstanceID
	return api.ReviewActivityIssue{
		Kind: kind, Combination: &combination, Reason: reason, Message: message,
	}
}

// reviewNonCancelled 过滤掉系统取消的运行（不计入进度分母）。
func reviewNonCancelled(runs []model.ReviewRun) []model.ReviewRun {
	out := make([]model.ReviewRun, 0, len(runs))
	for _, r := range runs {
		if r.Status != model.ReviewRunCancelled {
			out = append(out, r)
		}
	}
	return out
}

// reviewStageStatus 一段运行的聚合状态：任一 dispatched/running → running；
// 全部 queued → queued；有终态又有排队 → running（这一段已经开始）；空 →
// pending；任一 failed/timed_out → failed；全部 completed → completed。
func reviewStageStatus(runs []model.ReviewRun) api.ReviewStageProgressStatus {
	hasActive, hasQueued, hasTerminal := false, false, false
	for _, r := range runs {
		switch r.Status {
		case model.ReviewRunDispatched, model.ReviewRunRunning:
			hasActive = true
		case model.ReviewRunQueued:
			hasQueued = true
		default:
			hasTerminal = true
		}
	}
	if hasActive {
		return api.ReviewStageProgressStatusRunning
	}
	if hasQueued {
		if hasTerminal {
			return api.ReviewStageProgressStatusRunning
		}
		return api.ReviewStageProgressStatusQueued
	}
	if len(runs) == 0 {
		return api.ReviewStageProgressStatusPending
	}
	for _, r := range runs {
		if r.Status == model.ReviewRunFailed || r.Status == model.ReviewRunTimedOut {
			return api.ReviewStageProgressStatusFailed
		}
	}
	return api.ReviewStageProgressStatusCompleted
}

// reviewCompletedCount 一段运行里 completed 的数量。
func reviewCompletedCount(runs []model.ReviewRun) int {
	n := 0
	for _, r := range runs {
		if r.Status == model.ReviewRunCompleted {
			n++
		}
	}
	return n
}

// reviewVisualStage 视觉段进度：有运行照代码段聚合（即使用户后来清空视觉
// 组合也展示）；无运行且配置为 0 不返回该段；配置>0 时按代码段状态与账本
// 结论推一个「未启动」段。
func reviewVisualStage(visualRuns []model.ReviewRun, configured int,
	codeStage *api.ReviewStageProgress, pr *model.ReviewPullRequest, findings *api.ReviewFindingCounts,
	handoff bool,
) (api.ReviewStageProgress, bool) {
	if len(visualRuns) > 0 {
		return api.ReviewStageProgress{
			Kind:      api.ReviewStageProgressKindVisual,
			Total:     len(visualRuns),
			Completed: reviewCompletedCount(visualRuns),
			Status:    reviewStageStatus(visualRuns),
		}, true
	}
	if configured == 0 {
		return api.ReviewStageProgress{}, false
	}
	stage := api.ReviewStageProgress{Kind: api.ReviewStageProgressKindVisual, Total: configured}
	if handoff {
		// 代码已通过、视觉衔接尚未决策：前端文案「等待视觉审查衔接」。
		stage.Status = api.ReviewStageProgressStatusPending
		return stage, true
	}
	switch {
	case codeStage.Status == api.ReviewStageProgressStatusQueued ||
		codeStage.Status == api.ReviewStageProgressStatusRunning ||
		codeStage.Status == api.ReviewStageProgressStatusPending:
		// 前端文案「等待代码审查」。
		stage.Status = api.ReviewStageProgressStatusPending
	case codeStage.Status == api.ReviewStageProgressStatusFailed ||
		(codeStage.Status == api.ReviewStageProgressStatusCompleted &&
			findings.P0+findings.P1+findings.P2 > 0):
		// 前端文案「未启动」。
		stage.Status = api.ReviewStageProgressStatusSkipped
	case pr.VisualStatus == "pending":
		stage.Status = api.ReviewStageProgressStatusPending
	case pr.VisualStatus == "failure" || pr.VisualStatus == "error":
		stage.Status = api.ReviewStageProgressStatusFailed
	default:
		// 无 UI 改动 / 截图未变化 / 沿用上一轮。
		stage.Status = api.ReviewStageProgressStatusSkipped
	}
	return stage, true
}

// GetPullRequestRecord 跨项目 PR 审查详情：项目必须属于当前用户（40401）；
// 项目有设置行且按其仓库匹配的账本行存在，否则 40434。不要求 enabled
// （停用项目仍可看历史详情）。
func (s *ReviewService) GetPullRequestRecord(ctx context.Context, userID, projectID string, prNumber int) (*api.ReviewPullRequestRecord, error) {
	if _, err := s.ownedProject(userID, projectID); err != nil {
		return nil, err
	}
	var settings model.ProjectReviewSettings
	err := s.db.WithContext(ctx).Where("project_id = ?", projectID).First(&settings).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errs.ErrReviewPullRequestNotFound
	case err != nil:
		return nil, errs.ErrInternal
	}
	if settings.RepoFullName == "" {
		return nil, errs.ErrReviewPullRequestNotFound
	}
	var pr model.ReviewPullRequest
	err = s.db.WithContext(ctx).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			projectID, settings.RepoFullName, prNumber).
		First(&pr).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errs.ErrReviewPullRequestNotFound
	case err != nil:
		return nil, errs.ErrInternal
	}

	var runs []model.ReviewRun
	if err := s.db.WithContext(ctx).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			projectID, pr.RepoFullName, pr.PRNumber).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, errs.ErrInternal
	}
	roundMap := map[int][]model.ReviewRun{}
	for i := range runs {
		r := &runs[i]
		roundMap[r.RoundNumber] = append(roundMap[r.RoundNumber], *r)
	}
	envNames := s.reviewRunEnvNames(ctx, runs)
	// 历史运行记录同按 GitHub 身份兜底（与列表路径同口径，见上方注释）。
	rec := buildReviewPRRecord(&pr, roundMap, envNames, settings.RepoFullName,
		githubLinks(githubWebBase(s.cfg)))
	return &rec, nil
}
