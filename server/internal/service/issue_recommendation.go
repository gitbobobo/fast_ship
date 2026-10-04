package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

const (
	recommendationReasonMaxRunes = 500
	recommendationMaxDeps        = 20
)

type IssueRecommendationService struct {
	recRepo          *repository.IssueRecommendationRepository
	issueRepo        *repository.IssueRepository
	internalMetaRepo *repository.IssueInternalMetaRepository
	projectRepo      *repository.ProjectRepository
}

func NewIssueRecommendationService(
	recRepo *repository.IssueRecommendationRepository,
	issueRepo *repository.IssueRepository,
	internalMetaRepo *repository.IssueInternalMetaRepository,
	projectRepo *repository.ProjectRepository,
) *IssueRecommendationService {
	return &IssueRecommendationService{
		recRepo:          recRepo,
		issueRepo:        issueRepo,
		internalMetaRepo: internalMetaRepo,
		projectRepo:      projectRepo,
	}
}

type UpsertIssueRecommendationRequest struct {
	Reason       string
	Priority     model.IssueRecommendationPriority
	Dependencies []string
}

type RecommendationIssueSummary struct {
	ID             string                    `json:"id"`
	ProjectID      string                    `json:"project_id"`
	ProjectName    string                    `json:"project_name"`
	Source         model.IssueSource         `json:"source"`
	SequenceNumber int                       `json:"sequence_number"`
	Title          string                    `json:"title"`
	State          model.IssueState          `json:"state"`
	WorkflowStatus model.IssueWorkflowStatus `json:"workflow_status"`
}

type RecommendationDependencyResponse struct {
	IssueID        string                    `json:"issue_id"`
	Title          string                    `json:"title"`
	State          model.IssueState          `json:"state"`
	WorkflowStatus model.IssueWorkflowStatus `json:"workflow_status"`
	ProjectID      string                    `json:"project_id"`
	SequenceNumber int                       `json:"sequence_number"`
}

type IssueRecommendationResponse struct {
	Issue        RecommendationIssueSummary         `json:"issue"`
	Reason       string                             `json:"reason"`
	Priority     model.IssueRecommendationPriority  `json:"priority"`
	CreatedBy    string                             `json:"created_by"`
	CreatedAt    string                             `json:"created_at"`
	UpdatedAt    string                             `json:"updated_at"`
	Dependencies []RecommendationDependencyResponse `json:"dependencies"`
}

type IssueRecommendationListResponse struct {
	Items []IssueRecommendationResponse `json:"items"`
}

// Upsert 写入或整体覆盖一个 issue 的推荐（reason/priority/dependencies/created_by），保留原 created_at。
func (s *IssueRecommendationService) Upsert(issueID, userID, createdBy string, req UpsertIssueRecommendationRequest) (*IssueRecommendationResponse, error) {
	reason := strings.TrimSpace(req.Reason)
	if count := utf8.RuneCountInString(reason); count < 1 || count > recommendationReasonMaxRunes {
		return nil, errs.ErrInvalidParams
	}

	priority := req.Priority
	if priority == "" {
		priority = model.IssueRecommendationPriorityMedium
	} else if !model.IsValidIssueRecommendationPriority(priority) {
		return nil, errs.ErrInvalidParams
	}

	depIDs, appErr := normalizeRecommendationDeps(req.Dependencies, issueID)
	if appErr != nil {
		return nil, appErr
	}

	// 读取与写入必须同事务：校验时看到的状态与提交时写入的状态一致。
	var (
		issue     *model.Issue
		project   *model.Project
		meta      *model.IssueInternalMeta
		depIssues map[string]model.Issue
		depMetas  map[string]model.IssueInternalMeta
		rec       model.IssueRecommendation
		deps      []model.RecommendationDependency
	)
	err := s.recRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		if issue, err = s.issueRepo.FindByIDTx(tx, issueID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrIssueNotFound
			}
			return err
		}
		if project, err = s.projectRepo.FindByIDTx(tx, issue.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrProjectNotFound
			}
			return err
		}
		if meta, err = s.ensureRecommendable(tx, issue); err != nil {
			return err
		}
		if depIssues, err = s.loadDepIssues(tx, depIDs, issue.ProjectID, userID); err != nil {
			return err
		}
		if depMetas, err = s.internalMetaRepo.ListByIssueIDsTx(tx, depIDs); err != nil {
			return err
		}

		now := time.Now().UTC()
		rec = model.IssueRecommendation{
			IssueID:   issue.ID,
			ProjectID: issue.ProjectID,
			Reason:    reason,
			Priority:  priority,
			CreatedBy: createdBy,
			CreatedAt: now,
			UpdatedAt: now,
		}
		deps = make([]model.RecommendationDependency, 0, len(depIDs))
		for i, depID := range depIDs {
			deps = append(deps, model.RecommendationDependency{
				IssueID:    issue.ID,
				DepIssueID: depID,
				Position:   i,
				CreatedAt:  now,
			})
		}
		if err := s.recRepo.UpsertTx(tx, &rec); err != nil {
			return err
		}
		return s.recRepo.ReplaceDependenciesTx(tx, issue.ID, deps)
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}

	item := toRecommendationResponse(rec, *issue, meta, project.Name, deps, depIssues, depMetas)
	return &item, nil
}

func (s *IssueRecommendationService) Delete(issueID, userID string) error {
	rec, err := s.recRepo.Get(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrRecommendationNotFound
		}
		return errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(rec.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	if err := s.recRepo.Delete(issueID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// List 一次全量返回推荐列表；projectID 为空时覆盖当前用户全部项目。
func (s *IssueRecommendationService) List(userID, projectID string) (*IssueRecommendationListResponse, error) {
	projectNames := make(map[string]string)
	var projectIDs []string
	if projectID != "" {
		project, err := s.projectRepo.FindByID(projectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		projectIDs = []string{project.ID}
		projectNames[project.ID] = project.Name
	} else {
		projects, err := s.projectRepo.ListByUser(userID)
		if err != nil {
			return nil, errs.ErrInternal
		}
		for _, p := range projects {
			projectIDs = append(projectIDs, p.ID)
			projectNames[p.ID] = p.Name
		}
	}

	recs, err := s.recRepo.ListByProjectIDs(projectIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	issueIDs := make([]string, 0, len(recs))
	for _, rec := range recs {
		issueIDs = append(issueIDs, rec.IssueID)
	}
	issues, err := s.issueRepo.ListByIDs(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	metas, err := s.internalMetaRepo.ListByIssueIDs(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	depRows, err := s.recRepo.ListDependencies(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	depIDSet := make(map[string]struct{}, len(depRows))
	for _, dep := range depRows {
		depIDSet[dep.DepIssueID] = struct{}{}
	}
	depIssues, err := s.issueRepo.ListByIDs(sortedKeys(depIDSet))
	if err != nil {
		return nil, errs.ErrInternal
	}
	depMetas, err := s.internalMetaRepo.ListByIssueIDs(sortedKeys(depIDSet))
	if err != nil {
		return nil, errs.ErrInternal
	}

	depsByIssue := make(map[string][]model.RecommendationDependency, len(recs))
	for _, dep := range depRows {
		depsByIssue[dep.IssueID] = append(depsByIssue[dep.IssueID], dep)
	}

	items := make([]IssueRecommendationResponse, 0, len(recs))
	for _, rec := range recs {
		issue, ok := issues[rec.IssueID]
		if !ok {
			continue
		}
		items = append(items, toRecommendationResponse(
			rec, issue, internalMetaOrNil(metas, rec.IssueID), projectNames[rec.ProjectID],
			depsByIssue[rec.IssueID], depIssues, depMetas,
		))
	}
	return &IssueRecommendationListResponse{Items: items}, nil
}

// ensureRecommendable 校验 issue 当前可被推荐：state=open 且 workflow_status 为未设置或 todo。
// meta 行不存在按未设置处理；返回 meta 供调用方复用。
func (s *IssueRecommendationService) ensureRecommendable(tx *gorm.DB, issue *model.Issue) (*model.IssueInternalMeta, error) {
	if issue.State != model.IssueStateOpen {
		return nil, errs.ErrIssueNotRecommendable
	}
	meta, err := s.internalMetaRepo.GetTx(tx, issue.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	switch meta.WorkflowStatus {
	case "", model.IssueWorkflowStatusTodo:
		return meta, nil
	default:
		return nil, errs.ErrIssueNotRecommendable
	}
}

// loadDepIssues 批量读取依赖 issue 并校验项目归属；任一不存在或属于其他用户统一返回 40405，避免被用来枚举。
func (s *IssueRecommendationService) loadDepIssues(tx *gorm.DB, depIDs []string, ownProjectID, userID string) (map[string]model.Issue, error) {
	issues, err := s.issueRepo.ListByIDsTx(tx, depIDs)
	if err != nil {
		return nil, err
	}
	projectIDs := make(map[string]struct{})
	for _, depID := range depIDs {
		dep, ok := issues[depID]
		if !ok {
			return nil, errs.ErrIssueNotFound
		}
		if dep.ProjectID != ownProjectID {
			projectIDs[dep.ProjectID] = struct{}{}
		}
	}
	for projectID := range projectIDs {
		if _, err := s.projectRepo.FindByIDTx(tx, projectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrIssueNotFound
			}
			return nil, err
		}
	}
	return issues, nil
}

func toRecommendationResponse(
	rec model.IssueRecommendation,
	issue model.Issue,
	meta *model.IssueInternalMeta,
	projectName string,
	deps []model.RecommendationDependency,
	depIssues map[string]model.Issue,
	depMetas map[string]model.IssueInternalMeta,
) IssueRecommendationResponse {
	return IssueRecommendationResponse{
		Issue: RecommendationIssueSummary{
			ID:             issue.ID,
			ProjectID:      issue.ProjectID,
			ProjectName:    projectName,
			Source:         issue.Source,
			SequenceNumber: issue.SequenceNumber,
			Title:          issue.Title,
			State:          issue.State,
			WorkflowStatus: workflowStatusOf(meta),
		},
		Reason:       rec.Reason,
		Priority:     rec.Priority,
		CreatedBy:    rec.CreatedBy,
		CreatedAt:    formatTime(rec.CreatedAt),
		UpdatedAt:    formatTime(rec.UpdatedAt),
		Dependencies: buildDependencyResponses(deps, depIssues, depMetas),
	}
}

func buildDependencyResponses(deps []model.RecommendationDependency, depIssues map[string]model.Issue, depMetas map[string]model.IssueInternalMeta) []RecommendationDependencyResponse {
	items := make([]RecommendationDependencyResponse, 0, len(deps))
	for _, dep := range deps {
		issue, ok := depIssues[dep.DepIssueID]
		if !ok {
			continue
		}
		items = append(items, RecommendationDependencyResponse{
			IssueID:        dep.DepIssueID,
			Title:          issue.Title,
			State:          issue.State,
			WorkflowStatus: workflowStatusOf(internalMetaOrNil(depMetas, dep.DepIssueID)),
			ProjectID:      issue.ProjectID,
			SequenceNumber: issue.SequenceNumber,
		})
	}
	return items
}

func workflowStatusOf(meta *model.IssueInternalMeta) model.IssueWorkflowStatus {
	if meta == nil {
		return ""
	}
	return meta.WorkflowStatus
}

func internalMetaOrNil(metas map[string]model.IssueInternalMeta, issueID string) *model.IssueInternalMeta {
	meta, ok := metas[issueID]
	if !ok {
		return nil
	}
	return &meta
}

// normalizeRecommendationDeps 校验并整理依赖列表：去空白、去重（保序）、剔除自身，限制条数。
func normalizeRecommendationDeps(raw []string, issueID string) ([]string, *errs.AppError) {
	if len(raw) > recommendationMaxDeps {
		return nil, errs.ErrInvalidParams
	}
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" || id == issueID {
			return nil, errs.ErrInvalidParams
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}
