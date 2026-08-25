package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueCollabRepository struct {
	db *gorm.DB
}

func NewIssueCollabRepository(db *gorm.DB) *IssueCollabRepository {
	return &IssueCollabRepository{db: db}
}

func (r *IssueCollabRepository) Get(issueID string, kind model.CollabDocumentKind) (*model.IssueCollabDocument, error) {
	var doc model.IssueCollabDocument
	if err := r.db.Where("issue_id = ? AND kind = ?", issueID, kind).First(&doc).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *IssueCollabRepository) ListByIssueID(issueID string) ([]model.IssueCollabDocument, error) {
	var docs []model.IssueCollabDocument
	if err := r.db.Where("issue_id = ?", issueID).Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (r *IssueCollabRepository) Upsert(doc *model.IssueCollabDocument) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"body",
			"author_user_id",
			"author_kind",
			"updated_at",
		}),
	}).Create(doc).Error
}

func (r *IssueCollabRepository) Delete(issueID string, kind model.CollabDocumentKind) error {
	return r.db.Where("issue_id = ? AND kind = ?", issueID, kind).Delete(&model.IssueCollabDocument{}).Error
}

func (r *IssueCollabRepository) DeleteAllByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueCollabDocument{}).Error
}
