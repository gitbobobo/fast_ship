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
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
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
	cfg             *config.Config
	logger          *zap.Logger
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
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
) *ProjectService {
	return &ProjectService{
		projectRepo:   projectRepo,
		versionRepo:   versionRepo,
		syncStateRepo: syncStateRepo,
		storage:       storage,
		cfg:           cfg,
		logger:        logger,
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
	var encryptedToken []byte

	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		owner, repo, err = parseRepositoryURL(repositoryURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}
		encryptedToken, err = s.resolveGitHubToken(userID, api.Deref(req.GithubToken), api.Deref(req.SourceProjectId))
		if err != nil {
			return nil, err
		}
	}

	project := &model.Project{
		ID:                   uuid.New().String(),
		UserID:               userID,
		Name:                 req.Name,
		Description:          api.Deref(req.Description),
		GithubOwner:          owner,
		GithubRepo:           repo,
		GithubTokenEncrypted: encryptedToken,
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

	resp := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		projectResp := s.toResponse(&p)
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

	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		owner, repo, err := parseRepositoryURL(repositoryURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}

		willHaveToken := api.Deref(req.GithubToken) != "" || api.Deref(req.SourceProjectId) != "" || len(project.GithubTokenEncrypted) > 0
		if !willHaveToken {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 请输入 GitHub Token 或选择复用已有项目的 Token")
		}

		project.GithubOwner = owner
		project.GithubRepo = repo
	}

	if api.Deref(req.SourceProjectId) != "" || api.Deref(req.GithubToken) != "" {
		encryptedToken, err := s.resolveGitHubToken(userID, api.Deref(req.GithubToken), api.Deref(req.SourceProjectId))
		if err != nil {
			return nil, err
		}
		project.GithubTokenEncrypted = encryptedToken
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

	tokenBytes, appErr := requiredProjectGitHubToken(project, s.cfg, s.logger)
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

// resolveGitHubToken 从 Token 字符串或源项目解析加密后的 Token，优先使用 sourceProjectID。
func (s *ProjectService) resolveGitHubToken(userID, githubToken, sourceProjectID string) ([]byte, error) {
	if sourceProjectID != "" {
		sourceProject, err := s.projectRepo.FindByID(sourceProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		return sourceProject.GithubTokenEncrypted, nil
	}
	if githubToken != "" {
		encryptedToken, err := crypto.Encrypt([]byte(githubToken), []byte(s.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
		}
		return encryptedToken, nil
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 请输入 GitHub Token 或选择复用已有项目的 Token")
}

func (s *ProjectService) toResponse(p *model.Project) *ProjectResponse {
	resp := &ProjectResponse{
		Id:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		GithubOwner: p.GithubOwner,
		GithubRepo:  p.GithubRepo,
		CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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
