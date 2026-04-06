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

type ShipCheckItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type ShipCheckResponse struct {
	CanShip bool            `json:"can_ship"`
	Items   []ShipCheckItem `json:"items"`
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

func (s *ShipService) Check(versionID, userID string) (*ShipCheckResponse, error) {
	version, project, err := s.loadVersionAndProject(versionID, userID)
	if err != nil {
		return nil, err
	}

	check, err := s.buildCheck(context.Background(), version, project)
	if err != nil {
		return nil, err
	}

	return check, nil
}

func (s *ShipService) Ship(versionID, userID string) error {
	version, project, err := s.loadVersionAndProject(versionID, userID)
	if err != nil {
		return err
	}

	// 前置校验
	if err := s.updateShipState(version, model.ShipStatusInProgress, model.ShipStagePreCheck, "正在校验发货条件"); err != nil {
		return errs.ErrInternal
	}

	check, err := s.buildCheck(context.Background(), version, project)
	if err != nil {
		s.recordFailure(version, model.ShipStagePreCheck, err.Error())
		return err
	}
	if !check.CanShip {
		msg := errs.ErrShipPreCheckFailed.Message
		for _, item := range check.Items {
			if !item.OK && item.Detail != "" {
				msg = fmt.Sprintf("%s: %s", item.Label, item.Detail)
				break
			}
		}
		s.recordFailure(version, model.ShipStagePreCheck, msg)
		return errs.New(errs.ErrShipPreCheckFailed.Code, msg)
	}

	// 解密 GitHub Token
	tokenBytes, appErr := s.decryptGitHubToken(project)
	if appErr != nil {
		s.recordFailure(version, model.ShipStagePreCheck, appErr.Message)
		return appErr
	}

	gh := ghclient.NewClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	ctx := context.Background()

	// 创建 Tag
	_ = s.updateShipState(version, model.ShipStatusInProgress, model.ShipStageCreateTag, "正在创建 Git Tag")
	s.logger.Info("creating tag", zap.String("tag", version.VersionNumber))
	if err := gh.CreateTag(ctx, version.VersionNumber, version.TargetCommitish); err != nil {
		s.recordFailure(version, model.ShipStageCreateTag, fmt.Sprintf("创建 Tag 失败: %v", err))
		return errs.New(50200, fmt.Sprintf("创建 Tag 失败: %v", err))
	}

	// 创建 Release
	_ = s.updateShipState(version, model.ShipStatusInProgress, model.ShipStageCreateRelease, "正在创建 GitHub Release")
	s.logger.Info("creating release", zap.String("tag", version.VersionNumber))
	release, err := gh.CreateRelease(ctx, version.VersionNumber, version.VersionNumber, version.ReleaseNotes)
	if err != nil {
		s.recordFailure(version, model.ShipStageCreateRelease, fmt.Sprintf("创建 Release 失败: %v", err))
		return errs.New(50201, fmt.Sprintf("创建 Release 失败: %v", err))
	}

	// 并行上传安装包
	_ = s.updateShipState(version, model.ShipStatusInProgress, model.ShipStageUploadAssets, "正在上传安装包")
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
			s.recordFailure(version, model.ShipStageUploadAssets, fmt.Sprintf("上传安装包失败: %v", uerr))
			return errs.New(50202, fmt.Sprintf("上传安装包失败: %v", uerr))
		}
	}

	// 更新状态
	_ = s.updateShipState(version, model.ShipStatusInProgress, model.ShipStageFinalize, "正在更新版本状态")
	now := time.Now()
	version.Status = model.VersionStatusShipped
	version.ShippedAt = &now
	version.GithubReleaseURL = release.GetHTMLURL()
	version.ErrorLog = ""
	version.ShipStatus = model.ShipStatusCompleted
	version.ShipStage = model.ShipStageFinalize
	version.ShipMessage = "已成功发货到 GitHub"

	if err := s.versionRepo.Update(version); err != nil {
		return errs.ErrInternal
	}

	s.logger.Info("version shipped successfully", zap.String("version", version.VersionNumber))
	return nil
}

func (s *ShipService) loadVersionAndProject(versionID, userID string) (*model.Version, *model.Project, error) {
	version, err := s.versionRepo.FindByID(versionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrVersionNotFound
		}
		return nil, nil, errs.ErrInternal
	}

	if version.Status != model.VersionStatusPending {
		return nil, nil, errs.ErrVersionNotPending
	}

	project, err := s.projectRepo.FindByID(version.ProjectID, userID)
	if err != nil {
		return nil, nil, errs.ErrNotOwner
	}

	return version, project, nil
}

func (s *ShipService) buildCheck(ctx context.Context, version *model.Version, project *model.Project) (*ShipCheckResponse, error) {
	items := []ShipCheckItem{
		{
			Key:    "release_notes",
			Label:  "Release 说明",
			OK:     version.ReleaseNotes != "",
			Detail: "不能为空",
		},
		{
			Key:    "artifacts",
			Label:  "安装包",
			OK:     len(version.Artifacts) > 0,
			Detail: "至少上传一个安装包",
		},
		{
			Key:    "target_commitish",
			Label:  "目标分支 / Commit",
			OK:     version.TargetCommitish != "",
			Detail: "用于创建 Git Tag",
		},
	}

	githubItem := ShipCheckItem{
		Key:   "github_config",
		Label: "GitHub 配置",
		OK:    true,
	}

	switch {
	case project.GithubOwner == "" || project.GithubRepo == "":
		githubItem.OK = false
		githubItem.Detail = "缺少 GitHub 仓库配置"
	case len(project.GithubTokenEncrypted) == 0:
		githubItem.OK = false
		githubItem.Detail = "缺少 GitHub Token"
	default:
		tokenBytes, err := s.decryptGitHubToken(project)
		if err != nil {
			githubItem.OK = false
			githubItem.Detail = err.Message
		} else {
			gh := ghclient.NewClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
			if err := gh.ValidateRepository(ctx); err != nil {
				githubItem.OK = false
				githubItem.Detail = "无法访问 GitHub 仓库或 Token 无效"
			}
		}
	}
	items = append(items, githubItem)

	canShip := true
	for _, item := range items {
		if !item.OK {
			canShip = false
			break
		}
	}

	return &ShipCheckResponse{
		CanShip: canShip,
		Items:   items,
	}, nil
}

func (s *ShipService) decryptGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		s.logger.Error("decrypt github token failed", zap.Error(err))
		return nil, errs.ErrInternal
	}
	return tokenBytes, nil
}

func (s *ShipService) updateShipState(version *model.Version, status model.ShipStatus, stage model.ShipStage, message string) error {
	version.ShipStatus = status
	version.ShipStage = stage
	version.ShipMessage = message
	if status == model.ShipStatusInProgress {
		version.ErrorLog = ""
	}
	return s.versionRepo.Update(version)
}

func (s *ShipService) recordFailure(version *model.Version, stage model.ShipStage, errMsg string) {
	version.ErrorLog = errMsg
	version.ShipStatus = model.ShipStatusFailed
	version.ShipStage = stage
	version.ShipMessage = errMsg
	_ = s.versionRepo.Update(version)
}
