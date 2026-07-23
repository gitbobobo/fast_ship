package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxDocumentTitleRunes  = 200
	maxDocumentBodyRunes   = 200_000
	maxDocumentsPerProject = 2000
	maxDocumentParentDepth = 64
)

type DocumentService struct {
	docRepo     *repository.DocumentRepository
	projectRepo *repository.ProjectRepository
}

func NewDocumentService(docRepo *repository.DocumentRepository, projectRepo *repository.ProjectRepository) *DocumentService {
	return &DocumentService{
		docRepo:     docRepo,
		projectRepo: projectRepo,
	}
}

type DocumentListItem struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	ParentID  *string   `json:"parent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DocumentDetail struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	ParentID  *string   `json:"parent_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DocumentListData struct {
	Items []DocumentListItem `json:"items"`
	Total int64              `json:"total"`
}

type CreateDocumentRequest struct {
	Title    string
	Body     string
	ParentID *string
}

type UpdateDocumentRequest struct {
	Title    *string
	Body     *string
	ParentID **string
}

func (s *DocumentService) ensureProjectAccess(projectID, userID string) error {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *DocumentService) loadAccessibleDocument(docID, userID string) (*model.Document, error) {
	doc, err := s.docRepo.FindByID(docID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrDocumentNotFound
		}
		return nil, errs.ErrInternal
	}
	if err := s.ensureProjectAccess(doc.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, errs.ErrDocumentNotFound
		}
		return nil, err
	}
	return doc, nil
}

func validateDocumentTitle(title string) error {
	n := utf8.RuneCountInString(title)
	if n < 1 || n > maxDocumentTitleRunes {
		return errs.ErrInvalidParams
	}
	return nil
}

func validateDocumentBody(body string) error {
	if utf8.RuneCountInString(body) > maxDocumentBodyRunes {
		return errs.ErrInvalidParams
	}
	return nil
}

func (s *DocumentService) validateParentInTx(
	txRepo *repository.DocumentRepository,
	projectID, docID string,
	parentID string,
) error {
	if parentID == docID {
		return errs.ErrInvalidParams
	}
	parent, err := txRepo.FindByID(parentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrInvalidParams
		}
		return errs.ErrInternal
	}
	if parent.ProjectID != projectID {
		return errs.ErrInvalidParams
	}
	if docID == "" {
		return nil
	}
	return ensureNoCycle(txRepo, docID, parentID)
}

func ensureNoCycle(txRepo *repository.DocumentRepository, docID, newParentID string) error {
	current := newParentID
	for i := 0; i < maxDocumentParentDepth; i++ {
		if current == docID {
			return errs.ErrInvalidParams
		}
		node, err := txRepo.FindByID(current)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrInvalidParams
			}
			return errs.ErrInternal
		}
		if node.ParentID == nil {
			return nil
		}
		current = *node.ParentID
	}
	return errs.ErrInvalidParams
}

func toDocumentListItem(doc model.Document) DocumentListItem {
	return DocumentListItem{
		ID:        doc.ID,
		ProjectID: doc.ProjectID,
		ParentID:  doc.ParentID,
		Title:     doc.Title,
		CreatedAt: doc.CreatedAt,
		UpdatedAt: doc.UpdatedAt,
	}
}

func toDocumentDetail(doc model.Document) DocumentDetail {
	return DocumentDetail{
		ID:        doc.ID,
		ProjectID: doc.ProjectID,
		ParentID:  doc.ParentID,
		Title:     doc.Title,
		Body:      doc.Body,
		CreatedAt: doc.CreatedAt,
		UpdatedAt: doc.UpdatedAt,
	}
}

func (s *DocumentService) List(projectID, userID string) (*DocumentListData, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}
	docs, err := s.docRepo.ListByProject(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	items := make([]DocumentListItem, 0, len(docs))
	for _, doc := range docs {
		items = append(items, toDocumentListItem(doc))
	}
	return &DocumentListData{Items: items, Total: int64(len(items))}, nil
}

func (s *DocumentService) Get(docID, userID string) (*DocumentDetail, error) {
	doc, err := s.loadAccessibleDocument(docID, userID)
	if err != nil {
		return nil, err
	}
	d := toDocumentDetail(*doc)
	return &d, nil
}

func (s *DocumentService) Create(projectID, userID string, req *CreateDocumentRequest) (*DocumentDetail, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if err := validateDocumentTitle(title); err != nil {
		return nil, err
	}
	if err := validateDocumentBody(req.Body); err != nil {
		return nil, err
	}

	var created *model.Document
	err := s.docRepo.Transaction(func(txRepo *repository.DocumentRepository) error {
		count, err := txRepo.CountByProject(projectID)
		if err != nil {
			return errs.ErrInternal
		}
		if count >= maxDocumentsPerProject {
			return errs.ErrInvalidParams
		}

		var parentID *string
		if req.ParentID != nil && *req.ParentID != "" {
			if err := s.validateParentInTx(txRepo, projectID, "", *req.ParentID); err != nil {
				return err
			}
			id := *req.ParentID
			parentID = &id
		}

		now := time.Now()
		doc := &model.Document{
			ID:        uuid.New().String(),
			ProjectID: projectID,
			ParentID:  parentID,
			Title:     title,
			Body:      req.Body,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := txRepo.Create(doc); err != nil {
			return errs.ErrInternal
		}
		created = doc
		return nil
	})
	if err != nil {
		return nil, err
	}
	d := toDocumentDetail(*created)
	return &d, nil
}

func (s *DocumentService) Update(docID, userID string, req *UpdateDocumentRequest) (*DocumentDetail, error) {
	if req.Title == nil && req.Body == nil && req.ParentID == nil {
		return nil, errs.ErrInvalidParams
	}
	var titleValue string
	if req.Title != nil {
		titleValue = strings.TrimSpace(*req.Title)
		if err := validateDocumentTitle(titleValue); err != nil {
			return nil, err
		}
	}
	if req.Body != nil {
		if err := validateDocumentBody(*req.Body); err != nil {
			return nil, err
		}
	}

	var updated *model.Document
	err := s.docRepo.Transaction(func(txRepo *repository.DocumentRepository) error {
		doc, err := txRepo.FindByID(docID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrDocumentNotFound
			}
			return errs.ErrInternal
		}
		if err := s.ensureProjectAccess(doc.ProjectID, userID); err != nil {
			if errors.Is(err, errs.ErrProjectNotFound) {
				return errs.ErrDocumentNotFound
			}
			return err
		}

		fields := map[string]interface{}{
			"updated_at": time.Now(),
		}
		if req.Title != nil {
			fields["title"] = titleValue
		}
		if req.Body != nil {
			fields["body"] = *req.Body
		}
		if req.ParentID != nil {
			if *req.ParentID == nil || **req.ParentID == "" {
				fields["parent_id"] = nil
			} else {
				parentID := **req.ParentID
				if err := s.validateParentInTx(txRepo, doc.ProjectID, doc.ID, parentID); err != nil {
					return err
				}
				fields["parent_id"] = parentID
			}
		}

		if err := txRepo.UpdateByMap(doc.ID, fields); err != nil {
			return errs.ErrInternal
		}
		fresh, err := txRepo.FindByID(doc.ID)
		if err != nil {
			return errs.ErrInternal
		}
		updated = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	d := toDocumentDetail(*updated)
	return &d, nil
}

func (s *DocumentService) Delete(docID, userID string) error {
	doc, err := s.loadAccessibleDocument(docID, userID)
	if err != nil {
		return err
	}
	if err := s.docRepo.Delete(doc.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrDocumentNotFound
		}
		return errs.ErrInternal
	}
	return nil
}
