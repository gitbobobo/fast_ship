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

type ProjectService struct {
	projectRepo     *repository.ProjectRepository
	versionRepo     *repository.VersionRepository
	syncStateRepo   *repository.IssueSyncStateRepository
	storage         storage.Storage
	credentials     *ProjectCredentials
	newBranchClient gitHubBranchClientFactory
}

type gitHubBranchClient interface {
	ListBranches(ctx context.Context) ([]*ghclient.Branch, string, error)
}

type gitHubBranchClientFactory func(token, owner, repo string) gitHubBranchClient

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
		newBranchClient: func(token, owner, repo string) gitHubBranchClient {
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
		PublicGitHubTokenID: publicTokenID,
		CodeGitHubTokenID:   codeTokenID,
	}

	if err := s.projectRepo.Create(project); err != nil {
		return nil, errs.ErrInternal
	}

	return s.toResponse(project), nil
}

func (s *ProjectService) Get(id, userID string) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.toResponse(project), nil
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

	resp := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		projectResp := s.toResponse(&p)
		if count, ok := issueCounts[p.ID]; ok {
			projectResp.IssueCount = &count.Total
			projectResp.IssueWorkflowCounts = &api.IssueWorkflowCounts{
				Unset:      count.Unset,
				Todo:       count.Todo,
				InProgress: count.InProgress,
				Done:       count.Done,
			}
		}
		latest, err := s.versionRepo.GetLatestByProjectID(p.ID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrInternal
		}
		if err == nil {
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

// TokenRefUpdate 表达项目更新请求里 Token 引用字段的三态：
// Set=false（JSON key 缺省）保留现值；Set=true 且 Value 为空（显式 null 或空串）
// 清空该用途引用；Set=true 且 Value 非空替换。*string 表达不了三态——
// 显式 null 与 key 缺省解引用后都是 nil，而两者语义相反（清空 vs 保留）。
type TokenRefUpdate struct {
	Set   bool
	Value string
}

// UpdateProjectRequest 是项目更新的 service 入参，不再别名到 api.UpdateProjectRequest：
// 两个 Token 引用字段需三态（见 TokenRefUpdate），其余字段沿用生成类型的指针语义；
// handler 的手写 updateProjectInput 负责把 RawMessage 旁路解析成这里的三态。
type UpdateProjectRequest struct {
	Name                *string
	Description         *string
	RepositoryUrl       *string
	PublicGithubTokenId TokenRefUpdate
	CodeGithubTokenId   TokenRefUpdate
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
		return nil, errs.ErrInternal
	}

	return s.toResponse(project), nil
}

func (s *ProjectService) Delete(id, userID string) error {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
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
	ghClient := s.newBranchClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
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

func (s *ProjectService) toResponse(p *model.Project) *ProjectResponse {
	resp := &ProjectResponse{
		Id:                  p.ID,
		Name:                p.Name,
		Description:         p.Description,
		GithubOwner:         p.GithubOwner,
		GithubRepo:          p.GithubRepo,
		PublicGithubTokenId: p.PublicGitHubTokenID,
		CodeGithubTokenId:   p.CodeGitHubTokenID,
		CreatedAt:           p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:           p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if s.syncStateRepo != nil {
		if state, err := s.syncStateRepo.GetOrCreate(p.ID); err == nil {
			resp.IssueSync = &api.IssueSyncState{
				Status:    state.Status,
				LastError: state.LastError,
			}
			if state.LastIssueUpdatedAt != nil {
				value := state.LastIssueUpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
				resp.IssueSync.LastIssueUpdatedAt = &value
			}
			if state.LastSyncedAt != nil {
				value := state.LastSyncedAt.UTC().Format("2006-01-02T15:04:05Z")
				resp.IssueSync.LastSyncedAt = &value
			}
			if state.LastSuccessfulSyncAt != nil {
				value := state.LastSuccessfulSyncAt.UTC().Format("2006-01-02T15:04:05Z")
				resp.IssueSync.LastSuccessfulSyncAt = &value
			}
		}
	}

	return resp
}
