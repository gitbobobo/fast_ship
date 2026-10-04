package model

import "time"

type IssueRecommendationPriority string

const (
	IssueRecommendationPriorityHigh   IssueRecommendationPriority = "high"
	IssueRecommendationPriorityMedium IssueRecommendationPriority = "medium"
	IssueRecommendationPriorityLow    IssueRecommendationPriority = "low"
)

func IsValidIssueRecommendationPriority(priority IssueRecommendationPriority) bool {
	switch priority {
	case IssueRecommendationPriorityHigh, IssueRecommendationPriorityMedium, IssueRecommendationPriorityLow:
		return true
	default:
		return false
	}
}

type IssueRecommendation struct {
	IssueID   string                      `gorm:"type:text;primaryKey" json:"issue_id"`
	ProjectID string                      `gorm:"type:text;not null;index" json:"project_id"`
	Reason    string                      `gorm:"type:text" json:"reason"`
	Priority  IssueRecommendationPriority `gorm:"type:text;not null;default:medium" json:"priority"`
	CreatedBy string                      `gorm:"type:text" json:"created_by"`
	CreatedAt time.Time                   `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time                   `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueRecommendation) TableName() string {
	return "issue_recommendations"
}

type RecommendationDependency struct {
	IssueID    string    `gorm:"type:text;primaryKey" json:"issue_id"`
	DepIssueID string    `gorm:"type:text;primaryKey" json:"dep_issue_id"`
	Position   int       `gorm:"not null;default:0" json:"position"`
	CreatedAt  time.Time `gorm:"not null" json:"created_at"`

	Issue    Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
	DepIssue Issue `gorm:"foreignKey:DepIssueID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (RecommendationDependency) TableName() string {
	return "recommendation_dependencies"
}
