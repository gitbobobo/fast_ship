package service

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ArtifactService struct {
	artifactRepo *repository.ArtifactRepository
	versionRepo  *repository.VersionRepository
	projectRepo  *repository.ProjectRepository
	storage      storage.Storage
}

func NewArtifactService(
	artifactRepo *repository.ArtifactRepository,
	versionRepo *repository.VersionRepository,
	projectRepo *repository.ProjectRepository,
	storage storage.Storage,
) *ArtifactService {
	return &ArtifactService{
		artifactRepo: artifactRepo,
		versionRepo:  versionRepo,
		projectRepo:  projectRepo,
		storage:      storage,
	}
}

func (s *ArtifactService) Upload(versionID, userID, fileName string, fileSize int64, platform string, reader io.Reader) (*model.Artifact, error) {
	version, err := s.versionRepo.FindByID(versionID)
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

	storagePath := fmt.Sprintf("%s/%s/%s", version.ProjectID, versionID, fileName)
	if err := s.storage.Save(storagePath, reader); err != nil {
		return nil, errs.ErrInternal
	}

	artifact := &model.Artifact{
		ID:         uuid.New().String(),
		VersionID:  versionID,
		FileName:   fileName,
		FileSize:   fileSize,
		FilePath:   storagePath,
		Platform:   platform,
		UploadedAt: time.Now(),
	}

	if err := s.artifactRepo.Create(artifact); err != nil {
		// 回滚：删除已保存的文件
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	return artifact, nil
}

func (s *ArtifactService) Delete(id, userID string) error {
	artifact, err := s.artifactRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrArtifactNotFound
		}
		return errs.ErrInternal
	}

	version, err := s.versionRepo.FindByID(artifact.VersionID)
	if err != nil {
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

	if err := s.artifactRepo.Delete(id); err != nil {
		return errs.ErrInternal
	}

	_ = s.storage.Delete(artifact.FilePath)
	return nil
}

func (s *ArtifactService) Download(id string) (io.ReadCloser, string, error) {
	artifact, err := s.artifactRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrArtifactNotFound
		}
		return nil, "", errs.ErrInternal
	}

	reader, err := s.storage.Get(artifact.FilePath)
	if err != nil {
		return nil, "", errs.ErrInternal
	}

	return reader, artifact.FileName, nil
}
