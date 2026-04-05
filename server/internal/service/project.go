package service

import (
	"errors"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ProjectService struct {
	projectRepo *repository.ProjectRepository
	cfg         *config.Config
}

func NewProjectService(projectRepo *repository.ProjectRepository, cfg *config.Config) *ProjectService {
	return &ProjectService{projectRepo: projectRepo, cfg: cfg}
}

type CreateProjectRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=100"`
	Description string `json:"description"`
	GithubOwner string `json:"github_owner" binding:"required"`
	GithubRepo  string `json:"github_repo" binding:"required"`
	GithubToken string `json:"github_token" binding:"required"`
}

type UpdateProjectRequest struct {
	Name        string `json:"name" binding:"omitempty,min=1,max=100"`
	Description string `json:"description"`
	GithubOwner string `json:"github_owner"`
	GithubRepo  string `json:"github_repo"`
	GithubToken string `json:"github_token"`
}

type ProjectResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	GithubOwner string `json:"github_owner"`
	GithubRepo  string `json:"github_repo"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (s *ProjectService) Create(userID string, req *CreateProjectRequest) (*ProjectResponse, error) {
	exists, err := s.projectRepo.ExistsByName(userID, req.Name)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrProjectNameExists
	}

	encryptedToken, err := crypto.Encrypt([]byte(req.GithubToken), []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, errs.ErrInternal
	}

	project := &model.Project{
		ID:                   uuid.New().String(),
		UserID:               userID,
		Name:                 req.Name,
		Description:          req.Description,
		GithubOwner:          req.GithubOwner,
		GithubRepo:           req.GithubRepo,
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
		resp[i] = *s.toResponse(&p)
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

	project.Description = req.Description

	if req.GithubOwner != "" {
		project.GithubOwner = req.GithubOwner
	}
	if req.GithubRepo != "" {
		project.GithubRepo = req.GithubRepo
	}
	if req.GithubToken != "" {
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
	_, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return s.projectRepo.Delete(id, userID)
}

func (s *ProjectService) toResponse(p *model.Project) *ProjectResponse {
	return &ProjectResponse{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		GithubOwner: p.GithubOwner,
		GithubRepo:  p.GithubRepo,
		CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}
