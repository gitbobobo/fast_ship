package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var repoNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

type ProjectService struct {
	projectRepo     *repository.ProjectRepository
	versionRepo     *repository.VersionRepository
	syncStateRepo   *repository.IssueSyncStateRepository
	storage         storage.Storage
	cfg             *config.Config
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
) *ProjectService {
	return &ProjectService{
		projectRepo:   projectRepo,
		versionRepo:   versionRepo,
		syncStateRepo: syncStateRepo,
		storage:       storage,
		cfg:           cfg,
		newBranchClient: func(token, owner, repo string) gitHubBranchClient {
			return ghclient.NewClient(token, owner, repo)
		},
	}
}

type CreateProjectRequest struct {
	Name            string `json:"name" binding:"required,min=1,max=100"`
	Description     string `json:"description"`
	RepositoryURL   string `json:"repository_url" binding:"required"`
	GithubToken     string `json:"github_token"`
	SourceProjectID string `json:"source_project_id"`
}

type UpdateProjectRequest struct {
	Name            string `json:"name" binding:"omitempty,min=1,max=100"`
	Description     string `json:"description"`
	RepositoryURL   string `json:"repository_url"`
	GithubToken     string `json:"github_token"`
	SourceProjectID string `json:"source_project_id"`
}

type ProjectResponse struct {
	ID            string                         `json:"id"`
	Name          string                         `json:"name"`
	Description   string                         `json:"description"`
	GithubOwner   string                         `json:"github_owner"`
	GithubRepo    string                         `json:"github_repo"`
	LatestVersion *LatestVersionResponse         `json:"latest_version,omitempty"`
	IssueSync     *serviceIssueSyncStateResponse `json:"issue_sync,omitempty"`
	CreatedAt     string                         `json:"created_at"`
	UpdatedAt     string                         `json:"updated_at"`
}

type serviceIssueSyncStateResponse struct {
	Status               model.IssueSyncStatus `json:"status"`
	LastIssueUpdatedAt   *string               `json:"last_issue_updated_at,omitempty"`
	LastSyncedAt         *string               `json:"last_synced_at,omitempty"`
	LastSuccessfulSyncAt *string               `json:"last_successful_sync_at,omitempty"`
	LastError            string                `json:"last_error"`
}

type LatestVersionResponse struct {
	ID            string              `json:"id"`
	VersionNumber string              `json:"version_number"`
	Status        model.VersionStatus `json:"status"`
	CreatedAt     string              `json:"created_at"`
}

type BranchResponse struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Default bool   `json:"default"`
}

func (s *ProjectService) Create(userID string, req *CreateProjectRequest) (*ProjectResponse, error) {
	exists, err := s.projectRepo.ExistsByName(userID, req.Name)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrProjectNameExists
	}

	owner, repo, err := parseRepositoryURL(req.RepositoryURL)
	if err != nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
	}

	var encryptedToken []byte
	if req.SourceProjectID != "" {
		sourceProject, err := s.projectRepo.FindByID(req.SourceProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		encryptedToken = sourceProject.GithubTokenEncrypted
	} else if req.GithubToken != "" {
		encryptedToken, err = crypto.Encrypt([]byte(req.GithubToken), []byte(s.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
		}
	} else {
		return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": 请输入 GitHub Token 或选择复用已有项目的 Token")
	}

	project := &model.Project{
		ID:                   uuid.New().String(),
		UserID:               userID,
		Name:                 req.Name,
		Description:          req.Description,
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
				ID:            latest.ID,
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

	if req.Name != "" && req.Name != project.Name {
		exists, err := s.projectRepo.ExistsByNameExcludeID(userID, req.Name, id)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrProjectNameExists
		}
		project.Name = req.Name
	}

	if req.RepositoryURL != "" {
		owner, repo, err := parseRepositoryURL(req.RepositoryURL)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
		}
		project.GithubOwner = owner
		project.GithubRepo = repo
	}

	if req.SourceProjectID != "" {
		sourceProject, err := s.projectRepo.FindByID(req.SourceProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		project.GithubTokenEncrypted = sourceProject.GithubTokenEncrypted
	} else if req.GithubToken != "" {
		encryptedToken, err := crypto.Encrypt([]byte(req.GithubToken), []byte(s.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
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

	// Decrypt GitHub token
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, "", errs.ErrInternal
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
			SHA:     b.SHA,
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
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		GithubOwner: p.GithubOwner,
		GithubRepo:  p.GithubRepo,
		CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if s.syncStateRepo != nil {
		if state, err := s.syncStateRepo.GetOrCreate(p.ID); err == nil {
			resp.IssueSync = &serviceIssueSyncStateResponse{
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
