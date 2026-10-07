package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

const (
	recommendationReasonMaxRunes = 500
	recommendationNoteMaxRunes   = 500
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

// Upsert 写入或整体覆盖一个 issue 的推荐（reason/priority/dependencies/created_by），保留原 created_at。
func (s *IssueRecommendationService) Upsert(issueID, userID, createdBy string, req UpsertIssueRecommendationRequest) (*IssueRecommendationResponse, error) {
	reason := strings.TrimSpace(req.Reason)
	if count := utf8.RuneCountInString(reason); count < 1 || count > recommendationReasonMaxRunes {
		return nil, errs.ErrInvalidParams
	}

	priority := api.Deref(req.Priority)
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
		// 延后态推荐对 Agent 封闭：整行冻结保留但不允许覆盖写入
		if existing, err := s.recRepo.GetTx(tx, issue.ID); err == nil {
			if existing.DeferredAt != nil {
				return errs.ErrRecommendationDeferred
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
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
		if err := s.recRepo.ReplaceDependenciesTx(tx, issue.ID, deps); err != nil {
			return err
		}
		// OnConflict 保留原 created_at，回读拿到库里的真实行再组响应
		stored, err := s.recRepo.GetTx(tx, issue.ID)
		if err != nil {
			return err
		}
		rec = *stored
		return nil
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

// Delete 硬删推荐行（删除=遗忘，可再被推荐）。isJWT 标识调用方凭证：
// API Key 删延后项返回 40911（延后=压制，不允许 Agent 抹掉），JWT 删延后项即彻底移除。
// 读、归属校验、延后检查、删除全部在同一事务：SQLite 单写者下事务内读到的是最新
// committed 状态，杜绝「读到 active 后被延后，仍按旧态删除」的交错。
func (s *IssueRecommendationService) Delete(issueID, userID string, isJWT bool) error {
	err := s.recRepo.Transaction(func(tx *gorm.DB) error {
		rec, err := s.recRepo.GetTx(tx, issueID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrRecommendationNotFound
			}
			return err
		}
		if _, err := s.projectRepo.FindByIDTx(tx, rec.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrProjectNotFound
			}
			return err
		}
		if rec.DeferredAt != nil && !isJWT {
			return errs.ErrRecommendationDeferred
		}
		return s.recRepo.DeleteTx(tx, issueID)
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return appErr
		}
		return errs.ErrInternal
	}
	return nil
}

// Defer 把已存在的推荐置为延后态（仅 JWT 用户调用；路由层已挡 API Key）。
// 冻结保留整行数据；重复调用覆盖 note 并刷新 deferred_at，是幂等 upsert。
func (s *IssueRecommendationService) Defer(issueID, userID, note string) (*IssueRecommendationResponse, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > recommendationNoteMaxRunes {
		return nil, errs.ErrInvalidParams
	}

	var (
		rec     *model.IssueRecommendation
		project *model.Project
	)
	err := s.recRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		rec, err = s.recRepo.GetTx(tx, issueID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrRecommendationNotFound
			}
			return err
		}
		if project, err = s.projectRepo.FindByIDTx(tx, rec.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrProjectNotFound
			}
			return err
		}
		if err := s.recRepo.DeferTx(tx, issueID, note, time.Now().UTC()); err != nil {
			return err
		}
		// 回读库里的真实行再组响应
		rec, err = s.recRepo.GetTx(tx, issueID)
		return err
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}
	return s.recommendationResponse(rec, project.Name)
}

// Restore 撤销延后把推荐移回 active 组：清 deferred_at/defer_note 并刷新 updated_at。
// 防御性校验 issue 仍为 open（否则 40910）；未延后时调用视为幂等成功，返回当前项。
func (s *IssueRecommendationService) Restore(issueID, userID string) (*IssueRecommendationResponse, error) {
	var (
		rec     *model.IssueRecommendation
		project *model.Project
	)
	err := s.recRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		rec, err = s.recRepo.GetTx(tx, issueID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrRecommendationNotFound
			}
			return err
		}
		if project, err = s.projectRepo.FindByIDTx(tx, rec.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrProjectNotFound
			}
			return err
		}
		issue, err := s.issueRepo.FindByIDTx(tx, issueID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrIssueNotFound
			}
			return err
		}
		if issue.State != model.IssueStateOpen {
			return errs.ErrIssueNotRecommendable
		}
		if rec.DeferredAt == nil {
			return nil
		}
		if err := s.recRepo.RestoreTx(tx, issueID, time.Now().UTC()); err != nil {
			return err
		}
		rec, err = s.recRepo.GetTx(tx, issueID)
		return err
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}
	return s.recommendationResponse(rec, project.Name)
}

// recommendationResponse 为已提交的推荐行组装单条响应（Defer/Restore 写后回读共用）。
func (s *IssueRecommendationService) recommendationResponse(rec *model.IssueRecommendation, projectName string) (*IssueRecommendationResponse, error) {
	issue, err := s.issueRepo.FindByID(rec.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}
	meta, err := s.internalMetaRepo.Get(rec.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			meta = nil
		} else {
			return nil, errs.ErrInternal
		}
	}
	deps, err := s.recRepo.ListDependencies([]string{rec.IssueID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	depIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		depIDs = append(depIDs, dep.DepIssueID)
	}
	depIssues, err := s.issueRepo.ListByIDs(depIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	depMetas, err := s.internalMetaRepo.ListByIssueIDs(depIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	item := toRecommendationResponse(*rec, *issue, meta, projectName, deps, depIssues, depMetas)
	return &item, nil
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
			Id:             issue.ID,
			ProjectId:      issue.ProjectID,
			ProjectName:    projectName,
			Source:         issue.Source,
			SequenceNumber: issue.SequenceNumber,
			Reference:      buildIssueReference(issue),
			Title:          issue.Title,
			State:          issue.State,
			WorkflowStatus: workflowStatusOf(meta),
		},
		Reason:       rec.Reason,
		Priority:     rec.Priority,
		CreatedBy:    rec.CreatedBy,
		Status:       recommendationStatusOf(rec),
		DeferredAt:   formatTimePtr(rec.DeferredAt),
		DeferNote:    deferNoteOf(rec),
		CreatedAt:    formatTime(rec.CreatedAt),
		UpdatedAt:    formatTime(rec.UpdatedAt),
		Dependencies: buildDependencyResponses(deps, depIssues, depMetas),
	}
}

// recommendationStatusOf 由 deferred_at 派生契约层的 active/deferred 状态。
func recommendationStatusOf(rec model.IssueRecommendation) api.IssueRecommendationStatus {
	if rec.DeferredAt != nil {
		return api.Deferred
	}
	return api.Active
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return api.Ptr(formatTime(*t))
}

// deferNoteOf 空串映射为 nil，契约层 defer_note 用 null 表达「无备注」。
func deferNoteOf(rec model.IssueRecommendation) *string {
	if rec.DeferNote == "" {
		return nil
	}
	return api.Ptr(rec.DeferNote)
}

func buildDependencyResponses(deps []model.RecommendationDependency, depIssues map[string]model.Issue, depMetas map[string]model.IssueInternalMeta) []RecommendationDependencyResponse {
	items := make([]RecommendationDependencyResponse, 0, len(deps))
	for _, dep := range deps {
		issue, ok := depIssues[dep.DepIssueID]
		if !ok {
			continue
		}
		items = append(items, RecommendationDependencyResponse{
			IssueId:        dep.DepIssueID,
			Title:          issue.Title,
			State:          issue.State,
			WorkflowStatus: workflowStatusOf(internalMetaOrNil(depMetas, dep.DepIssueID)),
			ProjectId:      issue.ProjectID,
			SequenceNumber: issue.SequenceNumber,
			Reference:      buildIssueReference(issue),
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
