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

// PR 审查记录查询（INT-135 1b）：只读，不做任何 GitHub/T3 调用。
// 组织形态：PR（review_pull_requests）→ 轮次（round_number 聚合 review_runs）
// → code_runs / visual_runs。

const (
	reviewRecordsDefaultLimit = 20
	reviewRecordsMaxLimit     = 100
)

// ListReviewProjects 当前用户所有已关联仓库（公开或代码仓库任一）的项目审查
// 总览：开关与组合数取设置行，最近状态取最新 review_pull_requests 行；
// 同仓库被其他项目启用时 taken_by 非空且 can_manage=false。仓库字段展示
// 实际审查的代码仓库（未单独配置即公开仓库）。
func (s *ReviewService) ListReviewProjects(ctx context.Context, userID string) ([]api.ReviewProjectSummary, error) {
	var projects []model.Project
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND ((github_owner <> '' AND github_repo <> '') OR (code_github_owner <> '' AND code_github_repo <> ''))", userID).
		Order("created_at ASC").
		Find(&projects).Error; err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]api.ReviewProjectSummary, 0, len(projects))
	if len(projects) == 0 {
		return out, nil
	}

	projectIDs := make([]string, 0, len(projects))
	for _, p := range projects {
		projectIDs = append(projectIDs, p.ID)
	}

	// 设置行：enabled 与组合数。
	var settings []model.ProjectReviewSettings
	if err := s.db.WithContext(ctx).Where("project_id IN ?", projectIDs).Find(&settings).Error; err != nil {
		return nil, errs.ErrInternal
	}
	settingsByProject := map[string]model.ProjectReviewSettings{}
	for _, row := range settings {
		settingsByProject[row.ProjectID] = row
	}

	// 最新一条 PR 记录（updated_at 最大）给状态字段。按设置行的
	// repo_full_name（审查仓库的判定键，与 webhook 反查/同步过滤同口径）挑账本：
	// 项目换过仓库后，旧仓库的历史记录不能当成当前仓库的最近状态展示。
	// 没有设置行的项目不可能有账本（开轮要求设置行存在），直接跳过。
	repoByProject := map[string]string{}
	repoSet := map[string]struct{}{}
	for i := range projects {
		p := &projects[i]
		row, ok := settingsByProject[p.ID]
		if !ok || row.RepoFullName == "" {
			continue
		}
		repoByProject[p.ID] = row.RepoFullName
		repoSet[row.RepoFullName] = struct{}{}
	}
	latestByProject := map[string]model.ReviewPullRequest{}
	if len(repoSet) > 0 {
		repos := make([]string, 0, len(repoSet))
		for repo := range repoSet {
			repos = append(repos, repo)
		}
		var prs []model.ReviewPullRequest
		if err := s.db.WithContext(ctx).
			Where("project_id IN ? AND repo_full_name IN ?", projectIDs, repos).
			Order("updated_at DESC").
			Find(&prs).Error; err != nil {
			return nil, errs.ErrInternal
		}
		for _, pr := range prs {
			if repoByProject[pr.ProjectID] != pr.RepoFullName {
				continue
			}
			if _, ok := latestByProject[pr.ProjectID]; !ok {
				latestByProject[pr.ProjectID] = pr
			}
		}
	}
	// 同仓库占用：别的项目 enabled 且审查仓库（小写）相同 → 占用者登录名。
	occupierByRepo, err := s.reviewOccupiers(ctx, projects)
	if err != nil {
		return nil, err
	}

	for i := range projects {
		p := &projects[i]
		owner, repo := p.CodeRepo()
		item := api.ReviewProjectSummary{
			ProjectId:   p.ID,
			ProjectName: p.Name,
			GithubOwner: owner,
			GithubRepo:  repo,
			CanManage:   true,
		}
		if row, ok := settingsByProject[p.ID]; ok {
			item.Enabled = row.Enabled
			if combos, err := parseCombinations(row.CodeCombinationsJSON); err == nil {
				item.CodeCombinationCount = len(combos)
			}
			if combos, err := parseCombinations(row.VisualCombinationsJSON); err == nil {
				item.VisualCombinationCount = len(combos)
			}
		}
		if pr, ok := latestByProject[p.ID]; ok {
			if pr.CodeStatus != "" {
				item.CodeStatus = api.Ptr(pr.CodeStatus)
			}
			if pr.VisualStatus != "" {
				item.VisualStatus = api.Ptr(pr.VisualStatus)
			}
			item.LastReviewAt = api.Ptr(pr.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		repoKey := reviewRepoKey(owner, repo)
		if login, taken := occupierByRepo[repoKey][p.ID]; taken {
			item.TakenBy = api.Ptr(login)
			item.CanManage = false
		}
		out = append(out, item)
	}
	return out, nil
}

// reviewOccupiers 返回 repoKey → (占用 projectID → 登录名) 的映射：
// 对给定项目集合，查同仓库 enabled 的其他项目。占用判定只读设置行的
// repo_full_name 快照（enabled 行的唯一真相），联 users 取登录名。
// 键外层是 repo，内层项目 id 只用于排除「自己占自己」。
func (s *ReviewService) reviewOccupiers(ctx context.Context, projects []model.Project) (map[string]map[string]string, error) {
	type row struct {
		ProjectID    string
		RepoFullName string
		Login        string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("project_review_settings AS s").
		Joins("JOIN users u ON u.id = s.user_id").
		Where("s.enabled").
		Select("s.project_id, s.repo_full_name, u.username AS login").
		Scan(&rows).Error; err != nil {
		return nil, errs.ErrInternal
	}

	out := map[string]map[string]string{}
	for _, p := range projects {
		owner, repo := p.CodeRepo()
		key := reviewRepoKey(owner, repo)
		for _, r := range rows {
			if r.ProjectID == p.ID {
				continue
			}
			if r.RepoFullName != key {
				continue
			}
			if out[key] == nil {
				out[key] = map[string]string{}
			}
			out[key][p.ID] = r.Login
		}
	}
	return out, nil
}

func reviewRepoKey(owner, repo string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

// ListReviewRecords 项目审查记录：PR 按 updated_at DESC，每 PR 内嵌轮次
// （round_number DESC），轮内 code_runs/visual_runs 按创建时间正序。
func (s *ReviewService) ListReviewRecords(ctx context.Context, userID, projectID string, limit int) ([]api.ReviewPullRequestRecord, error) {
	if _, err := s.ownedProject(userID, projectID); err != nil {
		return nil, err
	}
	// can_rerun 的判定键是设置行的 repo_full_name（审查仓库，与 rerun 接口
	// 同口径），不按 enabled 过滤——停用只影响开轮授权，不影响记录能否重跑
	// （接口会返回 40923）。没有设置行的项目不可能有账本。
	currentRepo := ""
	var settings model.ProjectReviewSettings
	switch err := s.db.WithContext(ctx).Where("project_id = ?", projectID).First(&settings).Error; {
	case err == nil:
		currentRepo = settings.RepoFullName
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errs.ErrInternal
	}
	if limit <= 0 {
		limit = reviewRecordsDefaultLimit
	}
	if limit > reviewRecordsMaxLimit {
		limit = reviewRecordsMaxLimit
	}

	var prs []model.ReviewPullRequest
	if err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&prs).Error; err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]api.ReviewPullRequestRecord, 0, len(prs))
	if len(prs) == 0 {
		return out, nil
	}

	// 运行按 (repo, pr) 精确匹配：项目换过仓库时，A 与 B 可能有同号 PR，
	// 只按 pr_number 取运行会把两个仓库的轮次/线程混进同一条记录。
	keys := make([]string, 0, len(prs))
	for _, pr := range prs {
		keys = append(keys, pr.RepoFullName+"#"+strconv.Itoa(pr.PRNumber))
	}
	var runs []model.ReviewRun
	if err := s.db.WithContext(ctx).
		Where("project_id = ? AND (repo_full_name || '#' || pr_number) IN ?", projectID, keys).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, errs.ErrInternal
	}

	// 批量补 environment_name。
	envNames := s.reviewRunEnvNames(ctx, runs)
	// PR 链接兜底按项目的代码仓集成构造（INT-166）。
	links := forgeLinksForProject(ctx, s.db, projectID)

	// (repo, pr) → rounds。
	type prKey struct {
		Repo string
		PR   int
	}
	roundsByPR := map[prKey]map[int][]model.ReviewRun{}
	for i := range runs {
		r := &runs[i]
		key := prKey{r.RepoFullName, r.PRNumber}
		if roundsByPR[key] == nil {
			roundsByPR[key] = map[int][]model.ReviewRun{}
		}
		roundsByPR[key][r.RoundNumber] = append(roundsByPR[key][r.RoundNumber], *r)
	}

	for _, pr := range prs {
		key := prKey{pr.RepoFullName, pr.PRNumber}
		out = append(out, buildReviewPRRecord(&pr, roundsByPR[key], envNames, currentRepo, links))
	}
	return out, nil
}

// buildReviewPRRecord 单条 PR 记录投影：PR 元信息 + 轮次列表（round_number
// DESC）。records 列表与跨项目详情共用；currentRepo 是设置行的审查仓库
// 快照（can_rerun 判定键），为空表示项目没有设置行。links 按项目代码仓
// 集成解析，PR 标题/URL 快照缺失时兜底构造 PR 链接。
func buildReviewPRRecord(pr *model.ReviewPullRequest, roundMap map[int][]model.ReviewRun,
	envNames map[string]string, currentRepo string, links forgeLinks) api.ReviewPullRequestRecord {
	rec := api.ReviewPullRequestRecord{
		PrNumber:     pr.PRNumber,
		RepoFullName: pr.RepoFullName,
		HeadSha:      pr.HeadSHA,
		UpdatedAt:    pr.UpdatedAt.UTC().Format(time.RFC3339Nano),
		// 项目改过仓库后，旧仓库的记录只能查看：rerun 会按当前审查仓库
		// 取 PR，同号的无关 PR 会被误审。
		CanRerun: currentRepo != "" && strings.EqualFold(pr.RepoFullName, currentRepo),
		Rounds:   []api.ReviewRoundRecord{},
	}
	if pr.CodeStatus != "" {
		rec.CodeStatus = api.Ptr(pr.CodeStatus)
	}
	if pr.VisualStatus != "" {
		rec.VisualStatus = api.Ptr(pr.VisualStatus)
	}
	// 最近一次运行带 PR 标题/URL 快照。
	var meta *model.ReviewRun
	for i := range roundMap {
		for j := range roundMap[i] {
			r := &roundMap[i][j]
			if meta == nil || r.CreatedAt.After(meta.CreatedAt) {
				meta = r
			}
		}
	}
	if meta != nil {
		rec.PrTitle = meta.PRTitle
		rec.PrUrl = meta.PRURL
	}
	if rec.PrUrl == "" {
		rec.PrUrl = links.PullURL(pr.RepoFullName, pr.PRNumber)
	}

	numbers := make([]int, 0, len(roundMap))
	for n := range roundMap {
		numbers = append(numbers, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))
	for _, n := range numbers {
		rec.Rounds = append(rec.Rounds, buildRoundRecord(n, roundMap[n], envNames))
	}
	return rec
}

// buildRoundRecord 一轮内的运行分 code/visual 两组输出（入参已按创建时间升序）；
// trigger_action 取该轮第一条代码 run，没有代码 run 或为空时回退 unknown。
func buildRoundRecord(roundNumber int, runs []model.ReviewRun, envNames map[string]string) api.ReviewRoundRecord {
	round := api.ReviewRoundRecord{
		RoundNumber:   roundNumber,
		CreatedAt:     runs[0].CreatedAt.UTC().Format(time.RFC3339Nano),
		HeadSha:       runs[0].HeadSHA,
		TriggerAction: api.ReviewRoundRecordTriggerActionUnknown,
		CodeRuns:      []api.ReviewRunSummary{},
		VisualRuns:    []api.ReviewRunSummary{},
	}
	for i := range runs {
		r := &runs[i]
		sum := runSummary(r, envNames)
		if r.Kind == model.ReviewKindVisual {
			round.VisualRuns = append(round.VisualRuns, sum)
			continue
		}
		if round.TriggerAction == api.ReviewRoundRecordTriggerActionUnknown && r.TriggerAction != "" {
			round.TriggerAction = api.ReviewRoundRecordTriggerAction(r.TriggerAction)
		}
		round.CodeRuns = append(round.CodeRuns, sum)
	}
	return round
}

// runSummary 一次运行的 API 投影：findings_count 从 result_json 解析
// （代码计 P0/P1/P2，视觉计 blocker/worth_fixing）；ui_changed 仅代码结论有值；
// findings/unmeasurable 是完整发现列表（结论可解析时才有）。视觉分支把
// prior(open) 一并展示——展示宽口径（全部 open 条目，含 minor/未分级），计
// 数窄口径（只把阻塞级别算进 findings_count，与 GitHub status / Web 计数同
// 一把尺）；否则 PR 详情抽屉对「本轮只有 minor、上轮意见只标 prior(open)」
// 的轮次显示 0 条，而状态和首页都是失败，应用内看起来像通过。
func runSummary(r *model.ReviewRun, envNames map[string]string) api.ReviewRunSummary {
	sum := api.ReviewRunSummary{
		Id:            r.ID,
		Kind:          api.ReviewRunSummaryKind(r.Kind),
		EnvironmentId: r.EnvironmentID,
		InstanceId:    r.InstanceID,
		Model:         r.Model,
		Status:        api.ReviewRunSummaryStatus(r.Status),
		ThreadId:      r.ThreadID,
	}
	if name, ok := envNames[r.EnvironmentID]; ok {
		sum.EnvironmentName = api.Ptr(name)
	}
	if r.ErrorCode != "" {
		sum.ErrorCode = api.Ptr(r.ErrorCode)
	}
	if r.ErrorMessage != "" {
		sum.ErrorMessage = api.Ptr(r.ErrorMessage)
	}
	if r.Summary != "" {
		sum.Summary = api.Ptr(r.Summary)
	}
	if r.WorktreeState != "" && r.WorktreeState != model.ReviewWorktreeNone {
		sum.WorktreeState = api.Ptr(string(r.WorktreeState))
	}
	if len(r.ModelOptionsJSON) > 0 {
		var opts []api.ReviewModelOption
		if json.Unmarshal([]byte(r.ModelOptionsJSON), &opts) == nil && len(opts) > 0 {
			sum.Options = opts
		}
	}
	if r.StartedAt != nil {
		sum.StartedAt = api.Ptr(r.StartedAt.UTC().Format(time.RFC3339Nano))
	}
	if r.FinishedAt != nil {
		sum.FinishedAt = api.Ptr(r.FinishedAt.UTC().Format(time.RFC3339Nano))
	}
	if strings.TrimSpace(r.ResultJSON) == "" {
		return sum
	}
	if r.Kind == model.ReviewKindVisual {
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			return sum
		}
		count := 0
		for _, f := range result.Findings {
			if f.Severity == api.Blocker || f.Severity == api.WorthFixing {
				count++
			}
			sum.Findings = append(sum.Findings, reviewFindingFromVisual(&f))
		}
		// prior(open) 追加进展示列表（展示宽口径）：正文加「上轮遗留：」前缀，
		// 抽屉里能一眼区分遗留与本轮新发现；截图/维度/改法是本轮发现的字段，
		// 遗留条目没有对应本轮截图，留空。计数按阻塞口径（priorOpenBlocks），
		// minor 的遗留只陈列不计数——与状态判定的红绿口径一致。level 空时展示
		// 兜底为 worth_fixing：Level 字段非可空，留空会渲染残缺行。
		for _, p := range result.Prior {
			if p.Status != api.ReviewPriorDecisionStatusOpen {
				continue
			}
			if priorOpenBlocks(p.Level) {
				count++
			}
			level := "worth_fixing"
			if p.Level != nil && strings.TrimSpace(*p.Level) != "" {
				level = *p.Level
			}
			sum.Findings = append(sum.Findings, api.ReviewFinding{
				Level: level,
				Body:  "上轮遗留：" + p.Body,
			})
		}
		sum.FindingsCount = api.Ptr(count)
		sum.Unmeasurable = result.Unmeasurable
		return sum
	}
	var result api.ReviewCodeResult
	if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
		return sum
	}
	count := 0
	for _, f := range result.Findings {
		if f.Level == api.P0 || f.Level == api.P1 || f.Level == api.P2 {
			count++
		}
		sum.Findings = append(sum.Findings, reviewFindingFromCode(&f))
	}
	sum.FindingsCount = api.Ptr(count)
	sum.UiChanged = api.Ptr(result.UiChanged)
	return sum
}

// reviewFindingFromCode 代码发现 → 统一展示投影（级别原样，含 Note）。
func reviewFindingFromCode(f *api.ReviewCodeFinding) api.ReviewFinding {
	out := api.ReviewFinding{Level: string(f.Level), Body: f.Body, Line: f.Line}
	if f.File != "" {
		out.File = api.Ptr(f.File)
	}
	return out
}

// reviewFindingFromVisual 视觉发现 → 统一展示投影（severity → level）。
func reviewFindingFromVisual(f *api.ReviewVisualFinding) api.ReviewFinding {
	out := api.ReviewFinding{Level: string(f.Severity), Body: f.Body}
	out.Screenshot = api.Ptr(f.Screenshot)
	out.Dimension = api.Ptr(string(f.Dimension))
	if f.Fix != "" {
		out.Fix = api.Ptr(f.Fix)
	}
	return out
}

// reviewRunEnvNames 批量查运行涉及的环境名。
func (s *ReviewService) reviewRunEnvNames(ctx context.Context, runs []model.ReviewRun) map[string]string {
	ids := map[string]struct{}{}
	for _, r := range runs {
		if r.EnvironmentID != "" {
			ids[r.EnvironmentID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	var envs []model.T3Environment
	if err := s.db.WithContext(ctx).Where("id IN ?", idList).Find(&envs).Error; err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range envs {
		out[e.ID] = e.Name
	}
	return out
}
