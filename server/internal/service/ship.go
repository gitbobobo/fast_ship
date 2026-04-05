package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type ShipService struct {
	versionRepo  *repository.VersionRepository
	projectRepo  *repository.ProjectRepository
	artifactRepo *repository.ArtifactRepository
	storage      storage.Storage
	cfg          *config.Config
	logger       *zap.Logger
}

func NewShipService(
	versionRepo *repository.VersionRepository,
	projectRepo *repository.ProjectRepository,
	artifactRepo *repository.ArtifactRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
) *ShipService {
	return &ShipService{
		versionRepo:  versionRepo,
		projectRepo:  projectRepo,
		artifactRepo: artifactRepo,
		storage:      storage,
		cfg:          cfg,
		logger:       logger,
	}
}

func (s *ShipService) Ship(versionID, userID string) error {
	version, err := s.versionRepo.FindByID(versionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrVersionNotFound
		}
		return errs.ErrInternal
	}

	if version.Status != model.VersionStatusPending {
		return errs.ErrVersionNotPending
	}

	project, err := s.projectRepo.FindByID(version.ProjectID, userID)
	if err != nil {
		return errs.ErrNotOwner
	}

	// 前置校验
	if err := s.preCheck(version); err != nil {
		return err
	}

	// 解密 GitHub Token
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		s.logger.Error("decrypt github token failed", zap.Error(err))
		return errs.ErrInternal
	}

	gh := ghclient.NewClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	ctx := context.Background()

	// 创建 Tag
	s.logger.Info("creating tag", zap.String("tag", version.VersionNumber))
	if err := gh.CreateTag(ctx, version.VersionNumber, version.TargetCommitish); err != nil {
		s.recordError(version, fmt.Sprintf("创建 Tag 失败: %v", err))
		return errs.New(50200, fmt.Sprintf("创建 Tag 失败: %v", err))
	}

	// 创建 Release
	s.logger.Info("creating release", zap.String("tag", version.VersionNumber))
	release, err := gh.CreateRelease(ctx, version.VersionNumber, version.VersionNumber, version.ReleaseNotes)
	if err != nil {
		s.recordError(version, fmt.Sprintf("创建 Release 失败: %v", err))
		return errs.New(50201, fmt.Sprintf("创建 Release 失败: %v", err))
	}

	// 并行上传安装包
	artifacts, err := s.artifactRepo.ListByVersionID(versionID)
	if err != nil {
		return errs.ErrInternal
	}

	var wg sync.WaitGroup
	uploadErrors := make([]error, len(artifacts))

	for i, artifact := range artifacts {
		wg.Add(1)
		go func(idx int, a model.Artifact) {
			defer wg.Done()
			s.logger.Info("uploading artifact", zap.String("file", a.FileName))

			reader, err := s.storage.Get(a.FilePath)
			if err != nil {
				uploadErrors[idx] = fmt.Errorf("读取文件 %s 失败: %w", a.FileName, err)
				return
			}
			defer reader.Close()

			// 需要 *os.File，先写到临时文件
			tmpFile, err := os.CreateTemp("", "ship-upload-*")
			if err != nil {
				uploadErrors[idx] = fmt.Errorf("创建临时文件失败: %w", err)
				return
			}
			defer os.Remove(tmpFile.Name())
			defer tmpFile.Close()

			if _, err := tmpFile.ReadFrom(reader); err != nil {
				uploadErrors[idx] = fmt.Errorf("写入临时文件失败: %w", err)
				return
			}
			if _, err := tmpFile.Seek(0, 0); err != nil {
				uploadErrors[idx] = fmt.Errorf("seek 临时文件失败: %w", err)
				return
			}

			if err := gh.UploadAsset(ctx, release.GetID(), a.FileName, tmpFile); err != nil {
				uploadErrors[idx] = err
			}
		}(i, artifact)
	}

	wg.Wait()

	// 检查上传错误
	for _, uerr := range uploadErrors {
		if uerr != nil {
			s.recordError(version, fmt.Sprintf("上传安装包失败: %v", uerr))
			return errs.New(50202, fmt.Sprintf("上传安装包失败: %v", uerr))
		}
	}

	// 更新状态
	now := time.Now()
	version.Status = model.VersionStatusShipped
	version.ShippedAt = &now
	version.GithubReleaseURL = release.GetHTMLURL()
	version.ErrorLog = ""

	if err := s.versionRepo.Update(version); err != nil {
		return errs.ErrInternal
	}

	s.logger.Info("version shipped successfully", zap.String("version", version.VersionNumber))
	return nil
}

func (s *ShipService) preCheck(version *model.Version) error {
	var missing []string

	if version.ReleaseNotes == "" {
		missing = append(missing, "Release 说明")
	}
	if version.TargetCommitish == "" {
		missing = append(missing, "目标分支/Commit")
	}
	if len(version.Artifacts) == 0 {
		missing = append(missing, "安装包（至少上传一个）")
	}

	if len(missing) > 0 {
		return errs.New(errs.ErrShipPreCheckFailed.Code, fmt.Sprintf("发货校验未通过，缺少: %v", missing))
	}
	return nil
}

func (s *ShipService) recordError(version *model.Version, errMsg string) {
	version.ErrorLog = errMsg
	_ = s.versionRepo.Update(version)
}
