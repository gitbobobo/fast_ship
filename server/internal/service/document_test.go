package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupDocumentServiceTest(t *testing.T) (*DocumentService, *gorm.DB, string, string) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Document{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE INDEX IF NOT EXISTS idx_documents_project_parent ON documents(project_id, parent_id)")

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: userID, Username: "docuser", Email: "doc@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "docproj", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	docRepo := repository.NewDocumentRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	return NewDocumentService(docRepo, projectRepo), db, userID, projectID
}

func TestDocumentService_CreateListGetUpdateMoveDelete(t *testing.T) {
	svc, _, userID, projectID := setupDocumentServiceTest(t)

	root, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: " Root ", Body: "hello"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if root.Title != "Root" || root.Body != "hello" || root.ParentID != nil {
		t.Fatalf("unexpected root: %+v", root)
	}

	child, err := svc.Create(projectID, userID, &CreateDocumentRequest{
		Title:    "Child",
		ParentID: &root.ID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("unexpected child parent: %+v", child)
	}

	list, err := svc.List(projectID, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("unexpected list: %+v", list)
	}
	raw, err := json.Marshal(list.Items[0])
	if err != nil {
		t.Fatalf("marshal list item: %v", err)
	}
	if strings.Contains(string(raw), `"body"`) {
		t.Fatalf("list item should not contain body key: %s", raw)
	}

	detail, err := svc.Get(root.ID, userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if detail.Body != "hello" {
		t.Fatalf("expected body, got %q", detail.Body)
	}

	empty := ""
	updated, err := svc.Update(root.ID, userID, &UpdateDocumentRequest{Body: &empty, Title: strPtr("Root2")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "Root2" || updated.Body != "" {
		t.Fatalf("unexpected update: %+v", updated)
	}

	var rootParent **string
	nilParent := (*string)(nil)
	rootParent = &nilParent
	moved, err := svc.Update(child.ID, userID, &UpdateDocumentRequest{ParentID: rootParent})
	if err != nil {
		t.Fatalf("move to root: %v", err)
	}
	if moved.ParentID != nil {
		t.Fatalf("expected nil parent after move to root, got %+v", moved.ParentID)
	}

	parentRef := &root.ID
	parentPtr := &parentRef
	if _, err := svc.Update(child.ID, userID, &UpdateDocumentRequest{ParentID: parentPtr}); err != nil {
		t.Fatalf("move under root: %v", err)
	}
	emptyParent := ""
	emptyPtr := &emptyParent
	emptyOuter := &emptyPtr
	movedEmpty, err := svc.Update(child.ID, userID, &UpdateDocumentRequest{ParentID: emptyOuter})
	if err != nil {
		t.Fatalf("empty parent as root: %v", err)
	}
	if movedEmpty.ParentID != nil {
		t.Fatalf("expected nil parent for empty string, got %+v", movedEmpty.ParentID)
	}

	parentPtr2 := &parentRef
	if _, err := svc.Update(child.ID, userID, &UpdateDocumentRequest{ParentID: parentPtr2}); err != nil {
		t.Fatalf("reattach under root: %v", err)
	}

	if err := svc.Delete(root.ID, userID); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	if _, err := svc.Get(child.ID, userID); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("expected child cascade not found, got %v", err)
	}
	if _, err := svc.Get(root.ID, userID); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("expected root not found, got %v", err)
	}
}

func TestDocumentService_ValidationAndAccess(t *testing.T) {
	svc, db, userID, projectID := setupDocumentServiceTest(t)

	if _, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: "   "}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: strings.Repeat("甲", 201)}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("long title: %v", err)
	}
	if _, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: "ok", Body: strings.Repeat("乙", 200_001)}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("long body: %v", err)
	}
	if _, err := svc.Update(uuid.NewString(), userID, &UpdateDocumentRequest{}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("empty update: %v", err)
	}

	root, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: "Root"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: "Child", ParentID: &root.ID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	childRef := &child.ID
	childPtr := &childRef
	if _, err := svc.Update(root.ID, userID, &UpdateDocumentRequest{ParentID: childPtr}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("cycle should fail: %v", err)
	}

	selfRef := &root.ID
	selfPtr := &selfRef
	if _, err := svc.Update(root.ID, userID, &UpdateDocumentRequest{ParentID: selfPtr}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("self parent should fail: %v", err)
	}

	otherProjectID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.Project{ID: otherProjectID, UserID: userID, Name: "other", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherDoc, err := svc.Create(otherProjectID, userID, &CreateDocumentRequest{Title: "Other"})
	if err != nil {
		t.Fatalf("create other doc: %v", err)
	}
	otherRef := &otherDoc.ID
	otherPtr := &otherRef
	if _, err := svc.Update(root.ID, userID, &UpdateDocumentRequest{ParentID: otherPtr}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("cross project parent should fail: %v", err)
	}
	missing := uuid.NewString()
	missingRef := &missing
	missingPtr := &missingRef
	if _, err := svc.Update(root.ID, userID, &UpdateDocumentRequest{ParentID: missingPtr}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("missing parent should fail: %v", err)
	}

	otherUser := uuid.NewString()
	if _, err := svc.List(projectID, otherUser); !errors.Is(err, errs.ErrProjectNotFound) {
		t.Fatalf("list other user: %v", err)
	}
	if _, err := svc.Get(root.ID, otherUser); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("get other user: %v", err)
	}
	if _, err := svc.Get(uuid.NewString(), userID); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	title := "x"
	if _, err := svc.Update(root.ID, otherUser, &UpdateDocumentRequest{Title: &title}); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("update other user: %v", err)
	}
	if err := svc.Delete(root.ID, otherUser); !errors.Is(err, errs.ErrDocumentNotFound) {
		t.Fatalf("delete other user: %v", err)
	}
}

func TestDocumentService_ProjectCapacityLimit(t *testing.T) {
	svc, db, userID, projectID := setupDocumentServiceTest(t)
	now := time.Now()
	for i := 0; i < maxDocumentsPerProject; i++ {
		doc := model.Document{
			ID:        uuid.NewString(),
			ProjectID: projectID,
			Title:     "d",
			Body:      "",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := db.Create(&doc).Error; err != nil {
			t.Fatalf("seed doc: %v", err)
		}
	}
	if _, err := svc.Create(projectID, userID, &CreateDocumentRequest{Title: "overflow"}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("capacity limit: %v", err)
	}
}

func strPtr(v string) *string { return &v }
