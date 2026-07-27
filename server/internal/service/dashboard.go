package service

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
)

const dashboardResolvedWindowDays = 30

type DashboardService struct {
	dashboardRepo *repository.DashboardRepository
}

type DashboardOverviewResponse struct {
	OpenIssuesByProject []DashboardProjectOpenIssuePoint `json:"open_issues_by_project"`
	DailyResolved       []DashboardDailyResolvedPoint    `json:"daily_resolved"`
}

type DashboardProjectOpenIssuePoint struct {
	ProjectID      string `json:"project_id"`
	ProjectName    string `json:"project_name"`
	OpenIssueCount int    `json:"open_issue_count"`
}

type DashboardDailyResolvedProjectPoint struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Count       int    `json:"count"`
}

type DashboardDailyResolvedPoint struct {
	Date          string                               `json:"date"`
	ResolvedCount int                                  `json:"resolved_count"`
	Projects      []DashboardDailyResolvedProjectPoint `json:"projects"`
}

func NewDashboardService(dashboardRepo *repository.DashboardRepository) *DashboardService {
	return &DashboardService{dashboardRepo: dashboardRepo}
}

func (s *DashboardService) GetOverview(userID string) (*DashboardOverviewResponse, error) {
	openCounts, err := s.dashboardRepo.GetOpenIssueCounts(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	startInclusive := today.AddDate(0, 0, -(dashboardResolvedWindowDays - 1))
	endExclusive := today.AddDate(0, 0, 1)

	resolvedCounts, err := s.dashboardRepo.GetDailyResolvedCounts(userID, startInclusive, endExclusive)
	if err != nil {
		return nil, errs.ErrInternal
	}

	resolvedByDate := make(map[string]int, len(resolvedCounts))
	for _, row := range resolvedCounts {
		resolvedByDate[row.Date] = row.ResolvedCount
	}

	resolvedByProject, err := s.dashboardRepo.GetDailyResolvedCountsByProject(userID, startInclusive, endExclusive)
	if err != nil {
		return nil, errs.ErrInternal
	}

	projectsByDate := make(map[string][]DashboardDailyResolvedProjectPoint, len(resolvedCounts))
	for _, row := range resolvedByProject {
		projectsByDate[row.Date] = append(projectsByDate[row.Date], DashboardDailyResolvedProjectPoint{
			ProjectID:   row.ProjectID,
			ProjectName: row.ProjectName,
			Count:       row.ResolvedCount,
		})
	}

	resp := &DashboardOverviewResponse{
		OpenIssuesByProject: make([]DashboardProjectOpenIssuePoint, 0, len(openCounts)),
		DailyResolved:       make([]DashboardDailyResolvedPoint, 0, dashboardResolvedWindowDays),
	}

	for _, row := range openCounts {
		resp.OpenIssuesByProject = append(resp.OpenIssuesByProject, DashboardProjectOpenIssuePoint{
			ProjectID:      row.ProjectID,
			ProjectName:    row.ProjectName,
			OpenIssueCount: row.OpenIssueCount,
		})
	}

	for offset := 0; offset < dashboardResolvedWindowDays; offset++ {
		current := startInclusive.AddDate(0, 0, offset)
		date := current.Format("2006-01-02")
		resp.DailyResolved = append(resp.DailyResolved, DashboardDailyResolvedPoint{
			Date:          date,
			ResolvedCount: resolvedByDate[date],
			Projects:      projectsByDate[date],
		})
	}

	return resp, nil
}
