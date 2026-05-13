package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type DashboardRepository struct {
	db *gorm.DB
}

type DashboardProjectOpenIssueCount struct {
	ProjectID      string `gorm:"column:project_id"`
	ProjectName    string `gorm:"column:project_name"`
	OpenIssueCount int    `gorm:"column:open_issue_count"`
}

type DashboardDailyResolvedCount struct {
	Date          string `gorm:"column:date"`
	ResolvedCount int    `gorm:"column:resolved_count"`
}

type DashboardDailyResolvedProjectCount struct {
	Date          string `gorm:"column:date"`
	ProjectID     string `gorm:"column:project_id"`
	ProjectName   string `gorm:"column:project_name"`
	ResolvedCount int    `gorm:"column:resolved_count"`
}

func NewDashboardRepository(db *gorm.DB) *DashboardRepository {
	return &DashboardRepository{db: db}
}

func (r *DashboardRepository) GetOpenIssueCounts(userID string) ([]DashboardProjectOpenIssueCount, error) {
	var rows []DashboardProjectOpenIssueCount
	err := r.db.
		Table("projects AS p").
		Select(`
			p.id AS project_id,
			p.name AS project_name,
			COALESCE(SUM(CASE
				WHEN i.id IS NOT NULL
				 AND i.state = ?
				 AND (m.workflow_status IS NULL OR m.workflow_status != ?)
				THEN 1
				ELSE 0
			END), 0) AS open_issue_count
		`, model.IssueStateOpen, model.IssueWorkflowStatusDone).
		Joins("LEFT JOIN issues i ON i.project_id = p.id").
		Joins("LEFT JOIN issue_internal_meta m ON m.issue_id = i.id").
		Where("p.user_id = ?", userID).
		Group("p.id, p.name, p.created_at").
		Order("p.created_at DESC").
		Scan(&rows).Error
	return rows, err
}

func (r *DashboardRepository) GetDailyResolvedCounts(
	userID string,
	startInclusive time.Time,
	endExclusive time.Time,
) ([]DashboardDailyResolvedCount, error) {
	var rows []DashboardDailyResolvedCount
	err := r.db.
		Table("issue_internal_meta AS m").
		Select("DATE(m.completed_at) AS date, COUNT(*) AS resolved_count").
		Joins("JOIN issues i ON i.id = m.issue_id").
		Joins("JOIN projects p ON p.id = i.project_id").
		Where("p.user_id = ?", userID).
		Where("m.completed_at IS NOT NULL").
		Where("m.completed_at >= ? AND m.completed_at < ?", startInclusive, endExclusive).
		Group("DATE(m.completed_at)").
		Order("DATE(m.completed_at) ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *DashboardRepository) GetDailyResolvedCountsByProject(
	userID string,
	startInclusive time.Time,
	endExclusive time.Time,
) ([]DashboardDailyResolvedProjectCount, error) {
	var rows []DashboardDailyResolvedProjectCount
	err := r.db.
		Table("issue_internal_meta AS m").
		Select("DATE(m.completed_at) AS date, p.id AS project_id, p.name AS project_name, COUNT(*) AS resolved_count").
		Joins("JOIN issues i ON i.id = m.issue_id").
		Joins("JOIN projects p ON p.id = i.project_id").
		Where("p.user_id = ?", userID).
		Where("m.completed_at IS NOT NULL").
		Where("m.completed_at >= ? AND m.completed_at < ?", startInclusive, endExclusive).
		Group("DATE(m.completed_at), p.id, p.name").
		Order("DATE(m.completed_at) ASC, p.name ASC").
		Scan(&rows).Error
	return rows, err
}
