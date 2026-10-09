package service

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestIssueAttachmentServiceUploadAndDetailEmbed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	content := []byte("attachment-binary-payload")
	attachment, err := svc.attachmentService.Upload(issue.ID, user.ID, "report.pdf", bytes.NewReader(content), user.Username)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}
	if attachment.FileName != "report.pdf" {
		t.Fatalf("expected file_name report.pdf, got %q", attachment.FileName)
	}
	if attachment.FileSize != int64(len(content)) {
		t.Fatalf("expected file_size %d, got %d", len(content), attachment.FileSize)
	}
	if attachment.MimeType == "" {
		t.Fatalf("expected sniffed mime_type")
	}
	if attachment.Uploader != user.Username {
		t.Fatalf("expected uploader %q, got %q", user.Username, attachment.Uploader)
	}
	if attachment.DownloadUrl != "/api/attachments/"+attachment.Id+"/download" {
		t.Fatalf("unexpected download_url %q", attachment.DownloadUrl)
	}

	// 同名不去重：再次上传生成新行
	second, err := svc.attachmentService.Upload(issue.ID, user.ID, "report.pdf", bytes.NewReader(content), "API Key: CI")
	if err != nil {
		t.Fatalf("upload second attachment: %v", err)
	}
	if second.Id == attachment.Id {
		t.Fatalf("expected new row for same-name upload")
	}

	resp, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if len(resp.Attachments) != 2 {
		t.Fatalf("expected 2 attachments in detail, got %d", len(resp.Attachments))
	}
	if resp.Attachments[0].Id != attachment.Id || resp.Attachments[1].Id != second.Id {
		t.Fatalf("unexpected attachments order/ids: %+v", resp.Attachments)
	}
	if resp.Attachments[1].Uploader != "API Key: CI" {
		t.Fatalf("expected uploader from API key label, got %q", resp.Attachments[1].Uploader)
	}
}

func TestIssueAttachmentServiceUploadRejectsGitHubIssue(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	_, err := svc.attachmentService.Upload(issue.ID, user.ID, "a.txt", bytes.NewReader([]byte("x")), user.Username)
	if !errors.Is(err, errs.ErrIssueReadOnly) {
		t.Fatalf("expected ErrIssueReadOnly, got %v", err)
	}
}

func TestIssueAttachmentServiceDeleteAndDownload(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	content := []byte("attachment-content")
	attachment, err := svc.attachmentService.Upload(issue.ID, user.ID, "note.txt", bytes.NewReader(content), user.Username)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}

	reader, fileName, err := svc.attachmentService.Download(attachment.Id, user.ID)
	if err != nil {
		t.Fatalf("download attachment: %v", err)
	}
	downloaded, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if fileName != "note.txt" || !bytes.Equal(downloaded, content) {
		t.Fatalf("unexpected download: name=%q len=%d", fileName, len(downloaded))
	}

	stored, err := svc.issueAttachmentRepo.FindByID(attachment.Id)
	if err != nil {
		t.Fatalf("find attachment row: %v", err)
	}

	// 非项目成员下载/删除被拒（不限上传者本人，但校验项目归属）
	if _, _, err := svc.attachmentService.Download(attachment.Id, other.ID); !errors.Is(err, errs.ErrProjectNotFound) {
		t.Fatalf("expected ErrProjectNotFound for cross-user download, got %v", err)
	}
	if err := svc.attachmentService.Delete(attachment.Id, other.ID); !errors.Is(err, errs.ErrProjectNotFound) {
		t.Fatalf("expected ErrProjectNotFound for cross-user delete, got %v", err)
	}

	if err := svc.attachmentService.Delete(attachment.Id, user.ID); err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	exists, err := svc.storage.Exists(stored.FilePath)
	if err != nil {
		t.Fatalf("stat attachment file: %v", err)
	}
	if exists {
		t.Fatalf("expected attachment file removed from storage")
	}
	if _, err := svc.issueAttachmentRepo.FindByID(attachment.Id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected attachment row deleted, got %v", err)
	}
	if _, _, err := svc.attachmentService.Download(attachment.Id, user.ID); !errors.Is(err, errs.ErrIssueAttachmentNotFound) {
		t.Fatalf("expected ErrIssueAttachmentNotFound after delete, got %v", err)
	}
	if err := svc.attachmentService.Delete(attachment.Id, user.ID); !errors.Is(err, errs.ErrIssueAttachmentNotFound) {
		t.Fatalf("expected ErrIssueAttachmentNotFound on second delete, got %v", err)
	}
}

func TestIssueAttachmentCascadeOnIssueAndProjectDelete(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	attachment, err := svc.attachmentService.Upload(issue.ID, user.ID, "keep.bin", bytes.NewReader([]byte("x")), user.Username)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}

	// createTestIssue 会额外种一行 github_meta（生产里 internal issue 没有）；
	// issue_github_meta 等旧表的外键是 NO ACTION 的存量缺陷，先删掉让它回到
	// 生产 internal issue 的真实形态，再验证 issue_attachments 自身的级联。
	if err := svc.db.Delete(&model.IssueGitHubMeta{}, "issue_id = ?", issue.ID).Error; err != nil {
		t.Fatalf("delete seed github meta: %v", err)
	}
	if err := svc.db.Delete(&model.Issue{}, "id = ?", issue.ID).Error; err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if _, err := svc.issueAttachmentRepo.FindByID(attachment.Id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected attachment row cascaded, got %v", err)
	}

	issue2 := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
		i.SequenceNumber = 2
	})
	attachment2, err := svc.attachmentService.Upload(issue2.ID, user.ID, "keep2.bin", bytes.NewReader([]byte("y")), user.Username)
	if err != nil {
		t.Fatalf("upload attachment2: %v", err)
	}
	stored2, err := svc.issueAttachmentRepo.FindByID(attachment2.Id)
	if err != nil {
		t.Fatalf("find attachment2: %v", err)
	}
	if err := svc.db.Delete(&model.IssueGitHubMeta{}, "issue_id = ?", issue2.ID).Error; err != nil {
		t.Fatalf("delete seed github meta 2: %v", err)
	}

	projectService := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	if err := projectService.Delete(project.ID, user.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := svc.issueAttachmentRepo.FindByID(attachment2.Id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected attachment row removed by project cascade, got %v", err)
	}
	exists, err := svc.storage.Exists(stored2.FilePath)
	if err != nil {
		t.Fatalf("stat attachment file: %v", err)
	}
	if exists {
		t.Fatalf("expected attachment file removed by project DeletePrefix")
	}
}
