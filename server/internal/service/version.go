package service

import (
	"context"
	"errors"
	"fmt"

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

type VersionService struct {
	versionRepo     *repository.VersionRepository
	projectRepo     *repository.ProjectRepository
	storage         storage.Storage
	cfg             *config.Config
	newBranchClient gitHubBranchClientFactory
}

func NewVersionService(versionRepo *repository.VersionRepository, projectRepo *repository.ProjectRepository, storage storage.Storage, cfg *config.Config) *VersionService {
	return &VersionService{
		versionRepo: versionRepo,
		projectRepo: projectRepo,
		storage:     storage,
		cfg:         cfg,
		newBranchClient: func(token, owner, repo string) gitHubBranchClient {
			return ghclient.NewClient(token, owner, repo)
		},
	}
}

type CreateVersionRequest struct {
	VersionNumber   string `json:"version_number" binding:"required"`
	ReleaseNotes    string `json:"release_notes"`
	TargetCommitish string `json:"target_commitish"`
}

type UpdateVersionRequest struct {
	VersionNumber   *string `json:"version_number"`
	ReleaseNotes    *string `json:"release_notes"`
	TargetCommitish *string `json:"target_commitish"`
}

func (s *VersionService) Create(ctx context.Context, projectID, userID string, req *CreateVersionRequest) (*model.Version, error) {
	// 校验项目归属
	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	exists, err := s.versionRepo.ExistsByVersionNumber(projectID, req.VersionNumber)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrVersionNumberExists
	}
	if req.TargetCommitish != "" {
		if err := s.ensureTargetBranchExists(ctx, project, req.TargetCommitish); err != nil {
			return nil, err
		}
	}

	version := &model.Version{
		ID:              uuid.New().String(),
		ProjectID:       projectID,
		VersionNumber:   req.VersionNumber,
		Status:          model.VersionStatusPending,
		ReleaseNotes:    req.ReleaseNotes,
		TargetCommitish: req.TargetCommitish,
	}

	if err := s.versionRepo.Create(version); err != nil {
		return nil, errs.ErrInternal
	}

	return version, nil
}

func (s *VersionService) Get(id, userID string) (*model.Version, error) {
	version, err := s.versionRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrVersionNotFound
		}
		return nil, errs.ErrInternal
	}

	if _, err := s.projectRepo.FindByID(version.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}

	return version, nil
}

func (s *VersionService) List(projectID, userID string, status string, page, pageSize int) ([]model.Version, int64, error) {
	// 校验项目归属
	_, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrInternal
	}

	return s.versionRepo.List(projectID, status, page, pageSize)
}

func (s *VersionService) Update(ctx context.Context, id, userID string, allowVersionNumberEdit bool, req *UpdateVersionRequest) (*model.Version, error) {
	version, err := s.versionRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrVersionNotFound
		}
		return nil, errs.ErrInternal
	}

	if version.Status != model.VersionStatusPending {
		return nil, errs.ErrVersionNotPending
	}

	// 校验项目归属
	project, err := s.projectRepo.FindByID(version.ProjectID, userID)
	if err != nil {
		return nil, errs.ErrNotOwner
	}

	if req.VersionNumber != nil {
		if !allowVersionNumberEdit {
			return nil, errs.ErrApiKeyForbidden
		}
		if *req.VersionNumber == "" {
			return nil, errs.ErrInvalidParams
		}
		if *req.VersionNumber != version.VersionNumber {
			exists, err := s.versionRepo.ExistsByVersionNumberExcludeID(version.ProjectID, *req.VersionNumber, version.ID)
			if err != nil {
				return nil, errs.ErrInternal
			}
			if exists {
				return nil, errs.ErrVersionNumberExists
			}
			version.VersionNumber = *req.VersionNumber
		}
	}

	if req.ReleaseNotes != nil {
		version.ReleaseNotes = *req.ReleaseNotes
	}

	if req.TargetCommitish != nil {
		if *req.TargetCommitish != "" {
			if err := s.ensureTargetBranchExists(ctx, project, *req.TargetCommitish); err != nil {
				return nil, err
			}
		}
		version.TargetCommitish = *req.TargetCommitish
	}

	if err := s.versionRepo.Update(version); err != nil {
		return nil, errs.ErrInternal
	}

	return version, nil
}

func (s *VersionService) ensureTargetBranchExists(ctx context.Context, project *model.Project, branchName string) error {
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return errs.ErrInternal
	}

	branches, _, err := s.newBranchClient(string(tokenBytes), project.GithubOwner, project.GithubRepo).ListBranches(ctx)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("获取 GitHub 分支失败: %v", err))
	}
	for _, branch := range branches {
		if branch.Name == branchName {
			return nil
		}
	}
	return errs.ErrTargetBranchNotFound
}

func (s *VersionService) Delete(id, userID string) error {
	version, err := s.versionRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrVersionNotFound
		}
		return errs.ErrInternal
	}

	if version.Status != model.VersionStatusPending {
		return errs.ErrVersionNotPending
	}

	// 校验项目归属
	_, err = s.projectRepo.FindByID(version.ProjectID, userID)
	if err != nil {
		return errs.ErrNotOwner
	}

	if err := s.versionRepo.Delete(id); err != nil {
		return errs.ErrInternal
	}

	for _, artifact := range version.Artifacts {
		_ = s.storage.Delete(artifact.FilePath)
	}

	return nil
}
