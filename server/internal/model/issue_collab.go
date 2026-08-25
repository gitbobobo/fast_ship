package model

import "time"

type CollabAuthorKind string

const (
	CollabAuthorUser  CollabAuthorKind = "user"
	CollabAuthorAgent CollabAuthorKind = "agent"
)

func (k CollabAuthorKind) Valid() bool {
	return k == CollabAuthorUser || k == CollabAuthorAgent
}

type CollabDocumentKind string

const (
	CollabDocumentKindConsensus CollabDocumentKind = "consensus"
	CollabDocumentKindSummary   CollabDocumentKind = "summary"
)

func (k CollabDocumentKind) Valid() bool {
	return k == CollabDocumentKindConsensus || k == CollabDocumentKindSummary
}

type IssueCollabDocument struct {
	IssueID      string             `gorm:"type:text;primaryKey" json:"issue_id"`
	Kind         CollabDocumentKind `gorm:"type:text;primaryKey" json:"kind"`
	Body         string             `gorm:"type:text;not null" json:"body"`
	AuthorUserID string             `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind   CollabAuthorKind   `gorm:"type:text;not null;default:agent" json:"author_kind"`
	CreatedAt    time.Time          `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time          `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabDocument) TableName() string {
	return "issue_collab_documents"
}
