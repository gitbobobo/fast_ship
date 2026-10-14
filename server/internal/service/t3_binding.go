package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// T3BindingService 承载 Fast Ship 项目 ↔ T3 项目显式绑定（INT-89）。
//
// 绑定是「启动线程」的环境候选来源：用户（或经 Agent 辅助）把 Fast Ship 项目
// 绑到某 T3 环境中的某 T3 项目——主键 (project_id, environment_id)，每个
// 环境至多绑一个；不做路径推断，不写 Issue ↔ 线程关联。
//
// 发现与绑定校验复用线程只读面的读取路径（HTTP 会话探测 + WS shell 快照
// projects，活跃 + 归档去重合并）：PUT 实时确认 T3 项目存在后才把
// t3_project_title/workspace_root 快照落库，不保存未校验的绑定；快照让
// 绑定在环境不可达时仍可展示，与最新发现不一致即失效绑定。
// 单环境失败只标在该环境条目上，不阻塞其它环境（与线程列表同一隔离口径）。
type T3BindingService struct {
	t3          *T3ThreadsService
	projectRepo *repository.ProjectRepository
	bindingRepo *repository.ProjectT3BindingRepository
	logger      *zap.Logger
}

func NewT3BindingService(threads *T3ThreadsService, projectRepo *repository.ProjectRepository, bindingRepo *repository.ProjectT3BindingRepository) *T3BindingService {
	return &T3BindingService{
		t3:          threads,
		projectRepo: projectRepo,
		bindingRepo: bindingRepo,
		logger:      threads.logger,
	}
}

// DiscoverProjects GET /api/environments/t3-projects：逐环境实时拉取 T3 项目表，
// 多环境有界并行、各自超时预算；单环境失败只写该条目的 status/error。
func (s *T3BindingService) DiscoverProjects(ctx context.Context, userID string) ([]api.T3EnvironmentProjects, error) {
	envs, err := s.t3.envRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	items := make([]api.T3EnvironmentProjects, len(envs))
	roundCtx, cancelRound := context.WithTimeout(ctx, s.t3.budgets.Round)
	defer cancelRound()
	runT3Bounded(roundCtx, len(envs), t3ListConcurrency, s.t3.budgets.PerEnvironment, func(envCtx context.Context, i int) {
		items[i] = s.discoverEnvironmentProjects(envCtx, &envs[i])
	})
	return items, nil
}

// discoverEnvironmentProjects 探测 + 快照读取一个环境；任何失败收敛到该条目的
// status/error，projects 保持空数组（不返回 nil slice，契约恒为数组）。
func (s *T3BindingService) discoverEnvironmentProjects(ctx context.Context, env *model.T3Environment) api.T3EnvironmentProjects {
	item := api.T3EnvironmentProjects{
		EnvironmentId: env.ID,
		Name:          env.Name,
		Access:        t3AccessOf(env.BearerScope),
		Projects:      []api.T3DiscoveredProject{},
	}
	token, err := s.t3.bearerToken(env)
	if err != nil {
		item.Status = T3EnvironmentStatusRepair
		item.Error = api.NonEmpty("环境凭证不可用，请重新配对")
		return item
	}
	status, probeErr := s.t3.http.probeSession(ctx, env.Address, token)
	if status != T3EnvironmentStatusOK {
		item.Status = status
		item.Error = api.NonEmpty(probeErr)
		return item
	}
	snaps, err := s.t3.fetchShellSnapshots(ctx, env, token)
	if err != nil {
		status, msg := t3StatusFromError(err)
		item.Status = status
		item.Error = api.NonEmpty(msg)
		return item
	}
	item.Projects = mergeT3DiscoveredProjects(snaps)
	if snaps.ArchiveErr != nil {
		// 归档面失败不推翻活跃面结果（G1）：活跃项目照常返回，环境标 degraded。
		s.logger.Warn("t3 archived shell snapshot read failed during project discovery",
			zap.String("environment_id", env.ID), zap.Error(snaps.ArchiveErr))
		item.Status = T3EnvironmentStatusDegraded
		item.Error = api.NonEmpty("归档项目暂不可读")
		return item
	}
	item.Status = T3EnvironmentStatusOK
	return item
}

// mergeT3DiscoveredProjects 合并活跃 + 归档两份快照的项目表，按 ID 去重
// （与 ComposerOptions 同一合并口径：活跃优先）。
func mergeT3DiscoveredProjects(snaps *t3ShellSnapshots) []api.T3DiscoveredProject {
	projects := make([]api.T3DiscoveredProject, 0, len(snaps.Active.Projects)+len(snaps.Archived.Projects))
	seen := make(map[string]struct{}, len(snaps.Active.Projects)+len(snaps.Archived.Projects))
	for _, source := range [][]t3ProjectShell{snaps.Active.Projects, snaps.Archived.Projects} {
		for _, project := range source {
			if _, dup := seen[project.ID]; dup {
				continue
			}
			seen[project.ID] = struct{}{}
			item := api.T3DiscoveredProject{Id: project.ID, Title: project.Title}
			if project.WorkspaceRoot != nil && *project.WorkspaceRoot != "" {
				item.WorkspaceRoot = api.Ptr(*project.WorkspaceRoot)
			}
			projects = append(projects, item)
		}
	}
	return projects
}

// findT3Project 跨活跃 + 归档快照按 ID 找 T3 项目（与 CreateThread 同一解析路径）。
func findT3Project(snaps *t3ShellSnapshots, projectID string) *t3ProjectShell {
	for _, source := range [][]t3ProjectShell{snaps.Active.Projects, snaps.Archived.Projects} {
		for i := range source {
			if source[i].ID == projectID {
				return &source[i]
			}
		}
	}
	return nil
}

// List GET /api/projects/:id/t3-bindings：列出项目绑定，附环境名与实时探测
// 状态。探测按绑定行有界并行（每环境至多一行）；环境记录缺失（FK 级联兜底，
// 理论不可达）时环境名为空、状态 unreachable。
func (s *T3BindingService) List(ctx context.Context, userID, projectID string) ([]api.ProjectT3Binding, error) {
	if err := s.requireProject(projectID, userID); err != nil {
		return nil, err
	}
	bindings, err := s.bindingRepo.ListByProjectID(ctx, projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	envs := make([]*model.T3Environment, len(bindings))
	for i := range bindings {
		env, err := s.t3.envRepo.FindByIDAndUserID(ctx, bindings[i].EnvironmentID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, errs.ErrInternal
		}
		envs[i] = env
	}

	statuses := make([]T3EnvironmentStatus, len(bindings))
	roundCtx, cancelRound := context.WithTimeout(ctx, s.t3.budgets.Round)
	defer cancelRound()
	runT3Bounded(roundCtx, len(bindings), t3ListConcurrency, s.t3.budgets.PerEnvironment, func(envCtx context.Context, i int) {
		env := envs[i]
		if env == nil {
			statuses[i] = T3EnvironmentStatusUnreachable
			return
		}
		token, err := s.t3.bearerToken(env)
		if err != nil {
			statuses[i] = T3EnvironmentStatusRepair
			return
		}
		status, _ := s.t3.http.probeSession(envCtx, env.Address, token)
		statuses[i] = status
	})

	items := make([]api.ProjectT3Binding, 0, len(bindings))
	for i := range bindings {
		items = append(items, t3BindingResponse(&bindings[i], envs[i], statuses[i]))
	}
	return items, nil
}

// Upsert PUT /api/projects/:id/t3-bindings/:environmentId：实时校验 T3 项目在该
// 环境存在后才写入快照——T3 项目不在该环境 40401，环境不存在 40417，环境不可达/
// 凭证失效/上游错误 50202，不保存未校验的绑定。
func (s *T3BindingService) Upsert(ctx context.Context, userID, projectID, envID, t3ProjectID string) (*api.ProjectT3Binding, error) {
	if strings.TrimSpace(t3ProjectID) == "" {
		return nil, errs.ErrInvalidParams
	}
	if err := s.requireProject(projectID, userID); err != nil {
		return nil, err
	}
	env, err := s.t3.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	ctx, cancel := context.WithTimeout(ctx, s.t3.budgets.PerEnvironment)
	defer cancel()

	token, err := s.t3.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	// 与线程详情同一探测：WS 握手失败不区分状态码，先 HTTP 探明凭证状态。
	if status, probeErr := s.t3.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}
	snaps, err := s.t3.fetchShellSnapshots(ctx, env, token)
	if err != nil {
		return nil, s.t3.upstreamError(err)
	}

	project := findT3Project(snaps, t3ProjectID)
	if project == nil {
		// 活跃面没有：归档读取失败时归档面等价于「没读过」，不能据此下
		// 「项目不存在」的结论（G1），回上游错误让调用方重试。
		if snaps.ArchiveErr != nil {
			return nil, s.t3.upstreamError(fmt.Errorf("归档快照读取失败: %w", snaps.ArchiveErr))
		}
		return nil, errs.New(errs.ErrProjectNotFound.Code, "T3 项目不在该环境（或项目缺少 workspaceRoot）")
	}
	if project.WorkspaceRoot == nil || *project.WorkspaceRoot == "" {
		return nil, errs.New(errs.ErrProjectNotFound.Code, "T3 项目不在该环境（或项目缺少 workspaceRoot）")
	}

	if err := ctx.Err(); err != nil {
		return nil, errs.ErrInternal
	}
	now := time.Now().UTC()
	binding := &model.ProjectT3Binding{
		ProjectID:      projectID,
		EnvironmentID:  envID,
		T3ProjectID:    project.ID,
		T3ProjectTitle: project.Title,
		WorkspaceRoot:  *project.WorkspaceRoot,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.bindingRepo.Upsert(ctx, binding); err != nil {
		return nil, errs.ErrInternal
	}
	resp := t3BindingResponse(binding, env, T3EnvironmentStatusOK)
	return &resp, nil
}

// Delete DELETE /api/projects/:id/t3-bindings/:environmentId：解绑；
// 已解绑时重复删除返回成功（DELETE 幂等）。
func (s *T3BindingService) Delete(ctx context.Context, userID, projectID, envID string) error {
	if err := s.requireProject(projectID, userID); err != nil {
		return err
	}
	if err := s.bindingRepo.Delete(ctx, projectID, envID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// requireProject 项目归属校验：跨用户与不存在的项目一律 40401（与项目写入口径一致）。
func (s *T3BindingService) requireProject(projectID, userID string) error {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func t3BindingResponse(binding *model.ProjectT3Binding, env *model.T3Environment, status T3EnvironmentStatus) api.ProjectT3Binding {
	resp := api.ProjectT3Binding{
		EnvironmentId:     binding.EnvironmentID,
		EnvironmentStatus: status,
		T3ProjectId:       binding.T3ProjectID,
		T3ProjectTitle:    binding.T3ProjectTitle,
		WorkspaceRoot:     binding.WorkspaceRoot,
		CreatedAt:         formatTime(binding.CreatedAt.UTC()),
		UpdatedAt:         formatTime(binding.UpdatedAt.UTC()),
	}
	if env != nil {
		resp.EnvironmentName = env.Name
	}
	return resp
}
