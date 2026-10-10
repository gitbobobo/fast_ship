package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/image/webp"
	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"
)

// 标注状态值，与 api.ScreenshotAnnotationStatus 枚举一致
const (
	screenshotAnnotationStatusOpen     = "open"
	screenshotAnnotationStatusResolved = "resolved"
)

// 标注文字长度上限（字符数）；坐标校验用的浮点容差
const (
	maxScreenshotAnnotationBodyRunes = 1000
	annotationCoordEpsilon           = 1e-6
)

// 裁剪外边距：标注矩形短边的 15%，最小 8px
const (
	annotationCropMarginRatio = 0.15
	annotationCropMarginMinPx = 8
)

// 裁剪解码的像素上限（约 64MP，8K 截图约 33MP）：压缩体积上限管不住
// 解码后内存，一张声明 30000×30000 的 PNG 解码要 ~3.6GB
const maxScreenshotDecodePixels = 64_000_000

// 裁剪并发闸：个数上限 4，且按图像素数加权限总驻留内存——RGBA 解码源 +
// PNG 输出缓冲各按 w*h*4 估算（权 = 像素×8），总量封顶 512MiB。小截图并行、
// 单张 64MP 大图独占排队，4 个并发大裁剪不会叠出 ~2GiB。
var (
	screenshotCropDecodeSem = make(chan struct{}, 4)
	screenshotCropBytesSem  = semaphore.NewWeighted(512 << 20)
)

// ScreenshotAnnotationListFilters 是项目级标注列表的可选过滤项，空值不参与过滤。
type ScreenshotAnnotationListFilters struct {
	Status   string
	IssueID  string
	ScreenID string
}

// ScreenshotAnnotationService 承载截图标注的读写与裁剪；展示组装（含屏幕/版本/Issue
// 上下文与像素换算）与本包内 IssueService.Get 共用同一组函数。
type ScreenshotAnnotationService struct {
	annotationRepo *repository.ScreenshotAnnotationRepository
	screenshotRepo *repository.ScreenshotRepository
	issueRepo      *repository.IssueRepository
	projectRepo    *repository.ProjectRepository
	storage        storage.Storage
}

func NewScreenshotAnnotationService(
	annotationRepo *repository.ScreenshotAnnotationRepository,
	screenshotRepo *repository.ScreenshotRepository,
	issueRepo *repository.IssueRepository,
	projectRepo *repository.ProjectRepository,
	storage storage.Storage,
) *ScreenshotAnnotationService {
	return &ScreenshotAnnotationService{
		annotationRepo: annotationRepo,
		screenshotRepo: screenshotRepo,
		issueRepo:      issueRepo,
		projectRepo:    projectRepo,
		storage:        storage,
	}
}

func (s *ScreenshotAnnotationService) ensureProjectAccess(projectID, userID string) error {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

// loadAccessibleAnnotation 拉取标注并校验其项目归属当前用户；
// 项目不属于当前用户时按标注不存在处理，不暴露跨用户资源的存在性。
func (s *ScreenshotAnnotationService) loadAccessibleAnnotation(annotationID, userID string) (*model.ScreenshotAnnotation, error) {
	annotation, err := s.annotationRepo.FindByID(annotationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrScreenshotAnnotationNotFound
		}
		return nil, errs.ErrInternal
	}
	if err := s.ensureProjectAccess(annotation.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, errs.ErrScreenshotAnnotationNotFound
		}
		return nil, err
	}
	return annotation, nil
}

// loadAccessibleVersion 与 ScreenshotService 同思路：版本不存在、界面缺失或
// 跨用户统一按版本不存在返回（标注创建/裁剪的父资源是版本，错误码用 40414）。
func (s *ScreenshotAnnotationService) loadAccessibleVersion(versionID, userID string) (*model.ScreenshotVersion, *model.ScreenshotScreen, error) {
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

// List 返回项目全部标注（可选按 status/issue_id/screen_id 过滤），按创建先后升序。
func (s *ScreenshotAnnotationService) List(projectID, userID string, filters ScreenshotAnnotationListFilters) (*ScreenshotAnnotationListData, error) {
	if filters.Status != "" && !api.ScreenshotAnnotationStatus(filters.Status).Valid() {
		return nil, errs.ErrInvalidParams
	}
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}

	annotations, err := s.annotationRepo.ListByProject(projectID, repository.ScreenshotAnnotationFilters{
		Status:   filters.Status,
		IssueID:  filters.IssueID,
		ScreenID: filters.ScreenID,
	})
	if err != nil {
		return nil, errs.ErrInternal
	}
	items, err := s.buildAnnotationResponses(annotations)
	if err != nil {
		return nil, err
	}
	return &ScreenshotAnnotationListData{Items: items}, nil
}

// Create 在版本上创建标注；坐标为图片宽高比例，issue_id 缺省/空串 = 不关联。
// 坐标字段是指针：缺省/null 与显式 0 区分开，缺省按 40001 拒绝而不是静默落左上角。
func (s *ScreenshotAnnotationService) Create(versionID, userID, createdBy string, req *CreateScreenshotAnnotationRequest) (*ScreenshotAnnotationResponse, error) {
	if req.X == nil || req.Y == nil || req.Width == nil || req.Height == nil ||
		!validAnnotationRect(float64(*req.X), float64(*req.Y), float64(*req.Width), float64(*req.Height)) {
		return nil, errs.ErrInvalidParams
	}
	if n := utf8.RuneCountInString(req.Body); n < 1 || n > maxScreenshotAnnotationBodyRunes {
		return nil, errs.ErrInvalidParams
	}

	version, screen, err := s.loadAccessibleVersion(versionID, userID)
	if err != nil {
		return nil, err
	}

	issueID, err := s.resolveIssueID(req.IssueId, screen.ProjectID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	annotation := &model.ScreenshotAnnotation{
		ID:        uuid.NewString(),
		VersionID: version.ID,
		ScreenID:  screen.ID,
		ProjectID: screen.ProjectID,
		IssueID:   issueID,
		X:         float64(*req.X),
		Y:         float64(*req.Y),
		Width:     float64(*req.Width),
		Height:    float64(*req.Height),
		Body:      req.Body,
		Status:    screenshotAnnotationStatusOpen,
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.annotationRepo.Create(annotation); err != nil {
		return nil, errs.ErrInternal
	}
	return s.respondWithAnnotation(annotation)
}

// Update 按指针语义更新 body/status/issue_id。API Key 只允许整包
// {"status":"resolved"}——出现其他字段或要求重开（status=open）返回 40301。
func (s *ScreenshotAnnotationService) Update(annotationID, userID string, req *UpdateScreenshotAnnotationRequest, isAPIKey bool) (*ScreenshotAnnotationResponse, error) {
	if req.Body == nil && req.Status == nil && req.IssueId == nil {
		return nil, errs.ErrInvalidParams
	}
	if isAPIKey {
		if req.Body != nil || req.IssueId != nil || req.Status == nil ||
			*req.Status != api.Resolved {
			return nil, errs.ErrApiKeyForbidden
		}
	}
	if req.Body != nil {
		if n := utf8.RuneCountInString(*req.Body); n < 1 || n > maxScreenshotAnnotationBodyRunes {
			return nil, errs.ErrInvalidParams
		}
	}
	if req.Status != nil && !req.Status.Valid() {
		return nil, errs.ErrInvalidParams
	}

	annotation, err := s.loadAccessibleAnnotation(annotationID, userID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	fields := map[string]interface{}{
		"updated_at": now,
	}
	annotation.UpdatedAt = now
	if req.Body != nil {
		fields["body"] = *req.Body
		annotation.Body = *req.Body
	}
	if req.Status != nil {
		fields["status"] = string(*req.Status)
		annotation.Status = string(*req.Status)
		if *req.Status == api.Resolved {
			fields["resolved_at"] = now
			annotation.ResolvedAt = &now
		} else {
			fields["resolved_at"] = nil
			annotation.ResolvedAt = nil
		}
	}
	if req.IssueId != nil {
		issueID, err := s.resolveIssueID(req.IssueId, annotation.ProjectID)
		if err != nil {
			return nil, err
		}
		fields["issue_id"] = issueID
		annotation.IssueID = issueID
	}

	if err := s.annotationRepo.UpdateByMap(annotation.ID, fields); err != nil {
		return nil, errs.ErrInternal
	}
	return s.respondWithAnnotation(annotation)
}

// resolveIssueID 校验 issue_id 关联：nil 或空串 = 不关联；非空须为同项目内 Issue。
// 返回待写入列值（*string，nil 表示 NULL）。
func (s *ScreenshotAnnotationService) resolveIssueID(raw *string, projectID string) (*string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	issue, err := s.issueRepo.FindByID(*raw)
	if err != nil || issue.ProjectID != projectID {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		return nil, errs.ErrInvalidParams
	}
	return raw, nil
}

// Delete 删除标注行；文件无关，FK 兜底版本/界面/项目级联。
func (s *ScreenshotAnnotationService) Delete(annotationID, userID string) error {
	annotation, err := s.loadAccessibleAnnotation(annotationID, userID)
	if err != nil {
		return err
	}
	if err := s.annotationRepo.Delete(annotation.ID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// Crop 按标注比例裁出图片区域：矩形 = 比例 × 原图像素，外边距为矩形短边
// 15%（最小 8px，不超出图片边界），统一输出 PNG。
// 成功时返回的 release 必须在调用方把 PNG 写完响应后再调用——解码信号量同时
// 覆盖已编码缓冲的驻留期，慢客户端不会绕过并发上限堆积大响应体。
func (s *ScreenshotAnnotationService) Crop(ctx context.Context, annotationID, userID string) (*bytes.Reader, func(), error) {
	annotation, err := s.loadAccessibleAnnotation(annotationID, userID)
	if err != nil {
		return nil, nil, err
	}
	version, err := s.screenshotRepo.FindVersionByID(annotation.VersionID)
	if err != nil {
		return nil, nil, errs.ErrScreenshotAnnotationNotFound
	}

	// 解码前按文件头尺寸做像素预算校验；头部都读不出来时像素解码同样会失败
	imgW, imgH := resolveScreenshotVersionDims(s.storage, version)
	if imgW <= 0 || imgH <= 0 {
		return nil, nil, errs.ErrInternal
	}
	if int64(imgW)*int64(imgH) > maxScreenshotDecodePixels {
		return nil, nil, errs.ErrScreenshotImageTooLarge
	}

	reader, err := s.storage.Get(version.FilePath)
	if err != nil {
		return nil, nil, errs.ErrScreenshotAnnotationNotFound
	}
	defer reader.Close()

	// 个数闸 + 字节闸都占住才算拿到裁剪权，字节权覆盖解码与输出缓冲驻留期；
	// 两道排队都随请求 ctx 取消，客户端断连不再占队
	select {
	case screenshotCropDecodeSem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, errs.ErrInternal
	}
	weight := int64(imgW) * int64(imgH) * 8
	if err := screenshotCropBytesSem.Acquire(ctx, weight); err != nil {
		<-screenshotCropDecodeSem
		return nil, nil, errs.ErrInternal
	}
	release := func() {
		screenshotCropBytesSem.Release(weight)
		<-screenshotCropDecodeSem
	}

	src, err := s.decodeCropSource(reader, version)
	if err != nil {
		release()
		return nil, nil, errs.ErrInternal
	}

	bounds := src.Bounds()
	rect := annotationPixelRect(annotation, bounds.Dx(), bounds.Dy())
	margin := annotationCropMargin(rect)
	crop := image.Rect(
		rect.Min.X-margin, rect.Min.Y-margin,
		rect.Max.X+margin, rect.Max.Y+margin,
	).Intersect(bounds)

	cropped := cropScreenshot(src, crop)
	var buf bytes.Buffer
	if err := png.Encode(&buf, cropped); err != nil {
		release()
		return nil, nil, errs.ErrInternal
	}
	return bytes.NewReader(buf.Bytes()), release, nil
}

// respondWithAnnotation 组装单条标注响应（Create/Update 的返回体）。
func (s *ScreenshotAnnotationService) respondWithAnnotation(annotation *model.ScreenshotAnnotation) (*ScreenshotAnnotationResponse, error) {
	items, err := s.buildAnnotationResponses([]model.ScreenshotAnnotation{*annotation})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errs.ErrInternal
	}
	return &items[0], nil
}

// buildAnnotationResponses 批量组装标注响应：关联行经 LoadRelated 一次取回，
// issue 信息按 issue_id 集批量查 issues 表（Preload GitHubMeta 供 reference 生成）。
func (s *ScreenshotAnnotationService) buildAnnotationResponses(annotations []model.ScreenshotAnnotation) ([]api.ScreenshotAnnotation, error) {
	related, err := s.annotationRepo.LoadRelated(annotations)
	if err != nil {
		return nil, errs.ErrInternal
	}
	issues, err := s.loadAnnotationIssues(annotations)
	if err != nil {
		return nil, err
	}
	return assembleScreenshotAnnotations(annotations, related, issues, s.versionDims), nil
}

func (s *ScreenshotAnnotationService) loadAnnotationIssues(annotations []model.ScreenshotAnnotation) (map[string]model.Issue, error) {
	issueIDs := make([]string, 0, len(annotations))
	seen := make(map[string]struct{}, len(annotations))
	for _, a := range annotations {
		if a.IssueID == nil {
			continue
		}
		if _, ok := seen[*a.IssueID]; !ok {
			seen[*a.IssueID] = struct{}{}
			issueIDs = append(issueIDs, *a.IssueID)
		}
	}
	issues, err := s.issueRepo.ListByIDs(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return issues, nil
}

// versionDims 返回版本原图宽高：优先用落库值；存量行（width=0）惰性解码
// 文件头探测，仍失败返回 0,0（调用方据此输出 pixel_rect=null）。
func (s *ScreenshotAnnotationService) versionDims(version *model.ScreenshotVersion) (int, int) {
	return resolveScreenshotVersionDims(s.storage, version)
}

// resolveScreenshotVersionDims 是版本尺寸的共享解析：记录值优先，
// 未记录时按存储 mime 解码文件头；解码失败不报错，返回 0,0。
func resolveScreenshotVersionDims(st storage.Storage, version *model.ScreenshotVersion) (int, int) {
	if version == nil {
		return 0, 0
	}
	if version.Width > 0 && version.Height > 0 {
		return version.Width, version.Height
	}
	reader, err := st.Get(version.FilePath)
	if err != nil {
		return 0, 0
	}
	defer reader.Close()
	cfg, err := decodeScreenshotImageConfig(reader, version.MimeType)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// assembleScreenshotAnnotations 把标注行组装为契约响应；IssueService.Get
// 内嵌 screenshot_annotations 时共用此函数保证字段口径一致。
// related 提供版本/界面/最新版本；issues 提供 issue_id → Issue（含 GitHubMeta）。
func assembleScreenshotAnnotations(
	annotations []model.ScreenshotAnnotation,
	related *repository.ScreenshotAnnotationRelated,
	issues map[string]model.Issue,
	dimsOf func(*model.ScreenshotVersion) (int, int),
) []api.ScreenshotAnnotation {
	items := make([]api.ScreenshotAnnotation, 0, len(annotations))
	dimsCache := make(map[string][2]int, len(annotations))
	for i := range annotations {
		a := &annotations[i]
		item := api.ScreenshotAnnotation{
			Id:         a.ID,
			ProjectId:  a.ProjectID,
			ScreenId:   a.ScreenID,
			VersionId:  a.VersionID,
			X:          float32(a.X),
			Y:          float32(a.Y),
			Width:      float32(a.Width),
			Height:     float32(a.Height),
			Body:       a.Body,
			Status:     api.ScreenshotAnnotationStatus(a.Status),
			CropUrl:    fmt.Sprintf("/api/screenshot-annotations/%s/crop", a.ID),
			ImageUrl:   buildScreenshotContentURL(a.VersionID),
			CreatedBy:  a.CreatedBy,
			CreatedAt:  api.JSONTime(a.CreatedAt),
			UpdatedAt:  api.JSONTime(a.UpdatedAt),
			ResolvedAt: api.JSONTimePtr(a.ResolvedAt),
		}
		if screen, ok := related.Screens[a.ScreenID]; ok {
			item.ScreenKey = screen.ScreenKey
			item.ScreenTitle = screen.Title
			item.ScreenGroup = screen.GroupName
		}
		if latest, ok := related.Latest[a.ScreenID]; ok {
			item.IsLatestVersion = latest.ID == a.VersionID
		}
		if version, ok := related.Versions[a.VersionID]; ok {
			version := version
			dims, hit := dimsCache[version.ID]
			if !hit {
				w, h := dimsOf(&version)
				dims = [2]int{w, h}
				dimsCache[version.ID] = dims
			}
			item.ImageWidth = dims[0]
			item.ImageHeight = dims[1]
			if dims[0] > 0 && dims[1] > 0 {
				rect := annotationPixelRect(a, dims[0], dims[1])
				item.PixelRect = &api.ScreenshotAnnotationRect{
					X:      rect.Min.X,
					Y:      rect.Min.Y,
					Width:  rect.Dx(),
					Height: rect.Dy(),
				}
			}
		}
		if a.IssueID != nil {
			item.IssueId = a.IssueID
			if issue, ok := issues[*a.IssueID]; ok {
				ref := buildIssueReference(issue)
				item.IssueReference = &ref
				title := issue.Title
				item.IssueTitle = &title
			}
		}
		items = append(items, item)
	}
	return items
}

// validAnnotationRect 校验比例矩形：0 ≤ x,y、0 < w,h、x+w ≤ 1、y+h ≤ 1。
func validAnnotationRect(x, y, w, h float64) bool {
	if x < 0 || y < 0 || x > 1 || y > 1 {
		return false
	}
	if w <= 0 || h <= 0 || w > 1 || h > 1 {
		return false
	}
	return x+w <= 1+annotationCoordEpsilon && y+h <= 1+annotationCoordEpsilon
}

// annotationPixelRect 把比例矩形换算为原图坐标系像素矩形：四边缘四舍五入；
// 亚像素矩形塌成空矩形时至少保留 1px 且不丢位置，最后收进图片边界。
func annotationPixelRect(a *model.ScreenshotAnnotation, imgW, imgH int) image.Rectangle {
	x1 := int(math.Round(a.X * float64(imgW)))
	y1 := int(math.Round(a.Y * float64(imgH)))
	x2 := int(math.Round((a.X + a.Width) * float64(imgW)))
	y2 := int(math.Round((a.Y + a.Height) * float64(imgH)))
	if x2 <= x1 {
		x2 = x1 + 1
	}
	if y2 <= y1 {
		y2 = y1 + 1
	}
	if x2 > imgW {
		x2 = imgW
		x1 = imgW - 1
	}
	if y2 > imgH {
		y2 = imgH
		y1 = imgH - 1
	}
	return image.Rect(x1, y1, x2, y2).Intersect(image.Rect(0, 0, imgW, imgH))
}

// annotationCropMargin 计算裁剪外边距：矩形短边 15%，最小 8px。
func annotationCropMargin(rect image.Rectangle) int {
	short := rect.Dx()
	if rect.Dy() < short {
		short = rect.Dy()
	}
	margin := int(math.Round(float64(short) * annotationCropMarginRatio))
	if margin < annotationCropMarginMinPx {
		margin = annotationCropMarginMinPx
	}
	return margin
}

// decodeCropSource 解码截图像素。jpeg 直接流式解码后再重开一遍流扫 EXIF 头；
// gif 先用 DecodeConfig 取逻辑画布、再重开流 Decode 第一帧——两遍都走流，不
// ReadAll 整文件，大元数据（GIF 注释/应用扩展、JPEG ICC 段）不会驻留内存。
func (s *ScreenshotAnnotationService) decodeCropSource(reader io.Reader, version *model.ScreenshotVersion) (image.Image, error) {
	switch version.MimeType {
	case "image/jpeg":
		img, err := jpeg.Decode(reader)
		if err != nil {
			return nil, err
		}
		r2, err := s.storage.Get(version.FilePath)
		if err != nil {
			return nil, err
		}
		defer r2.Close()
		_, orientation, err := jpegScanHeader(r2)
		if err != nil {
			orientation = 0
		}
		return applyJPEGExifOrientation(img, orientation), nil
	case "image/gif":
		cfg, err := gif.DecodeConfig(reader)
		if err != nil {
			return nil, err
		}
		r2, err := s.storage.Get(version.FilePath)
		if err != nil {
			return nil, err
		}
		defer r2.Close()
		frame, err := gif.Decode(r2)
		if err != nil {
			return nil, err
		}
		canvasRect := image.Rect(0, 0, cfg.Width, cfg.Height)
		if frame.Bounds().Eq(canvasRect) {
			return frame, nil
		}
		canvas := image.NewRGBA(canvasRect)
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		return canvas, nil
	default:
		return decodeScreenshotImage(reader, version.MimeType)
	}
}

// decodeScreenshotImage 按存储 mime 流式解码原图：png 走标准库，webp 走
// x/image；jpeg/gif 由 decodeCropSource 两遍流处理，不经此函数。
func decodeScreenshotImage(r io.Reader, mimeType string) (image.Image, error) {
	switch mimeType {
	case "image/png":
		return png.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	default:
		return nil, fmt.Errorf("unsupported screenshot mime type %q", mimeType)
	}
}

// decodeScreenshotImageConfig 与 decodeScreenshotImage 同分派，只取尺寸不解码像素。
// jpeg 返回的是 EXIF 方向应用后的展示尺寸：方向 5-8 会交换宽高，与浏览器所见一致。
func decodeScreenshotImageConfig(r io.Reader, mimeType string) (image.Config, error) {
	switch mimeType {
	case "image/png":
		return png.DecodeConfig(r)
	case "image/jpeg":
		cfg, orientation, err := jpegScanHeader(r)
		if err != nil {
			return image.Config{}, err
		}
		if orientation >= 5 && orientation <= 8 {
			cfg.Width, cfg.Height = cfg.Height, cfg.Width
		}
		return cfg, nil
	case "image/gif":
		return gif.DecodeConfig(r)
	case "image/webp":
		return webp.DecodeConfig(r)
	default:
		return image.Config{}, fmt.Errorf("unsupported screenshot mime type %q", mimeType)
	}
}

// jpegScanHeader 走 JPEG 段结构直到 SOF：返回 SOF 声明的宽高与途中遇到的
// APP1/Exif Orientation。非目标段只跳负载不落内存（APP1 段长上限 64KB），
// 段前缀允许 0xFF 填充字节；找不到 SOF 报错。探测与解码共用此扫描，结论一致。
func jpegScanHeader(r io.Reader) (image.Config, int, error) {
	var magic [2]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || magic[0] != 0xff || magic[1] != 0xd8 {
		return image.Config{}, 0, fmt.Errorf("not a jpeg stream")
	}
	orientation := 0
	var one [1]byte
	for {
		// 段前缀 0xFF 前允许任意填充字节；标记本身也可能跟 0xFF 填充（FF FF E1 合法）
		for {
			if _, err := io.ReadFull(r, one[:]); err != nil {
				return image.Config{}, 0, err
			}
			if one[0] == 0xff {
				break
			}
		}
		for {
			if _, err := io.ReadFull(r, one[:]); err != nil {
				return image.Config{}, 0, err
			}
			if one[0] != 0xff {
				break
			}
		}
		marker := one[0]
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd9) {
			continue // TEM/RSTn/SOI/EOI 无负载
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return image.Config{}, 0, err
		}
		payloadLen := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if payloadLen < 0 {
			return image.Config{}, 0, fmt.Errorf("invalid jpeg segment length")
		}
		switch {
		// SOFn 帧标记（排除 DHT C4 / JPG C8 / DAC CC）：精度(1)+高(2)+宽(2)
		case marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc:
			head := make([]byte, 5)
			if _, err := io.ReadFull(r, head); err != nil {
				return image.Config{}, 0, err
			}
			return image.Config{
				Height: int(binary.BigEndian.Uint16(head[1:])),
				Width:  int(binary.BigEndian.Uint16(head[3:])),
			}, orientation, nil
		case marker == 0xe1 && orientation == 0:
			payload := make([]byte, payloadLen)
			if _, err := io.ReadFull(r, payload); err != nil {
				return image.Config{}, 0, err
			}
			if len(payload) >= 6 && string(payload[:6]) == "Exif\x00\x00" {
				orientation = parseExifOrientation(payload[6:])
			}
		case marker == 0xda:
			return image.Config{}, 0, fmt.Errorf("jpeg SOF not found before SOS")
		default:
			if _, err := io.CopyN(io.Discard, r, int64(payloadLen)); err != nil {
				return image.Config{}, 0, err
			}
		}
	}
}

// parseExifOrientation 读 TIFF 头 IFD0 中的 Orientation(0x0112) SHORT 值。
func parseExifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(tiff[2:]) != 42 {
		return 0
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd < 0 || ifd+2 > len(tiff) {
		return 0
	}
	count := int(order.Uint16(tiff[ifd:]))
	for i := 0; i < count; i++ {
		entry := ifd + 2 + i*12
		if entry+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[entry:]) == 0x0112 {
			return int(order.Uint16(tiff[entry+8:]))
		}
	}
	return 0
}

// applyJPEGExifOrientation 按 EXIF Orientation(1-8) 把存储方向的像素重排成展示
// 方向：2-4 为镜像/翻转，5-8 为含转置的旋转。0/1/越界值原样返回。
func applyJPEGExifOrientation(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for sy := 0; sy < h; sy++ {
		for sx := 0; sx < w; sx++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-sx, sy
			case 3:
				dx, dy = w-1-sx, h-1-sy
			case 4:
				dx, dy = sx, h-1-sy
			case 5:
				dx, dy = sy, sx
			case 6:
				dx, dy = h-1-sy, sx
			case 7:
				dx, dy = h-1-sy, w-1-sx
			default: // 8
				dx, dy = sy, w-1-sx
			}
			dst.Set(dx, dy, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// cropScreenshot 取图：解码结果均为带 SubImage 的具体类型；兜底 draw 拷贝以防未来格式不支持。
func cropScreenshot(src image.Image, rect image.Rectangle) image.Image {
	type subImager interface {
		SubImage(image.Rectangle) image.Image
	}
	if si, ok := src.(subImager); ok {
		return si.SubImage(rect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}
