package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var repoNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// ProjectReviewCleaner 删除项目前清理审查远端资源（线程/工作区）的出口；
// 由 ReviewService 实现，main 注入。nil 时跳过（测试与未启用审查的部署）。
type ProjectReviewCleaner interface {
	CleanupBeforeProjectDelete(ctx context.Context, projectID string)
}

type ProjectService struct {
	projectRepo   *repository.ProjectRepository
	versionRepo   *repository.VersionRepository
	syncStateRepo *repository.IssueSyncStateRepository
	storage       storage.Storage
	credentials   *ProjectCredentials
	newClient     ghclient.ClientFactory
	reviewCleaner ProjectReviewCleaner
}

// SetReviewCleaner 绑定删除项目前的审查远端清理（main.go 注入）。
func (s *ProjectService) SetReviewCleaner(c ProjectReviewCleaner) { s.reviewCleaner = c }

func NewProjectService(
	projectRepo *repository.ProjectRepository,
	versionRepo *repository.VersionRepository,
	syncStateRepo *repository.IssueSyncStateRepository,
	tokenRepo *repository.UserGitHubTokenRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
) *ProjectService {
	return &ProjectService{
		projectRepo:   projectRepo,
		versionRepo:   versionRepo,
		syncStateRepo: syncStateRepo,
		storage:       storage,
		credentials:   newProjectCredentials(projectRepo, tokenRepo, cfg, logger),
		newClient: func(token, owner, repo string) ghclient.Client {
			return ghclient.NewClient(token, owner, repo)
		},
	}
}

func (s *ProjectService) Create(userID string, req *CreateProjectRequest) (*ProjectResponse, error) {
	exists, err := s.projectRepo.ExistsByName(userID, req.Name)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrProjectNameExists
	}

	var owner, repo string
	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		owner, repo, err = parseRepositoryURL(repositoryURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}
	}

	// 代码仓库（PR 审查用）：解析后落 code 字段；与公开仓库相同的归一在
	// normalizeCodeRepo 统一做（与 Update 同一条规则）。
	var codeOwner, codeRepo string
	if codeURL := api.Deref(req.CodeRepositoryUrl); codeURL != "" {
		codeOwner, codeRepo, err = parseRepositoryURL(codeURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}
	}

	// 引用必须属于当前用户；仓库与 Token 相互独立，可只填仓库不选 Token。
	publicTokenID := api.Deref(req.PublicGithubTokenId)
	codeTokenID := api.Deref(req.CodeGithubTokenId)
	if appErr := s.credentials.CheckTokenReference(userID, publicTokenID); appErr != nil {
		return nil, appErr
	}
	if appErr := s.credentials.CheckTokenReference(userID, codeTokenID); appErr != nil {
		return nil, appErr
	}

	project := &model.Project{
		ID:                  uuid.New().String(),
		UserID:              userID,
		Name:                req.Name,
		Description:         api.Deref(req.Description),
		GithubOwner:         owner,
		GithubRepo:          repo,
		CodeGithubOwner:     codeOwner,
		CodeGithubRepo:      codeRepo,
		PublicGitHubTokenID: publicTokenID,
		CodeGitHubTokenID:   codeTokenID,
	}

	normalizeCodeRepo(project)

	if err := s.projectRepo.Create(project); err != nil {
		if errors.Is(err, repository.ErrGitHubTokenMissing) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		return nil, errs.ErrInternal
	}

	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

func (s *ProjectService) Get(id, userID string) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

func (s *ProjectService) List(userID string, page, pageSize int) ([]ProjectResponse, int64, error) {
	projects, total, err := s.projectRepo.List(userID, page, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	// 列表项附带 issue_count（Issue 总数，不分状态）与 issue_workflow_counts
	// （仅 state=open 的 Issue 按内部状态分组）；无 Issue 的项目两者都保持缺省
	projectIDs := make([]string, len(projects))
	for i, p := range projects {
		projectIDs[i] = p.ID
	}
	issueCounts, err := s.projectRepo.GetIssueCounts(projectIDs)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}
	latestVersions, err := s.versionRepo.GetLatestByProjectIDs(projectIDs)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}
	// 同步状态一次批量补齐+取回；失败时整页缺省（旧实现按项目容错跳过，
	// 批量化后粒度放大到整页，语义同为「不拖垮列表」）。
	var syncStates map[string]*model.IssueSyncState
	if s.syncStateRepo != nil {
		if states, err := s.syncStateRepo.GetOrCreateByProjectIDs(projectIDs); err == nil {
			syncStates = states
		}
	}

	resp := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		projectResp := s.toResponse(&p, syncStates[p.ID])
		if count, ok := issueCounts[p.ID]; ok {
			projectResp.IssueCount = &count.Total
			projectResp.IssueWorkflowCounts = &api.IssueWorkflowCounts{
				Unset:      count.Unset,
				Todo:       count.Todo,
				InProgress: count.InProgress,
				Done:       count.Done,
			}
		}
		if latest, ok := latestVersions[p.ID]; ok {
			projectResp.LatestVersion = &LatestVersionResponse{
				Id:            latest.ID,
				VersionNumber: latest.VersionNumber,
				Status:        latest.Status,
				CreatedAt:     latest.CreatedAt.Format("2006-01-02T15:04:05Z"),
			}
		}
		resp[i] = *projectResp
	}
	return resp, total, nil
}

// OptionalStringUpdate 表达项目更新请求里可选字符串字段的三态：
// Set=false（JSON key 缺省）保留现值；Set=true 且 Value 为空（显式 null 或空串）
// 清空；Set=true 且 Value 非空替换。*string 表达不了三态——显式 null 与 key
// 缺省解引用后都是 nil，而两者语义相反（清空 vs 保留）。
type OptionalStringUpdate struct {
	Set   bool
	Value string
}

// TokenRefUpdate 是 OptionalStringUpdate 的旧名（Token 引用字段最先用它），
// 保留别名避免改动既有调用点。
type TokenRefUpdate = OptionalStringUpdate

// UpdateProjectRequest 是项目更新的 service 入参，不再别名到 api.UpdateProjectRequest：
// Token 引用与代码仓库字段需三态（见 OptionalStringUpdate），其余字段沿用生成
// 类型的指针语义；handler 的手写 updateProjectInput 负责把 RawMessage 旁路解析
// 成这里的三态。
type UpdateProjectRequest struct {
	Name                *string
	Description         *string
	RepositoryUrl       *string
	CodeRepositoryUrl   OptionalStringUpdate
	PublicGithubTokenId OptionalStringUpdate
	CodeGithubTokenId   OptionalStringUpdate
}

func (s *ProjectService) Update(id, userID string, req *UpdateProjectRequest) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	if name := api.Deref(req.Name); name != "" && name != project.Name {
		exists, err := s.projectRepo.ExistsByNameExcludeID(userID, name, id)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrProjectNameExists
		}
		project.Name = name
	}

	// description 按指针三态：nil 保留现值、指向空串清空、指向非空替换。
	if req.Description != nil {
		project.Description = *req.Description
	}

	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		owner, repo, err := parseRepositoryURL(repositoryURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}
		project.GithubOwner = owner
		project.GithubRepo = repo
	}

	// 代码仓库三态：key 缺省保留、显式空清空（回落公开仓库）、非空替换。
	// 与公开仓库相同的归一在下面应用完全部仓库字段后统一做。
	if req.CodeRepositoryUrl.Set {
		if req.CodeRepositoryUrl.Value != "" {
			owner, repo, err := parseRepositoryURL(req.CodeRepositoryUrl.Value)
			if err != nil {
				return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
			}
			project.CodeGithubOwner = owner
			project.CodeGithubRepo = repo
		} else {
			project.CodeGithubOwner = ""
			project.CodeGithubRepo = ""
		}
	}

	// 归一收尾：公开与代码仓库字段都已应用完（含请求不带 code key、只改
	// 公开仓库撞上存量 code 仓库的情形），同一规则统一判一次。
	normalizeCodeRepo(project)

	// Token 引用三态：key 缺省保留、显式空清空、非空替换（须属于当前用户）。
	if req.PublicGithubTokenId.Set {
		if appErr := s.credentials.CheckTokenReference(userID, req.PublicGithubTokenId.Value); appErr != nil {
			return nil, appErr
		}
		project.PublicGitHubTokenID = req.PublicGithubTokenId.Value
	}
	if req.CodeGithubTokenId.Set {
		if appErr := s.credentials.CheckTokenReference(userID, req.CodeGithubTokenId.Value); appErr != nil {
			return nil, appErr
		}
		project.CodeGitHubTokenID = req.CodeGithubTokenId.Value
	}

	if err := s.projectRepo.Update(project); err != nil {
		if errors.Is(err, repository.ErrGitHubTokenMissing) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

func (s *ProjectService) Delete(id, userID string) error {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}

	// 项目删除会 CASCADE 掉审查运行行；先尽力停掉远端线程并回收工作区，
	// 否则这些线程/工作区再也不会被调度发现（归档与回收都按运行行驱动）。
	if s.reviewCleaner != nil {
		s.reviewCleaner.CleanupBeforeProjectDelete(context.Background(), project.ID)
	}
	if err := s.projectRepo.Delete(id, userID); err != nil {
		return errs.ErrInternal
	}

	_ = s.storage.DeletePrefix(project.ID)
	return nil
}

// GetBranches fetches all branches from the project's GitHub repository.
func (s *ProjectService) GetBranches(ctx context.Context, id, userID string) ([]BranchResponse, string, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrProjectNotFound
		}
		return nil, "", errs.ErrInternal
	}

	tokenBytes, appErr := s.credentials.requiredGitHubToken(project)
	if appErr != nil {
		return nil, "", appErr
	}

	// Create GitHub client and fetch branches.
	ghClient := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	branches, defaultBranch, err := ghClient.ListBranches(ctx)
	if err != nil {
		return nil, "", errs.New(50200, fmt.Sprintf("Failed to fetch branches: %v", err))
	}

	// Convert to response format
	resp := make([]BranchResponse, len(branches))
	for i, b := range branches {
		resp[i] = BranchResponse{
			Name:    b.Name,
			Sha:     b.SHA,
			Default: b.Default,
		}
	}

	return resp, defaultBranch, nil
}

// normalizeCodeRepo 归一收尾：代码仓库与公开仓库大小写不敏感相同时清空 code
// 字段（空 = 同公开仓库，规范存储不存冗余值）。必须在应用完全部仓库字段之后
// 调用，Create/Update 共用这一条规则，写侧不另设第二处归一判断。
func normalizeCodeRepo(p *model.Project) {
	if p.CodeGithubOwner != "" && strings.EqualFold(
		p.CodeGithubOwner+"/"+p.CodeGithubRepo, p.GithubOwner+"/"+p.GithubRepo) {
		p.CodeGithubOwner, p.CodeGithubRepo = "", ""
	}
}

func parseRepositoryURL(raw string) (owner, repo string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", errors.New("仓库链接不能为空")
	}

	if idx := strings.Index(s, "://"); idx != -1 {
		s = s[idx+3:]
	}

	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "github.com/") {
		s = s[11:]
	}

	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return "", "", errors.New("仓库链接格式无效，应为 owner/repo 或 https://github.com/owner/repo")
	}

	owner = strings.TrimSpace(parts[0])
	repo = strings.TrimSpace(parts[1])

	if owner == "" || repo == "" {
		return "", "", errors.New("仓库链接格式无效，owner 和 repo 不能为空")
	}

	if !repoNameRegex.MatchString(owner) || !repoNameRegex.MatchString(repo) {
		return "", "", errors.New("仓库链接包含非法字符")
	}

	return owner, repo, nil
}

// toResponse 组装项目响应；syncState 由调用方提供——单项目路径用
// syncStateFor 现取，列表路径经 GetOrCreateByProjectIDs 批量预取后逐条传入，
// nil 表示同步状态缺省。
func (s *ProjectService) toResponse(p *model.Project, syncState *model.IssueSyncState) *ProjectResponse {
	resp := &ProjectResponse{
		Id:                  p.ID,
		Name:                p.Name,
		Description:         p.Description,
		GithubOwner:         p.GithubOwner,
		GithubRepo:          p.GithubRepo,
		CodeGithubOwner:     p.CodeGithubOwner,
		CodeGithubRepo:      p.CodeGithubRepo,
		PublicGithubTokenId: p.PublicGitHubTokenID,
		CodeGithubTokenId:   p.CodeGitHubTokenID,
		CreatedAt:           p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:           p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if syncState != nil {
		resp.IssueSync = &api.IssueSyncState{
			Status:    syncState.Status,
			LastError: syncState.LastError,
		}
		if syncState.LastIssueUpdatedAt != nil {
			value := syncState.LastIssueUpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastIssueUpdatedAt = &value
		}
		if syncState.LastSyncedAt != nil {
			value := syncState.LastSyncedAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastSyncedAt = &value
		}
		if syncState.LastSuccessfulSyncAt != nil {
			value := syncState.LastSuccessfulSyncAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastSuccessfulSyncAt = &value
		}
	}

	return resp
}

// syncStateFor 单项目路径现取同步状态；repo 未挂载或读取失败时容错为 nil。
func (s *ProjectService) syncStateFor(projectID string) *model.IssueSyncState {
	if s.syncStateRepo == nil {
		return nil
	}
	state, err := s.syncStateRepo.GetOrCreate(projectID)
	if err != nil {
		return nil
	}
	return state
}
