package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// 上传/编辑可写字段的长度上限（字符数），防止 API Key 写入超长元数据
const maxScreenshotTitleRunes = 200

// 维度过滤的「未标注」桶哨兵值：URL 与批量 filter 里的 NULL 语义
const screenshotDimensionNone = "__none__"

// 上传事务在唯一索引冲突/锁竞争下的最大重试次数（与 LogRepository.UploadRunTx 一致）
const maxUploadTxAttempts = 5

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
	// deleteScreenBeforeTx 是删除交错场景的测试注入点：删除事务开启前触发，
	// 模拟「权限校验读与删除事务之间」发生的并发覆盖上传；生产路径恒为 nil。
	deleteScreenBeforeTx func()
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

// ScreenshotUploadInput 是上传一张截图的入参；Platform/Locale/Theme 为指针
// 表达表单存在性（nil=表单未携带保持不变，指向空串=清除为未标注），
// Title 非空才覆盖。
type ScreenshotUploadInput struct {
	ProjectID  string
	UserID     string
	ScreenKey  string
	Title      string
	Platform   *string
	Locale     *string
	Theme      *string
	FileName   string
	UploadedBy string
	Reader     io.Reader
}

func normalizeScreenshotScreenKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// buildScreenshotStoragePath 生成存储相对路径。每次上传带独立文件段，
// 覆盖上传写新路径再切换 DB 指针，永不原地覆盖旧文件（读者不会看到半截文件，
// 事务失败删新文件即可，不碰旧文件）。
func buildScreenshotStoragePath(projectID, screenID, fileID, mimeType string) string {
	ext := extensionForMimeType(mimeType)
	return fmt.Sprintf("%s/screenshots/%s-%s%s", projectID, screenID, fileID, ext)
}

func extensionForMimeType(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

// buildScreenshotContentURL 生成截图内容地址：`?v=` 取 uploaded_at 纳秒值，
// 覆盖上传推进 uploaded_at 即换 URL，浏览器缓存的旧图（Cache-Control 按
// 完整 URL 记键）不再遮住新文件；画布与草稿也拿它当「文件已换」的指纹。
func buildScreenshotContentURL(screen *model.ScreenshotScreen) string {
	return fmt.Sprintf("/api/screenshot-screens/%s/content?v=%d", screen.ID, screen.UploadedAt.UnixNano())
}

func toScreenshotScreenResponse(screen *model.ScreenshotScreen) ScreenshotScreenResponse {
	var theme *api.ScreenshotScreenTheme
	if screen.Theme != nil {
		t := api.ScreenshotScreenTheme(*screen.Theme)
		theme = &t
	}
	return ScreenshotScreenResponse{
		Id:         screen.ID,
		ProjectId:  screen.ProjectID,
		ScreenKey:  screen.ScreenKey,
		Title:      screen.Title,
		Platform:   screen.Platform,
		Locale:     screen.Locale,
		Theme:      theme,
		MimeType:   screen.MimeType,
		FileSize:   screen.FileSize,
		Width:      screen.Width,
		Height:     screen.Height,
		UploadedBy: screen.UploadedBy,
		UploadedAt: api.JSONTime(screen.UploadedAt),
		CreatedAt:  api.JSONTime(screen.CreatedAt),
		ContentUrl: buildScreenshotContentURL(screen),
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

// loadAccessibleScreen 拉取截图并校验其项目归属当前用户；
// 项目不属于当前用户时按不存在处理，不暴露跨用户资源的存在性。
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

// storeScreenshotFile 按 issue_assets.go 的 LimitReader+1 计数模式把文件写到
// 临时路径：先读头部嗅探 mime，再把 head 拼回流里一次写完；超限文件写完即删。
// 正式路径在事务内确认 screen 后由 Rename 切换。
func (s *ScreenshotService) storeScreenshotFile(projectID string, reader io.Reader) (string, int64, string, error) {
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

	tempPath := fmt.Sprintf("%s/screenshots/tmp-%s%s", projectID, uuid.NewString(), extensionForMimeType(mimeType))
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(tempPath, countedReader); err != nil {
		_ = s.storage.Delete(tempPath)
		return "", 0, "", errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(tempPath)
		return "", 0, "", errs.ErrInvalidParams
	}

	return mimeType, countedReader.n, tempPath, nil
}

// probeScreenshotDims 重开已落盘文件解码头部取原图宽高（按嗅探出的 mime 分派解码器）；
// 解码失败不阻塞上传，返回 0,0 表示未知。
func (s *ScreenshotService) probeScreenshotDims(storagePath, mimeType string) (int, int) {
	reader, err := s.storage.Get(storagePath)
	if err != nil {
		return 0, 0
	}
	defer reader.Close()
	cfg, err := decodeScreenshotImageConfig(reader, mimeType)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// Upload 按 (project_id, screen_key, platform, locale, theme) 定位截图：
// 已存在即覆盖（替换文件、更新文件元数据，不新增行），不存在则新建。
// 文件先落临时路径，事务内确认 screen 后原子改名到正式路径；事务提交后删被替换的旧文件。
func (s *ScreenshotService) Upload(input *ScreenshotUploadInput) (*ScreenshotUploadResult, error) {
	screenKey := normalizeScreenshotScreenKey(input.ScreenKey)
	if n := utf8.RuneCountInString(screenKey); n < 1 || n > maxScreenshotScreenKeyRunes {
		return nil, errs.ErrInvalidParams
	}
	if input.Title != "" && utf8.RuneCountInString(input.Title) > maxScreenshotTitleRunes {
		return nil, errs.ErrInvalidParams
	}
	platform, err := normalizeScreenshotPlatform(input.Platform)
	if err != nil {
		return nil, err
	}
	locale, err := normalizeScreenshotLocale(input.Locale)
	if err != nil {
		return nil, err
	}
	theme, err := normalizeScreenshotTheme(input.Theme)
	if err != nil {
		return nil, err
	}
	if err := s.ensureProjectAccess(input.ProjectID, input.UserID); err != nil {
		return nil, err
	}

	mimeType, fileSize, tempPath, err := s.storeScreenshotFile(input.ProjectID, input.Reader)
	if err != nil {
		return nil, err
	}
	imgWidth, imgHeight := s.probeScreenshotDims(tempPath, mimeType)

	// 并发首传同一新变体时两边都会错过 FindScreenByVariant 并撞表达式唯一索引；
	// 多连接下整个事务还可能撞 SQLITE_LOCKED 死锁。失败的事务已整体回滚，
	// 按 UploadRunTx 的惯例做有界重试：对方提交后重查即命中已存在的截图并覆盖。
	var screen *model.ScreenshotScreen
	var replacedOldPath string
	var txErr error
	for attempt := 0; attempt < maxUploadTxAttempts; attempt++ {
		var renamedPath string
		screen, replacedOldPath, renamedPath, txErr = s.uploadScreenshotTx(input, screenKey, platform, locale, theme, tempPath, mimeType, fileSize, imgWidth, imgHeight)
		if txErr == nil {
			break
		}
		// 改名已发生但提交失败：新路径成为孤儿，删掉后临时文件已不在，整个上传终止
		if renamedPath != "" {
			_ = s.storage.Delete(renamedPath)
			break
		}
		if !isRetryableScreenshotTxError(txErr) || attempt == maxUploadTxAttempts-1 {
			_ = s.storage.Delete(tempPath)
			break
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	if txErr != nil {
		return nil, errs.ErrInternal
	}

	// 提交成功后再删被替换的旧文件；路径不同才有得删（新上传没有旧文件）。
	// 删除失败把路径写入待删队列，下次启动出队重试；文件已不存在视同清理成功。
	if replacedOldPath != "" {
		if err := s.storage.Delete(replacedOldPath); err != nil && !isAbsentScreenshotFile(err) {
			_ = s.screenshotRepo.EnqueuePendingDelete(replacedOldPath)
		}
	}

	return &ScreenshotUploadResult{
		Screen: toScreenshotScreenResponse(screen),
	}, nil
}

// isRetryableScreenshotTxError 识别值得整体重试的并发冲突：唯一索引违例
// （glebarez 的约束错误文本为 "UNIQUE constraint failed: ..."）与锁竞争
// （沿用 repository.isSQLiteLockedError 覆盖的 "database is locked" 系文本）。
func isRetryableScreenshotTxError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// isScreenshotVariantConflict 判定唯一索引违例是否来自变体唯一索引
// （PATCH 改维度撞既有组合时转 409，其余唯一错误仍按内部错误处理）。
func isScreenshotVariantConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") &&
		strings.Contains(msg, "screenshot_screens")
}

// uploadScreenshotTx 在一个事务里完成变体 find-or-create、文件元数据写入、
// open 标注的过期置位与文件切换。文件改名放在 DB 写之后、提交之前：
// DB 失败不会动磁盘，改名失败则事务回滚。内部直接透传驱动错误，由 Upload 判定重试。
// 返回被替换的旧文件路径（提交成功后由调用方删除）与已改名的新路径
// （仅当改名已发生；提交失败时调用方据此清理孤儿）。
func (s *ScreenshotService) uploadScreenshotTx(
	input *ScreenshotUploadInput,
	screenKey string,
	platform, locale, theme *string,
	tempPath, mimeType string,
	fileSize int64,
	imgWidth, imgHeight int,
) (*model.ScreenshotScreen, string, string, error) {
	var screen *model.ScreenshotScreen
	var replacedOldPath string
	var renamedPath string
	txErr := s.screenshotRepo.Transaction(func(txRepo *repository.ScreenshotRepository) error {
		now := time.Now().UTC()
		existing, findErr := txRepo.FindScreenByVariant(input.ProjectID, screenKey, platform, locale, theme)
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			screen = &model.ScreenshotScreen{
				ID:         uuid.NewString(),
				ProjectID:  input.ProjectID,
				ScreenKey:  screenKey,
				Platform:   platform,
				Locale:     locale,
				Theme:      theme,
				UploadedAt: now,
				CreatedAt:  now,
			}
			if input.Title != "" {
				screen.Title = input.Title
			}
			if err := txRepo.CreateScreen(screen); err != nil {
				return err
			}
		} else {
			screen = existing
			replacedOldPath = screen.FilePath
		}

		newPath := buildScreenshotStoragePath(input.ProjectID, screen.ID, uuid.NewString(), mimeType)
		fields := map[string]interface{}{
			"file_path":   newPath,
			"mime_type":   mimeType,
			"file_size":   fileSize,
			"width":       imgWidth,
			"height":      imgHeight,
			"uploaded_by": input.UploadedBy,
			"uploaded_at": now,
		}
		if input.Title != "" {
			fields["title"] = input.Title
		}
		if err := txRepo.UpdateScreenByMap(screen.ID, fields); err != nil {
			return err
		}
		// 同步内存结构，响应直接取自它
		screen.FilePath = newPath
		screen.MimeType = mimeType
		screen.FileSize = fileSize
		screen.Width = imgWidth
		screen.Height = imgHeight
		screen.UploadedBy = input.UploadedBy
		screen.UploadedAt = now
		if input.Title != "" {
			screen.Title = input.Title
		}

		// 覆盖后该截图上全部 open 标注视为过期，等待「确认保留」；resolved 不动
		if replacedOldPath != "" {
			if err := txRepo.MarkScreenAnnotationsOutdated(screen.ID); err != nil {
				return err
			}
		}

		if err := s.storage.Rename(tempPath, newPath); err != nil {
			return err
		}
		renamedPath = newPath
		return nil
	})
	if txErr != nil {
		return nil, "", renamedPath, txErr
	}
	return screen, replacedOldPath, renamedPath, nil
}

// List 返回项目全部截图（uploaded_at 倒序），支持维度过滤（多值 OR + 未标注桶）。
func (s *ScreenshotService) List(projectID, userID string, filters repository.ScreenshotScreenFilters) (*ScreenshotScreenListData, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}

	screens, err := s.screenshotRepo.ListScreensByProject(projectID, filters)
	if err != nil {
		return nil, errs.ErrInternal
	}

	items := make([]ScreenshotScreenResponse, 0, len(screens))
	for i := range screens {
		items = append(items, toScreenshotScreenResponse(&screens[i]))
	}
	return &ScreenshotScreenListData{Items: items}, nil
}

// Update 按 PATCH 指针语义更新 title 与三个变体维度：字段出现才写入
// （维度空串清除为未标注），全不传按参数无效处理；改出的组合撞变体唯一
// 索引转 409。
func (s *ScreenshotService) Update(screenID, userID string, req *UpdateScreenshotScreenRequest) (*ScreenshotScreenResponse, error) {
	if req.Title == nil && req.Platform == nil && req.Locale == nil && req.Theme == nil {
		return nil, errs.ErrInvalidParams
	}
	if req.Title != nil && utf8.RuneCountInString(*req.Title) > maxScreenshotTitleRunes {
		return nil, errs.ErrInvalidParams
	}
	platform, err := normalizeScreenshotPlatform(req.Platform)
	if err != nil {
		return nil, err
	}
	locale, err := normalizeScreenshotLocale(req.Locale)
	if err != nil {
		return nil, err
	}
	theme, err := normalizeScreenshotThemeRequest(req.Theme)
	if err != nil {
		return nil, err
	}
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return nil, err
	}

	fields := map[string]interface{}{}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Platform != nil {
		fields["platform"] = platform
	}
	if req.Locale != nil {
		fields["locale"] = locale
	}
	if req.Theme != nil {
		fields["theme"] = theme
	}
	if err := s.screenshotRepo.UpdateScreenByMap(screen.ID, fields); err != nil {
		if isScreenshotVariantConflict(err) {
			return nil, errs.ErrScreenshotVariantExists
		}
		return nil, errs.ErrInternal
	}
	if req.Title != nil {
		screen.Title = *req.Title
	}
	if req.Platform != nil {
		screen.Platform = platform
	}
	if req.Locale != nil {
		screen.Locale = locale
	}
	if req.Theme != nil {
		screen.Theme = theme
	}
	resp := toScreenshotScreenResponse(screen)
	return &resp, nil
}

// normalizeScreenshotThemeRequest 处理 PATCH 请求里的 theme 枚举值
// （生成类型为 enum 字符串）；空串清除为未标注。
func normalizeScreenshotThemeRequest(raw *api.UpdateScreenshotScreenRequestTheme) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := string(*raw)
	return normalizeScreenshotTheme(&v)
}

// DeleteScreen 删除截图行及其磁盘文件。文件路径在删除事务内重新读取：
// 权限校验的读与事务之间可能插入覆盖上传（文件已换新路径），按旧路径
// 清理会让新文件失去引用成为孤儿；事务提交后清理的必须是提交时仍被
// 引用的那条路径。
func (s *ScreenshotService) DeleteScreen(screenID, userID string) error {
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return err
	}

	if s.deleteScreenBeforeTx != nil {
		s.deleteScreenBeforeTx()
	}

	var deletedPath string
	if err := s.screenshotRepo.Transaction(func(txRepo *repository.ScreenshotRepository) error {
		current, err := txRepo.FindScreenByID(screen.ID)
		if err != nil {
			return err
		}
		deletedPath = current.FilePath
		return txRepo.DeleteScreen(current.ID)
	}); err != nil {
		return errs.ErrInternal
	}

	// 删除失败把路径写入待删队列，下次启动出队重试；文件已不存在视同
	// 清理成功——与覆盖上传的清理同一条路径，不留无引用的孤儿文件。
	if err := s.storage.Delete(deletedPath); err != nil && !isAbsentScreenshotFile(err) {
		_ = s.screenshotRepo.EnqueuePendingDelete(deletedPath)
	}
	return nil
}

// GetScreenContent 返回截图文件的读取器、存储的 mime 与大小，供 content 端点流式输出。
func (s *ScreenshotService) GetScreenContent(screenID, userID string) (io.ReadCloser, string, int64, error) {
	screen, err := s.loadAccessibleScreen(screenID, userID)
	if err != nil {
		return nil, "", 0, err
	}

	reader, err := s.storage.Get(screen.FilePath)
	if err != nil {
		return nil, "", 0, errs.ErrScreenshotScreenNotFound
	}
	return reader, screen.MimeType, screen.FileSize, nil
}
