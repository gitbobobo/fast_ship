package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type IssueAttachmentRepository struct {
	db *gorm.DB
}

func NewIssueAttachmentRepository(db *gorm.DB) *IssueAttachmentRepository {
	return &IssueAttachmentRepository{db: db}
}

func (r *IssueAttachmentRepository) Create(attachment *model.IssueAttachment) error {
	return r.db.Create(attachment).Error
}

func (r *IssueAttachmentRepository) FindByID(id string) (*model.IssueAttachment, error) {
	var attachment model.IssueAttachment
	if err := r.db.Where("id = ?", id).First(&attachment).Error; err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (r *IssueAttachmentRepository) ListByIssueID(issueID string) ([]model.IssueAttachment, error) {
	var attachments []model.IssueAttachment
	if err := r.db.Where("issue_id = ?", issueID).Order("created_at ASC").Find(&attachments).Error; err != nil {
		return nil, err
	}
	return attachments, nil
}

func (r *IssueAttachmentRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.IssueAttachment{}).Error
}
