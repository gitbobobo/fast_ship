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

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IssueAttachmentService 管理 Issue 文件附件：不限类型、与正文引用解耦、
// 同名不去重。仅 internal 来源 Issue 可上传/删除（github 源返回 40908）。
type IssueAttachmentService struct {
	attachmentRepo *repository.IssueAttachmentRepository
	issueRepo      *repository.IssueRepository
	projectRepo    *repository.ProjectRepository
	storage        storage.Storage
	cfg            *config.Config
	logger         *zap.Logger
}

func NewIssueAttachmentService(
	attachmentRepo *repository.IssueAttachmentRepository,
	issueRepo *repository.IssueRepository,
	projectRepo *repository.ProjectRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
) *IssueAttachmentService {
	return &IssueAttachmentService{
		attachmentRepo: attachmentRepo,
		issueRepo:      issueRepo,
		projectRepo:    projectRepo,
		storage:        storage,
		cfg:            cfg,
		logger:         logger,
	}
}

func (s *IssueAttachmentService) Upload(issueID, userID, fileName string, reader io.Reader, uploader string) (*IssueAttachmentResponse, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}

	readFrom := reader
	if s.cfg.Upload.MaxFileSize > 0 {
		readFrom = io.LimitReader(reader, s.cfg.Upload.MaxFileSize+1)
	}

	head := make([]byte, issueAssetSniffBytes)
	headSize, err := io.ReadFull(readFrom, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errs.ErrInternal
	}
	head = head[:headSize]
	if len(head) == 0 {
		return nil, errs.ErrInvalidParams
	}
	mimeType := http.DetectContentType(head)

	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "attachment"
	}

	attachmentID := uuid.NewString()
	storagePath := buildIssueAttachmentStoragePath(issue.ProjectID, issue.ID, attachmentID, name)
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(storagePath, countedReader); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInvalidParams
	}

	attachment := &model.IssueAttachment{
		ID:         attachmentID,
		IssueID:    issue.ID,
		FileName:   name,
		FilePath:   storagePath,
		MimeType:   mimeType,
		FileSize:   countedReader.n,
		UploadedBy: uploader,
		CreatedAt:  time.Now().UTC(),
	}

	if err := s.attachmentRepo.Create(attachment); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue attachment uploaded",
		zap.String("action", "upload_issue_attachment"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", uploader),
	)
	resp := toIssueAttachmentResponse(*attachment)
	return &resp, nil
}

func (s *IssueAttachmentService) Delete(attachmentID, userID string) error {
	attachment, err := s.attachmentRepo.FindByID(attachmentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrIssueAttachmentNotFound
		}
		return errs.ErrInternal
	}

	issue, err := s.issueRepo.FindByID(attachment.IssueID)
	if err != nil {
		return errs.ErrInternal
	}

	// 归属校验先于 source 检查：非项目成员统一拿到 40401，无法借
	// 40908/40415 的返回差异探测附件挂在 github 还是 internal issue 上。
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrNotOwner
	}
	if issue.Source != model.IssueSourceInternal {
		return errs.ErrIssueReadOnly
	}

	if err := s.attachmentRepo.Delete(attachment.ID); err != nil {
		return errs.ErrInternal
	}
	_ = s.storage.Delete(attachment.FilePath)
	return nil
}

func (s *IssueAttachmentService) Download(attachmentID, userID string) (io.ReadCloser, string, error) {
	attachment, err := s.attachmentRepo.FindByID(attachmentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrIssueAttachmentNotFound
		}
		return nil, "", errs.ErrInternal
	}

	issue, err := s.issueRepo.FindByID(attachment.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrIssueNotFound
		}
		return nil, "", errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrProjectNotFound
		}
		return nil, "", errs.ErrNotOwner
	}

	reader, err := s.storage.Get(attachment.FilePath)
	if err != nil {
		return nil, "", errs.ErrInternal
	}
	return reader, attachment.FileName, nil
}

func buildIssueAttachmentStoragePath(projectID, issueID, attachmentID, fileName string) string {
	return fmt.Sprintf("%s/issues/%s/attachments/%s%s", projectID, issueID, attachmentID, strings.ToLower(filepath.Ext(fileName)))
}

func toIssueAttachmentResponse(attachment model.IssueAttachment) IssueAttachmentResponse {
	return IssueAttachmentResponse{
		Id:          attachment.ID,
		FileName:    attachment.FileName,
		FileSize:    attachment.FileSize,
		MimeType:    attachment.MimeType,
		Uploader:    attachment.UploadedBy,
		DownloadUrl: fmt.Sprintf("/api/attachments/%s/download", attachment.ID),
		CreatedAt:   formatTime(attachment.CreatedAt),
	}
}

func toIssueAttachmentResponses(attachments []model.IssueAttachment) []IssueAttachmentResponse {
	items := make([]IssueAttachmentResponse, 0, len(attachments))
	for _, attachment := range attachments {
		items = append(items, toIssueAttachmentResponse(attachment))
	}
	return items
}
