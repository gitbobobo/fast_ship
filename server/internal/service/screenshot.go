package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// screen_key 规范化（ToLower+TrimSpace）后的长度上限（字符数）
const maxScreenshotScreenKeyRunes = 100

// 截图库只收位图格式；mime 以内容嗅探（http.DetectContentType）为准，不信任客户端声明
var screenshotAllowedMimeTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
	"image/gif":  {},
}

type ScreenshotService struct {
	screenshotRepo *repository.ScreenshotRepository
	projectRepo    *repository.ProjectRepository
	storage        storage.Storage
	cfg            *config.Config
}

func NewScreenshotService(
	screenshotRepo *repository.ScreenshotRepository,
	projectRepo *repository.ProjectRepository,
	storage storage.Storage,
	cfg *config.Config,
) *ScreenshotService {
	return &ScreenshotService{
		screenshotRepo: screenshotRepo,
		projectRepo:    projectRepo,
		storage:        storage,
		cfg:            cfg,
	}
}

// ScreenshotUploadInput 是上传一张截图的入参；Group 为指针表达表单存在性
// （nil=表单未携带保持不变，指向空串=置为未分组），Title 非空才覆盖。
type ScreenshotUploadInput struct {
	ProjectID  string
	UserID     string
	ScreenKey  string
	Group      *string
	Title      string
	Note       string
	FileName   string
	UploadedBy string
	Reader     io.Reader
}

func normalizeScreenshotScreenKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func buildScreenshotStoragePath(projectID, versionID, fileName, mimeType string) string {
	name := normalizeIssueAssetFileName(fileName, mimeType)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/screenshots/%s%s", projectID, versionID, ext)
}

func buildScreenshotContentURL(versionID string) string {
	return fmt.Sprintf("/api/screenshot-versions/%s/content", versionID)
}

func toScreenshotScreenResponse(screen *model.ScreenshotScreen) ScreenshotScreenResponse {
	return ScreenshotScreenResponse{
		Id:             screen.ID,
		ProjectId:      screen.ProjectID,
		ScreenKey:      screen.ScreenKey,
		Title:          screen.Title,
		Group:          screen.GroupName,
		VersionCount:   screen.VersionCount,
		LastUploadedAt: api.JSONTime(screen.LastUploadedAt),
		CreatedAt:      api.JSONTime(screen.CreatedAt),
	}
}

func toScreenshotVersionResponse(version *model.ScreenshotVersion) ScreenshotVersionResponse {
	return ScreenshotVersionResponse{
		Id:         version.ID,
		ScreenId:   version.ScreenID,
		Note:       version.Note,
		FileName:   version.FileName,
		FileSize:   version.FileSize,
		MimeType:   version.MimeType,
		UploadedBy: version.UploadedBy,
		UploadedAt: api.JSONTime(version.UploadedAt),
		ContentUrl: buildScreenshotContentURL(version.ID),
	}
}

func (s *ScreenshotService) ensureProjectAccess(projectID, userID string) error {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

// loadAccessibleScreen 拉取 screen 并校验其项目归属当前用户；
// 项目不属于当前用户时按界面不存在处理，不暴露跨用户资源的存在性。
func (s *ScreenshotService) loadAccessibleScreen(screenID, userID string) (*model.ScreenshotScreen, error) {
	screen, err := s.screenshotRepo.FindScreenByID(screenID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrScreenshotScreenNotFound
		}
		return nil, errs.ErrInternal
	}
	if err := s.ensureProjectAccess(screen.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, errs.ErrScreenshotScreenNotFound
		}
		return nil, err
	}
	return screen, nil
}

func (s *ScreenshotService) loadAccessibleVersion(versionID, userID string) (*model.ScreenshotVersion, *model.ScreenshotScreen, error) {
	version, err := s.screenshotRepo.FindVersionByID(versionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrScreenshotVersionNotFound
		}
		return nil, nil, errs.ErrInternal
	}
	screen, err := s.screenshotRepo.FindScreenByID(version.ScreenID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrScreenshotVersionNotFound
		}
		return nil, nil, errs.ErrInternal
	}
	if err := s.ensureProjectAccess(screen.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, nil, errs.ErrScreenshotVersionNotFound
		}
		return nil, nil, err
	}
	return version, screen, nil
}

// storeScreenshotVersionFile 按 issue_assets.go 的 LimitReader+1 计数模式落盘：
// 先读头部嗅探 mime，再把 head 拼回流里一次写完；超限文件写完即删。
func (s *ScreenshotService) storeScreenshotVersionFile(projectID, versionID, fileName string, reader io.Reader) (string, int64, string, error) {
	readFrom := reader
	if s.cfg.Upload.MaxFileSize > 0 {
		readFrom = io.LimitReader(reader, s.cfg.Upload.MaxFileSize+1)
	}

	head := make([]byte, issueAssetSniffBytes)
	headSize, err := io.ReadFull(readFrom, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", 0, "", errs.ErrInternal
	}
	head = head[:headSize]
	if len(head) == 0 {
		return "", 0, "", errs.ErrInvalidParams
	}

	mimeType := http.DetectContentType(head)
	if _, ok := screenshotAllowedMimeTypes[mimeType]; !ok {
		return "", 0, "", errs.ErrInvalidParams
	}

	storagePath := buildScreenshotStoragePath(projectID, versionID, fileName, mimeType)
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(storagePath, countedReader); err != nil {
		_ = s.storage.Delete(storagePath)
		return "", 0, "", errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(storagePath)
		return "", 0, "", errs.ErrInvalidParams
	}

	return mimeType, countedReader.n, storagePath, nil
}

// Upload 归并到 (project_id, screen_key) 对应的 screen：不存在则自动建；
// 版本全量追加，screen 上的 group/title 按表单语义顺带更新。
func (s *ScreenshotService) Upload(input *ScreenshotUploadInput) (*ScreenshotUploadResult, error) {
	screenKey := normalizeScreenshotScreenKey(input.ScreenKey)
	if n := utf8.RuneCountInString(screenKey); n < 1 || n > maxScreenshotScreenKeyRunes {
		return nil, errs.ErrInvalidParams
	}
	if err := s.ensureProjectAccess(input.ProjectID, input.UserID); err != nil {
		return nil, err
	}

	versionID := uuid.NewString()
	mimeType, fileSize, storagePath, err := s.storeScreenshotVersionFile(input.ProjectID, versionID, input.FileName, input.Reader)
	if err != nil {
		return nil, err
	}

	var screen *model.ScreenshotScreen
	var version *model.ScreenshotVersion
	txErr := s.screenshotRepo.Transaction(func(txRepo *repository.ScreenshotRepository) error {
		now := time.Now().UTC()
		existing, findErr := txRepo.FindScreenByKey(input.ProjectID, screenKey)
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return errs.ErrInternal
		}
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			screen = &model.ScreenshotScreen{
				ID:             uuid.NewString(),
				ProjectID:      input.ProjectID,
				ScreenKey:      screenKey,
				LastUploadedAt: now,
				CreatedAt:      now,
			}
			if err := txRepo.CreateScreen(screen); err != nil {
				return errs.ErrInternal
			}
		} else {
			screen = existing
		}

		version = &model.ScreenshotVersion{
			ID:         versionID,
			ScreenID:   screen.ID,
			FileName:   normalizeIssueAssetFileName(input.FileName, mimeType),
			FilePath:   storagePath,
			MimeType:   mimeType,
			FileSize:   fileSize,
			Note:       input.Note,
			UploadedBy: input.UploadedBy,
			UploadedAt: now,
		}
		if err := txRepo.CreateVersion(version); err != nil {
			return errs.ErrInternal
		}

		count, err := txRepo.CountVersionsByScreenID(screen.ID)
		if err != nil {
			return errs.ErrInternal
		}
		fields := map[string]interface{}{
			"version_count":    count,
			"last_uploaded_at": version.UploadedAt,
		}
		if input.Group != nil {
			fields["group_name"] = *input.Group
		}
		if input.Title != "" {
			fields["title"] = input.Title
		}
		if err := txRepo.UpdateScreenByMap(screen.ID, fields); err != nil {
			return errs.ErrInternal
		}

		screen.VersionCount = int(count)
		screen.LastUploadedAt = version.UploadedAt
		if input.Group != nil {
			screen.GroupName = *input.Group
		}
		if input.Title != "" {
			screen.Title = input.Title
		}
		return nil
	})
	if txErr != nil {
		_ = s.storage.Delete(storagePath)
		return nil, txErr
	}

	return &ScreenshotUploadResult{
		Screen:  toScreenshotScreenResponse(screen),
		Version: toScreenshotVersionResponse(version),
	}, nil
}

// List 返回项目全部 screen（last_uploaded_at 倒序），每项携带最新版本。
func (s *ScreenshotService) List(projectID, userID string) (*ScreenshotScreenListData, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}

	screens, err := s.screenshotRepo.ListScreensByProject(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	screenIDs := make([]string, 0, len(screens))
	for _, screen := range screens {
		screenIDs = append(screenIDs, screen.ID)
	}
	latestByScreen, err := s.screenshotRepo.LatestVersionsByScreenIDs(screenIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	items := make([]ScreenshotScreenListItem, 0, len(screens))
	for i := range screens {
		base := toScreenshotScreenResponse(&screens[i])
		item := ScreenshotScreenListItem{
			Id:             base.Id,
			ProjectId:      base.ProjectId,
			ScreenKey:      base.ScreenKey,
			Title:          base.Title,
			Group:          base.Group,
			VersionCount:   base.VersionCount,
			LastUploadedAt: base.LastUploadedAt,
			CreatedAt:      base.CreatedAt,
		}
		if latest, ok := latestByScreen[screens[i].ID]; ok {
			resp := toScreenshotVersionResponse(&latest)
			item.LatestVersion = &resp
		}
		items = append(items, item)
	}

	return &ScreenshotScreenListData{Items: items}, nil
}

// Get 返回 screen 详情，versions 按 uploaded_at 倒序全量带出。
func (s *ScreenshotService) Get(screenID, userID string) (*ScreenshotScreenDetail, error) {
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return nil, err
	}
	return s.buildScreenDetail(screen)
}

func (s *ScreenshotService) buildScreenDetail(screen *model.ScreenshotScreen) (*ScreenshotScreenDetail, error) {
	versions, err := s.screenshotRepo.ListVersionsByScreenID(screen.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	base := toScreenshotScreenResponse(screen)
	resp := ScreenshotScreenDetail{
		Id:             base.Id,
		ProjectId:      base.ProjectId,
		ScreenKey:      base.ScreenKey,
		Title:          base.Title,
		Group:          base.Group,
		VersionCount:   base.VersionCount,
		LastUploadedAt: base.LastUploadedAt,
		CreatedAt:      base.CreatedAt,
		Versions:       make([]ScreenshotVersionResponse, 0, len(versions)),
	}
	for i := range versions {
		resp.Versions = append(resp.Versions, toScreenshotVersionResponse(&versions[i]))
	}
	return &resp, nil
}

// Update 按 PATCH 指针语义更新 group/title：字段出现才写入（group 允许空串置为未分组），
// 两者都不传按参数无效处理。
func (s *ScreenshotService) Update(screenID, userID string, req *UpdateScreenshotScreenRequest) (*ScreenshotScreenDetail, error) {
	if req.Group == nil && req.Title == nil {
		return nil, errs.ErrInvalidParams
	}
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return nil, err
	}

	fields := map[string]interface{}{}
	if req.Group != nil {
		fields["group_name"] = *req.Group
	}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if err := s.screenshotRepo.UpdateScreenByMap(screen.ID, fields); err != nil {
		return nil, errs.ErrInternal
	}
	if req.Group != nil {
		screen.GroupName = *req.Group
	}
	if req.Title != nil {
		screen.Title = *req.Title
	}

	return s.buildScreenDetail(screen)
}

// DeleteScreen 删除 screen 及其全部版本行与磁盘文件；版本文件路径在删除事务内收集，
// 避免事务间隙提交的新版本被删行却漏删文件；文件删除在事务提交后做。
func (s *ScreenshotService) DeleteScreen(screenID, userID string) error {
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return err
	}

	var paths []string
	if err := s.screenshotRepo.Transaction(func(txRepo *repository.ScreenshotRepository) error {
		versions, err := txRepo.ListVersionsByScreenID(screen.ID)
		if err != nil {
			return errs.ErrInternal
		}
		paths = make([]string, 0, len(versions))
		for _, version := range versions {
			paths = append(paths, version.FilePath)
		}
		if err := txRepo.DeleteVersionsByScreenID(screen.ID); err != nil {
			return errs.ErrInternal
		}
		if err := txRepo.DeleteScreen(screen.ID); err != nil {
			return errs.ErrInternal
		}
		return nil
	}); err != nil {
		return err
	}

	for _, path := range paths {
		_ = s.storage.Delete(path)
	}
	return nil
}

// DeleteVersion 删除单个版本及其文件；删到最后一个版本时连带删除 screen。
func (s *ScreenshotService) DeleteVersion(versionID, userID string) error {
	version, screen, err := s.loadAccessibleVersion(versionID, userID)
	if err != nil {
		return err
	}

	if err := s.screenshotRepo.Transaction(func(txRepo *repository.ScreenshotRepository) error {
		if err := txRepo.DeleteVersion(version.ID); err != nil {
			return errs.ErrInternal
		}
		count, err := txRepo.CountVersionsByScreenID(screen.ID)
		if err != nil {
			return errs.ErrInternal
		}
		if count == 0 {
			if err := txRepo.DeleteScreen(screen.ID); err != nil {
				return errs.ErrInternal
			}
			return nil
		}
		latest, err := txRepo.FindLatestVersionByScreenID(screen.ID)
		if err != nil {
			return errs.ErrInternal
		}
		if err := txRepo.UpdateScreenByMap(screen.ID, map[string]interface{}{
			"version_count":    count,
			"last_uploaded_at": latest.UploadedAt,
		}); err != nil {
			return errs.ErrInternal
		}
		return nil
	}); err != nil {
		return err
	}

	_ = s.storage.Delete(version.FilePath)
	return nil
}

// GetVersionContent 返回版本文件的读取器、存储的 mime 与大小，供 content 端点流式输出。
func (s *ScreenshotService) GetVersionContent(versionID, userID string) (io.ReadCloser, string, int64, error) {
	version, _, err := s.loadAccessibleVersion(versionID, userID)
	if err != nil {
		return nil, "", 0, err
	}

	reader, err := s.storage.Get(version.FilePath)
	if err != nil {
		return nil, "", 0, errs.ErrScreenshotVersionNotFound
	}
	return reader, version.MimeType, version.FileSize, nil
}
