package service

import (
	"errors"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type VersionService struct {
	versionRepo *repository.VersionRepository
	projectRepo *repository.ProjectRepository
	storage     storage.Storage
}

func NewVersionService(versionRepo *repository.VersionRepository, projectRepo *repository.ProjectRepository, storage storage.Storage) *VersionService {
	return &VersionService{
		versionRepo: versionRepo,
		projectRepo: projectRepo,
		storage:     storage,
	}
}

type CreateVersionRequest struct {
	VersionNumber   string `json:"version_number" binding:"required"`
	ReleaseNotes    string `json:"release_notes"`
	TargetCommitish string `json:"target_commitish"`
}

type UpdateVersionRequest struct {
	ReleaseNotes    string `json:"release_notes"`
	TargetCommitish string `json:"target_commitish"`
}

func (s *VersionService) Create(projectID, userID string, req *CreateVersionRequest) (*model.Version, error) {
	// 校验项目归属
	_, err := s.projectRepo.FindByID(projectID, userID)
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

func (s *VersionService) Update(id, userID string, req *UpdateVersionRequest) (*model.Version, error) {
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
	_, err = s.projectRepo.FindByID(version.ProjectID, userID)
	if err != nil {
		return nil, errs.ErrNotOwner
	}

	version.ReleaseNotes = req.ReleaseNotes
	version.TargetCommitish = req.TargetCommitish

	if err := s.versionRepo.Update(version); err != nil {
		return nil, errs.ErrInternal
	}

	return version, nil
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
