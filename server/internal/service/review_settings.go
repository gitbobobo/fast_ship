package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PR 审查设置（INT-135 1b）：项目级开关 + 组合配置。启用时全量校验前置条件
// （部署 App、安装、public_url、Token、环境绑定）与同仓库唯一占用；
// t3_project_id 保存时从 project_t3_bindings 快照，不信任客户端回传值。

const (
	reviewMaxCodeCombinations   = 5
	reviewMaxVisualCombinations = 3
)

// reviewComboError 组合结构/归属/绑定校验失败一律 40001（参数类错误，
// 不是 41202 前置条件缺项——前置条件指部署与项目级资源是否就绪）。
func reviewComboError(format string, args ...any) error {
	return errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf(format, args...))
}

// persistedCombination 落库的组合 JSON 元素：与 api.ReviewCombination 同形，
// 但 environment_name 是响应专用字段、不落库。
type persistedCombination struct {
	EnvironmentID string                  `json:"environment_id"`
	InstanceID    string                  `json:"instance_id"`
	Model         string                  `json:"model"`
	Options       []api.ReviewModelOption `json:"options,omitempty"`
	T3ProjectID   string                  `json:"t3_project_id,omitempty"`
}

// GetSettings 读取项目审查设置（项目必须属于该用户，否则 40401）；
// prerequisites 实时计算（github_app_installation 会调一次 GitHub），
// taken_by 是同仓库已被其他项目开启时的占用者登录名。
func (s *ReviewService) GetSettings(ctx context.Context, userID, projectID string) (*api.ReviewSettings, error) {
	project, err := s.ownedProject(userID, projectID)
	if err != nil {
		return nil, err
	}

	var row model.ProjectReviewSettings
	err = s.db.WithContext(ctx).Where("project_id = ?", projectID).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.Error("读取审查设置失败", zap.Error(err))
		return nil, errs.ErrInternal
	}

	codeCombos, err := parseCombinations(row.CodeCombinationsJSON)
	if err != nil {
		return nil, errs.ErrInternal
	}
	visualCombos, err := parseCombinations(row.VisualCombinationsJSON)
	if err != nil {
		return nil, errs.ErrInternal
	}
	s.fillEnvironmentNames(ctx, userID, codeCombos, visualCombos)

	prereqs, err := s.reviewPrerequisites(ctx, project, codeCombos, visualCombos)
	if err != nil {
		return nil, err
	}
	takenBy, err := s.reviewTakenBy(ctx, project)
	if err != nil {
		return nil, err
	}

	// 未保存过的行采用模型默认触发项（opened/synchronize 开）。
	out := &api.ReviewSettings{
		ProjectId:             projectID,
		RepoFullName:          project.CodeRepoFullName(),
		Enabled:               false,
		TriggerOpened:         true,
		TriggerSynchronize:    true,
		TriggerReadyForReview: false,
		TriggerReopened:       false,
		CodeCombinations:      codeCombos,
		VisualCombinations:    visualCombos,
		Prerequisites:         prereqs,
	}
	if row.ProjectID != "" {
		out.Enabled = row.Enabled
		out.TriggerOpened = row.TriggerOpened
		out.TriggerSynchronize = row.TriggerSynchronize
		out.TriggerReadyForReview = row.TriggerReadyForReview
		out.TriggerReopened = row.TriggerReopened
	}
	if takenBy != "" {
		out.TakenBy = api.Ptr(takenBy)
	}
	out.CanManage = api.Ptr(takenBy == "")
	return out, nil
}

// UpdateSettings 全量保存审查设置。enabled=false 允许零组合、跳过前置条件
// 校验（仍做组合字段结构与 t3_project_id 快照的净化）；enabled=true 依次
// 校验：组合数量与重复/环境归属/环境绑定（40001）、部署前置条件（41202）、
// 同仓库唯一（40922）。保存后读回保证与 GET 一致。
func (s *ReviewService) UpdateSettings(ctx context.Context, userID, projectID string, input *api.ReviewSettingsInput) (*api.ReviewSettings, error) {
	project, err := s.ownedProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, reviewComboError("请求体不能为空")
	}

	codeCombos := input.CodeCombinations
	if codeCombos == nil {
		codeCombos = []api.ReviewCombination{}
	}
	visualCombos := input.VisualCombinations
	if visualCombos == nil {
		visualCombos = []api.ReviewCombination{}
	}

	if input.Enabled {
		if cOwner, cRepo := project.CodeRepo(); cOwner == "" || cRepo == "" {
			return nil, errs.ErrProjectGitHubNotConfigured
		}
		if n := len(codeCombos); n < 1 || n > reviewMaxCodeCombinations {
			return nil, reviewComboError("代码审查组合需 1-%d 个", reviewMaxCodeCombinations)
		}
		if len(visualCombos) > reviewMaxVisualCombinations {
			return nil, reviewComboError("视觉审查组合最多 %d 个", reviewMaxVisualCombinations)
		}
		if err := s.validateCombinations(ctx, userID, projectID, codeCombos); err != nil {
			return nil, err
		}
		if err := s.validateCombinations(ctx, userID, projectID, visualCombos); err != nil {
			return nil, err
		}
		if err := checkDuplicateCombinations(codeCombos); err != nil {
			return nil, err
		}
		if err := checkDuplicateCombinations(visualCombos); err != nil {
			return nil, err
		}

		// 前置条件缺项（41202）：缺项逐条写进 message。
		prereqs, err := s.reviewPrerequisites(ctx, project, codeCombos, visualCombos)
		if err != nil {
			return nil, err
		}
		var missing []string
		for _, p := range prereqs {
			if !p.Missing {
				continue
			}
			item := p.Label
			if p.Detail != nil && *p.Detail != "" {
				item += "（" + *p.Detail + "）"
			}
			missing = append(missing, item)
		}
		if len(missing) > 0 {
			return nil, errs.New(errs.ErrReviewPrerequisitesMissing.Code,
				"开启 PR 审查缺少前置条件："+strings.Join(missing, "；"))
		}

		// 同仓库唯一（40922）：其他项目已启用时拒绝并给出占用者登录名。
		if takenBy, err := s.reviewTakenBy(ctx, project); err != nil {
			return nil, err
		} else if takenBy != "" {
			return nil, errs.New(errs.ErrReviewRepoTaken.Code,
				fmt.Sprintf("该仓库已被用户 %s 的项目开启审查", takenBy))
		}
	}

	// 快照净化：t3_project_id 只取绑定表真值（客户端回传值一律覆盖）；
	// environment_name 是响应字段，不落库。
	s.snapshotT3ProjectIDs(ctx, projectID, codeCombos)
	s.snapshotT3ProjectIDs(ctx, projectID, visualCombos)

	codeJSON, err := marshalCombinations(codeCombos)
	if err != nil {
		return nil, errs.ErrInternal
	}
	visualJSON, err := marshalCombinations(visualCombos)
	if err != nil {
		return nil, errs.ErrInternal
	}

	now := time.Now().UTC()
	row := model.ProjectReviewSettings{
		ProjectID:              projectID,
		UserID:                 userID,
		RepoFullName:           strings.ToLower(project.CodeRepoFullName()),
		Enabled:                input.Enabled,
		TriggerOpened:          input.TriggerOpened,
		TriggerSynchronize:     input.TriggerSynchronize,
		TriggerReadyForReview:  input.TriggerReadyForReview,
		TriggerReopened:        input.TriggerReopened,
		CodeCombinationsJSON:   codeJSON,
		VisualCombinationsJSON: visualJSON,
		UpdatedAt:              now,
	}
	// upsert：主键 project_id，已存在整行覆盖。启用时在同一事务里再查一次
	// 同仓库占用（此前那次在事务外，两个并发保存可能都通过），并由部分唯一
	// 索引 idx_project_review_settings_repo 兜底。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 事务内复核项目仓库（代码仓库，未单独配置即公开仓库）：此前前置条件
		// 校验用的是事务外的项目快照，并发改仓库可能让 enabled 落到旧仓库上
		// （唯一占用与账本分区都按旧键写错）。事务时刻与快照不一致一律 400，
		// 让用户刷新重来。
		var fresh model.Project
		if err := tx.Select("id", "github_owner", "github_repo", "code_github_owner", "code_github_repo").
			Where("id = ?", projectID).First(&fresh).Error; err != nil {
			return err
		}
		if !strings.EqualFold(fresh.CodeRepoFullName(), project.CodeRepoFullName()) {
			return errs.New(errs.ErrInvalidParams.Code, "项目仓库已变更，请刷新后重试")
		}
		if row.Enabled {
			var otherID string
			err := tx.Model(&model.ProjectReviewSettings{}).
				Where("enabled = 1 AND repo_full_name = ? AND project_id <> ?", row.RepoFullName, projectID).
				Limit(1).Pluck("project_id", &otherID).Error
			if err != nil {
				return err
			}
			if otherID != "" {
				return errs.ErrReviewRepoTaken
			}
			// 接管收尾：唯一占用保证没有其他 enabled 项目，这里清的是曾
			// 开启过该仓库的其他项目留下的执行态——未终态运行置
			// cancelled/taken_over 并消费令牌、删防抖、清总账
			// sync_pending（不让旧项目继续发 GitHub 产出）。
			if err := s.takeoverRepoTx(tx, row.RepoFullName, projectID, now); err != nil {
				return err
			}
		}
		// 停用审查时把在途/排队的运行一并取消：调度按 status 挑选，留着
		// queued/dispatched 会在停用之后继续开线程。
		if !row.Enabled {
			if err := cancelProjectReviewRunsTx(tx, projectID, "disabled", now); err != nil {
				return err
			}
		}
		var existing model.ProjectReviewSettings
		findErr := tx.Where("project_id = ?", projectID).First(&existing).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			row.CreatedAt = now
			return tx.Create(&row).Error
		case findErr != nil:
			return findErr
		}
		row.CreatedAt = existing.CreatedAt
		return tx.Save(&row).Error
	})
	if err != nil {
		var appErr *errs.AppError
		switch {
		case errors.Is(err, errs.ErrReviewRepoTaken) || strings.Contains(err.Error(), "UNIQUE constraint failed"):
			return nil, errs.New(errs.ErrReviewRepoTaken.Code,
				"该仓库已被其他项目开启审查")
		case errors.Is(err, gorm.ErrRecordNotFound):
			return nil, errs.ErrProjectNotFound
		case errors.As(err, &appErr):
			return nil, appErr
		}
		s.logger.Error("保存审查设置失败", zap.Error(err))
		return nil, errs.ErrInternal
	}
	return s.GetSettings(ctx, userID, projectID)
}

// takeoverRepoTx 启用某仓库时的接管收尾：同仓库其他项目残留的未终态运行
// 全部 cancelled/taken_over 并消费令牌、删除防抖行、清掉总账 sync_pending。
// 调用前已由部分唯一索引语义保证没有其他 enabled 项目——残留行来自曾
// 开启后停用、或换过仓库的项目。
func (s *ReviewService) takeoverRepoTx(tx *gorm.DB, repo, projectID string, now time.Time) error {
	active := []model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}
	var runIDs []string
	if err := tx.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND project_id <> ? AND status IN ?", repo, projectID, active).
		Pluck("id", &runIDs).Error; err != nil {
		return err
	}
	if len(runIDs) > 0 {
		if err := tx.Model(&model.ReviewRun{}).
			Where("id IN ? AND status IN ?", runIDs, active).
			Updates(map[string]any{
				"status":      model.ReviewRunCancelled,
				"error_code":  "taken_over",
				"finished_at": now,
				"updated_at":  now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ReviewRunToken{}).
			Where("run_id IN ? AND consumed_at IS NULL", runIDs).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("repo_full_name = ? AND project_id <> ?", repo, projectID).
		Delete(&model.ReviewDebounce{}).Error; err != nil {
		return err
	}
	return tx.Model(&model.ReviewPullRequest{}).
		Where("repo_full_name = ? AND project_id <> ? AND sync_pending", repo, projectID).
		Updates(map[string]any{"sync_pending": false, "updated_at": now}).Error
}

// ownedProject 读项目并要求归属当前用户；不存在/不属于统一 40401。
func (s *ReviewService) ownedProject(userID, projectID string) (*model.Project, error) {
	project, err := s.projects.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return project, nil
}

// validateCombinations 校验一组组合的字段完整性、环境归属与项目绑定，
// 并把绑定的 t3_project_id 快照进组合（供落库）。
func (s *ReviewService) validateCombinations(ctx context.Context, userID, projectID string, combos []api.ReviewCombination) error {
	for i := range combos {
		c := &combos[i]
		if strings.TrimSpace(c.EnvironmentId) == "" || strings.TrimSpace(c.InstanceId) == "" || strings.TrimSpace(c.Model) == "" {
			return reviewComboError("审查组合缺少 environment_id、instance_id 或 model")
		}
		var env model.T3Environment
		err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", c.EnvironmentId, userID).First(&env).Error
		if err != nil {
			return reviewComboError("审查环境 %s 不存在或不属于当前用户", c.EnvironmentId)
		}
		// 只读会话（缺 orchestration:operate）无法建线程；保存时就拒绝，
		// 否则每次派发都会落 dispatch_failed。
		if !t3HasOperateScope(env.BearerScope) {
			return reviewComboError("审查环境 %s 是只读会话，重新配对后才能用于审查", env.Name)
		}
		var binding model.ProjectT3Binding
		err = s.db.WithContext(ctx).
			Where("project_id = ? AND environment_id = ?", projectID, c.EnvironmentId).
			First(&binding).Error
		if err != nil {
			return reviewComboError("环境 %s 未绑定该项目", env.Name)
		}
		c.T3ProjectId = api.Ptr(binding.T3ProjectID)
		c.EnvironmentName = nil
	}
	return nil
}

// checkDuplicateCombinations 按规范化键（environment_id+instance_id+model+
// options 的 canonical JSON）判重；nil 与空的 options 视为相同。
func checkDuplicateCombinations(combos []api.ReviewCombination) error {
	seen := map[string]struct{}{}
	for i := range combos {
		key := combinationKey(&combos[i])
		if _, dup := seen[key]; dup {
			return reviewComboError("审查组合重复：%s", combinationLabel(&combos[i]))
		}
		seen[key] = struct{}{}
	}
	return nil
}

func combinationKey(c *api.ReviewCombination) string {
	return c.EnvironmentId + "\x00" + c.InstanceId + "\x00" + c.Model + "\x00" + canonicalOptionsJSON(c.Options)
}

// canonicalOptionsJSON 把选项数组规范化成稳定 JSON：按 id 排序、去重（后者覆盖前者）。
// 选项顺序来自 UI 的 descriptor 顺序，不能作为身份的一部分。
func canonicalOptionsJSON(options []api.ReviewModelOption) string {
	if len(options) == 0 {
		return "null"
	}
	sorted := make([]api.ReviewModelOption, len(options))
	copy(sorted, options)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Id < sorted[j].Id })
	out := make([]api.ReviewModelOption, 0, len(sorted))
	for _, opt := range sorted {
		if n := len(out); n > 0 && out[n-1].Id == opt.Id {
			out[n-1] = opt
			continue
		}
		out = append(out, opt)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func combinationLabel(c *api.ReviewCombination) string {
	return fmt.Sprintf("%s/%s/%s", c.EnvironmentId, c.InstanceId, c.Model)
}

// snapshotT3ProjectIDs 非校验路径的快照净化（enabled=false 保存）：
// 绑得上的环境写 t3_project_id，绑不上清掉客户端回传值。
func (s *ReviewService) snapshotT3ProjectIDs(ctx context.Context, projectID string, combos []api.ReviewCombination) {
	for i := range combos {
		c := &combos[i]
		c.EnvironmentName = nil
		c.T3ProjectId = nil
		var binding model.ProjectT3Binding
		if err := s.db.WithContext(ctx).
			Where("project_id = ? AND environment_id = ?", projectID, c.EnvironmentId).
			First(&binding).Error; err == nil {
			c.T3ProjectId = api.Ptr(binding.T3ProjectID)
		}
	}
}

// reviewPrerequisites 计算五项前置条件；环境绑定按给定组合逐个判定。
// github_app_installation 在 App 已配置且项目有仓库时实时调一次 GitHub。
func (s *ReviewService) reviewPrerequisites(ctx context.Context, project *model.Project, codeCombos, visualCombos []api.ReviewCombination) ([]api.ReviewPrerequisite, error) {
	out := []api.ReviewPrerequisite{}
	add := func(code api.ReviewPrerequisiteCode, label string, missing bool, detail string) {
		p := api.ReviewPrerequisite{Code: code, Label: label, Missing: missing}
		if detail != "" {
			p.Detail = api.Ptr(detail)
		}
		out = append(out, p)
	}

	// 1. 部署 GitHub App 已配置：debug fake 视为已配置，否则问部署集成。
	appConfigured := s.cfg != nil && s.cfg.GitHubApp.Fake && s.cfg.Server.Mode == "debug"
	if !appConfigured && s.appSource != nil {
		_, ok, err := s.appSource.Current(ctx)
		if err != nil {
			return nil, err
		}
		appConfigured = ok
	}
	appDetail := ""
	if !appConfigured {
		appDetail = "请在「设置 → 部署」配置 GitHub App"
	}
	add(api.GithubApp, "部署已配置 GitHub App", !appConfigured, appDetail)

	repoOwner, repoName := project.CodeRepo()
	repoFullName := repoOwner + "/" + repoName
	repoLinked := repoOwner != "" && repoName != ""

	// 2. App 已安装到该仓库：保存/读取前实时调一次 GitHub（安装令牌
	// GetRepository），失败即缺项。部署未配置 App 时不发起调用。
	installed := false
	installDetail := "部署未配置 GitHub App"
	if appConfigured && repoLinked && s.gh != nil {
		client, err := s.gh.ClientFor(ctx)
		switch {
		case err != nil:
			installDetail = "GitHub App 访问面不可用：" + err.Error()
		default:
			if err := client.GetRepository(ctx, 0, repoFullName); err != nil {
				installDetail = "App 未安装到该仓库或仓库不可访问：" + err.Error()
			} else {
				installed = true
				installDetail = ""
			}
		}
	} else if appConfigured && !repoLinked {
		installDetail = "项目未关联 GitHub 仓库"
	}
	add(api.GithubAppInstallation, "GitHub App 已安装到该仓库", !installed, installDetail)

	// 3. public_url 已配置。
	publicURL := ""
	if s.publicURL != nil {
		publicURL = s.publicURL.PublicURL(ctx)
	}
	urlDetail := ""
	if publicURL == "" {
		urlDetail = "请在「设置 → 部署」填写对外访问地址"
	}
	add(api.PublicUrl, "已配置对外访问地址（public_url）", publicURL == "", urlDetail)

	// 4. 项目绑定了公开或代码仓库 Token。
	tokenBound := project.PublicGitHubTokenID != "" || project.CodeGitHubTokenID != ""
	tokenDetail := ""
	if !tokenBound {
		tokenDetail = "请在项目设置中绑定公开或代码仓库 Token"
	}
	add(api.GithubToken, "项目已绑定 GitHub Token", !tokenBound, tokenDetail)

	// 5. 每个组合的环境属于当前用户且已绑定该项目（无组合视为满足）。
	envDetail := ""
	for _, c := range append(append([]api.ReviewCombination{}, codeCombos...), visualCombos...) {
		var env model.T3Environment
		if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", c.EnvironmentId, project.UserID).First(&env).Error; err != nil {
			envDetail = fmt.Sprintf("环境 %s 不存在或不属于当前用户", c.EnvironmentId)
			break
		}
		if !t3HasOperateScope(env.BearerScope) {
			envDetail = fmt.Sprintf("环境 %s 是只读会话，不能用于审查", env.Name)
			break
		}
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.ProjectT3Binding{}).
			Where("project_id = ? AND environment_id = ?", project.ID, c.EnvironmentId).
			Count(&n).Error; err != nil || n == 0 {
			envDetail = fmt.Sprintf("环境 %s 未绑定该项目", env.Name)
			break
		}
	}
	add(api.EnvironmentBinding, "组合环境已绑定该项目", envDetail != "", envDetail)

	return out, nil
}

// reviewTakenBy 查同仓库已启用审查的其他项目，返回其拥有者登录名；
// 无占用返回空串。占用判定只看设置行的 repo_full_name 快照（enabled 行的
// 唯一真相，项目行改仓库会刷新它），不再回联 projects。
func (s *ReviewService) reviewTakenBy(ctx context.Context, project *model.Project) (string, error) {
	repoOwner, repoName := project.CodeRepo()
	if repoOwner == "" || repoName == "" {
		return "", nil
	}
	var logins []string
	err := s.db.WithContext(ctx).
		Table("project_review_settings AS s").
		Joins("JOIN users u ON u.id = s.user_id").
		Where("s.enabled AND s.project_id <> ? AND s.repo_full_name = ?",
			project.ID, strings.ToLower(repoOwner+"/"+repoName)).
		Limit(1).
		Pluck("u.username", &logins).Error
	if err != nil {
		return "", errs.ErrInternal
	}
	if len(logins) == 0 {
		return "", nil
	}
	return logins[0], nil
}

// parseCombinations 解析落库 JSON；空串/空数组一律给空切片（契约要求数组非 null）。
func parseCombinations(raw string) ([]api.ReviewCombination, error) {
	if strings.TrimSpace(raw) == "" {
		return []api.ReviewCombination{}, nil
	}
	var persisted []persistedCombination
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return nil, err
	}
	out := make([]api.ReviewCombination, 0, len(persisted))
	for _, p := range persisted {
		c := api.ReviewCombination{
			EnvironmentId: p.EnvironmentID,
			InstanceId:    p.InstanceID,
			Model:         p.Model,
			Options:       p.Options,
		}
		if p.T3ProjectID != "" {
			c.T3ProjectId = api.Ptr(p.T3ProjectID)
		}
		out = append(out, c)
	}
	return out, nil
}

// marshalCombinations 序列化组合落库。
func marshalCombinations(combos []api.ReviewCombination) (string, error) {
	persisted := make([]persistedCombination, 0, len(combos))
	for _, c := range combos {
		p := persistedCombination{
			EnvironmentID: c.EnvironmentId,
			InstanceID:    c.InstanceId,
			Model:         c.Model,
			Options:       c.Options,
		}
		if c.T3ProjectId != nil {
			p.T3ProjectID = *c.T3ProjectId
		}
		persisted = append(persisted, p)
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// fillEnvironmentNames 批量补 environment_name（响应字段）。
func (s *ReviewService) fillEnvironmentNames(ctx context.Context, userID string, lists ...[]api.ReviewCombination) {
	ids := map[string]struct{}{}
	for _, list := range lists {
		for _, c := range list {
			if c.EnvironmentId != "" {
				ids[c.EnvironmentId] = struct{}{}
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	var envs []model.T3Environment
	if err := s.db.WithContext(ctx).Where("id IN ?", idList).Find(&envs).Error; err != nil {
		return // 名称是展示字段，查询失败不阻塞读取
	}
	names := map[string]string{}
	for _, e := range envs {
		names[e.ID] = e.Name
	}
	for _, list := range lists {
		for i := range list {
			if name, ok := names[list[i].EnvironmentId]; ok {
				list[i].EnvironmentName = api.Ptr(name)
			}
		}
	}
}

// cancelProjectReviewRunsTx 取消项目下所有非终态审查运行、消费令牌并清掉
// 防抖行。停用审查、项目换仓库、删除项目都用这条路径。
func cancelProjectReviewRunsTx(tx *gorm.DB, projectID, code string, now time.Time) error {
	active := []model.ReviewRunStatus{
		model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning,
	}
	var runIDs []string
	if err := tx.Model(&model.ReviewRun{}).
		Where("project_id = ? AND status IN ?", projectID, active).
		Pluck("id", &runIDs).Error; err != nil {
		return err
	}
	if len(runIDs) > 0 {
		if err := tx.Model(&model.ReviewRun{}).
			Where("id IN ? AND status IN ?", runIDs, active).
			Updates(map[string]any{
				"status": model.ReviewRunCancelled, "error_code": code,
				"finished_at": now, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ReviewRunToken{}).
			Where("run_id IN ? AND consumed_at IS NULL", runIDs).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("project_id = ?", projectID).Delete(&model.ReviewDebounce{}).Error; err != nil {
		return err
	}
	// 推进 epoch 并清 pending：停用/换仓库时若同步正在进行，旧快照不能在
	// 停用之后继续发布或创建视觉运行。
	return tx.Model(&model.ReviewPullRequest{}).
		Where("project_id = ?", projectID).
		Updates(map[string]any{
			"sync_pending": false,
			"sync_epoch":   gorm.Expr("sync_epoch + 1"),
			"updated_at":   now,
		}).Error
}
