package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// createT3TestEnv 直接落一条环境记录（不走配对流）：仅当用例不需要真实上游时
// 使用——Address 指到不可达端口，探测路径自然归 unreachable。
func createT3TestEnv(t *testing.T, db *gorm.DB, userID, name string) *model.T3Environment {
	t.Helper()
	encrypted, err := crypto.Encrypt([]byte("bearer-"+name), []byte(testEncryptionKey))
	if err != nil {
		t.Fatalf("encrypt bearer: %v", err)
	}
	env := &model.T3Environment{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Name:                 name,
		Address:              "http://127.0.0.1:1/",
		BearerTokenEncrypted: encrypted,
		BearerExpiresAt:      time.Now().Add(time.Hour),
		BearerScope:          "orchestration:read orchestration:operate",
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}
	if err := db.Create(env).Error; err != nil {
		t.Fatalf("create t3 env: %v", err)
	}
	return env
}

type t3BindingTestEnv struct {
	db   *gorm.DB
	cfg  *config.Config
	env  *T3EnvironmentService
	bind *T3BindingService
	repo *repository.ProjectT3BindingRepository
	fake *fakeT3Environment
}

func setupT3BindingTest(t *testing.T) *t3BindingTestEnv {
	t.Helper()

	fake := newFakeT3Environment(t)
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	fake.baseURL = server.URL

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.UserGitHubToken{},
		&model.Project{},
		&model.T3Environment{},
		&model.ProjectT3Binding{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_t3_environments_user_name ON t3_environments(user_id, name)")

	cfg := &config.Config{
		JWT: config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}
	envRepo := repository.NewT3EnvironmentRepository(db)
	hub := NewT3ShellHub(zap.NewNop())
	t.Cleanup(hub.Close)
	th := NewT3ThreadsService(envRepo, cfg, zap.NewNop(), hub)
	// 测试预算：秒级封顶，坏环境的隔离不需要真等默认预算。
	th.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 2 * time.Second, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 2 * time.Second}
	repo := repository.NewProjectT3BindingRepository(db)
	return &t3BindingTestEnv{
		db:   db,
		cfg:  cfg,
		env:  NewT3EnvironmentService(envRepo, cfg, zap.NewNop(), hub),
		bind: NewT3BindingService(th, repository.NewProjectRepository(db), repo),
		repo: repo,
		fake: fake,
	}
}

// pairFakeEnv 走完配对流建一个指向 fake 的环境（bearer 真实加密落库）。
func (e *t3BindingTestEnv) pairFakeEnv(t *testing.T, ctx context.Context, userID, name string) string {
	t.Helper()
	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: name, PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("pair env %s: %v", name, err)
	}
	return env.Id
}

func TestT3BindingDiscoverProjects(t *testing.T) {
	e := setupT3BindingTest(t)
	userID := createTestUser(t, e.db, "binding-discover-user").ID
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
		map[string]any{"id": "p-2", "title": "Beta"},
	}})
	e.fake.mu.Unlock()
	okEnvID := e.pairFakeEnv(t, ctx, userID, "健康机")
	deadEnv := createT3TestEnv(t, e.db, userID, "坏机")

	items, err := e.bind.DiscoverProjects(ctx, userID)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 env items, got %d: %#v", len(items), items)
	}
	byEnv := map[string]*T3EnvironmentProjects{}
	for i := range items {
		byEnv[items[i].EnvironmentId] = &items[i]
	}
	okItem := byEnv[okEnvID]
	if okItem == nil || okItem.Status != T3EnvironmentStatusOK || okItem.Access != T3EnvironmentAccessOperate {
		t.Fatalf("unexpected healthy env item: %#v", items)
	}
	if len(okItem.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %#v", okItem.Projects)
	}
	if okItem.Projects[0].Id != "p-1" || *okItem.Projects[0].WorkspaceRoot != "/ws/alpha" {
		t.Fatalf("unexpected project: %#v", okItem.Projects[0])
	}
	if okItem.Projects[1].WorkspaceRoot != nil {
		t.Fatalf("expected omitted workspace_root for p-2, got: %#v", okItem.Projects[1])
	}
	// 项目发现零 shell 调用（INT-116）：订阅类 RPC 不出现。
	for _, rpc := range e.fake.recordedRPC() {
		switch rpc.method {
		case t3RPCSubscribeShell, t3RPCSubscribeArchivedShell:
			t.Fatalf("discovery must not subscribe shell, got %s", rpc.method)
		}
	}
	// 单环境失败只标自己：status/error 承载原因，projects 恒为空数组。
	deadItem := byEnv[deadEnv.ID]
	if deadItem == nil || deadItem.Status != T3EnvironmentStatusUnreachable {
		t.Fatalf("unexpected dead env item: %#v", items)
	}
	if deadItem.Error == nil || len(deadItem.Projects) != 0 {
		t.Fatalf("expected error + empty projects for dead env, got: %#v", deadItem)
	}
}

func TestT3BindingDiscoverProjectsEmpty(t *testing.T) {
	e := setupT3BindingTest(t)
	userID := createTestUser(t, e.db, "binding-discover-empty").ID

	items, err := e.bind.DiscoverProjects(context.Background(), userID)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items for user without envs, got %#v", items)
	}
}

// 归档面失败与项目发现无关（INT-116：发现走 `/api/projects`，零 shell 调用）：
// 项目 HTTP 成功即 ok，不再有「归档失败导致发现降级」的契约。
func TestT3BindingDiscoverIndependentOfArchive(t *testing.T) {
	e := setupT3BindingTest(t)
	userID := createTestUser(t, e.db, "binding-discover-archive").ID
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
	}})
	e.fake.archiveFail = true // 归档订阅持续失败，也影响不到项目发现
	e.fake.mu.Unlock()
	e.pairFakeEnv(t, ctx, userID, "归档坏机")

	items, err := e.bind.DiscoverProjects(ctx, userID)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 1 || items[0].Status != T3EnvironmentStatusOK {
		t.Fatalf("project HTTP success must be ok regardless of archive, got %#v", items)
	}
	if len(items[0].Projects) != 1 || items[0].Projects[0].Id != "p-1" {
		t.Fatalf("expected projects from /api/projects, got %#v", items[0].Projects)
	}
}

func TestT3BindingUpsertListDeleteLifecycle(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-lifecycle-user")
	project := createTestProject(t, e.db, user.ID)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
		map[string]any{"id": "p-2", "title": "Beta", "workspaceRoot": "/ws/beta"},
	}})
	e.fake.mu.Unlock()
	envID := e.pairFakeEnv(t, ctx, user.ID, "工作站")

	binding, err := e.bind.Upsert(ctx, user.ID, project.ID, envID, "p-1")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if binding.T3ProjectId != "p-1" || binding.T3ProjectTitle != "Alpha" || binding.WorkspaceRoot != "/ws/alpha" {
		t.Fatalf("unexpected binding snapshot: %#v", binding)
	}
	if binding.EnvironmentName != "工作站" || binding.EnvironmentStatus != T3EnvironmentStatusOK {
		t.Fatalf("unexpected env echo: %#v", binding)
	}

	items, err := e.bind.List(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].EnvironmentId != envID || items[0].T3ProjectId != "p-1" {
		t.Fatalf("unexpected list: %#v", items)
	}

	// 同 (project_id, environment_id) 重复 PUT 覆盖快照，列表仍一条。
	stored, err := e.repo.FindByID(ctx, project.ID, envID)
	if err != nil {
		t.Fatalf("load stored binding: %v", err)
	}
	firstCreated := stored.CreatedAt
	time.Sleep(10 * time.Millisecond)

	rebound, err := e.bind.Upsert(ctx, user.ID, project.ID, envID, "p-2")
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if rebound.T3ProjectId != "p-2" || rebound.T3ProjectTitle != "Beta" {
		t.Fatalf("unexpected rebound snapshot: %#v", rebound)
	}
	items, err = e.bind.List(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("list after rebind: %v", err)
	}
	if len(items) != 1 || items[0].T3ProjectId != "p-2" {
		t.Fatalf("expected overwrite to single binding, got %#v", items)
	}
	stored, err = e.repo.FindByID(ctx, project.ID, envID)
	if err != nil {
		t.Fatalf("reload binding: %v", err)
	}
	if !stored.CreatedAt.Equal(firstCreated) {
		t.Fatalf("expected created_at preserved across upsert, before=%s after=%s", firstCreated, stored.CreatedAt)
	}

	if err := e.bind.Delete(ctx, user.ID, project.ID, envID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 已解绑时重复 DELETE 幂等成功。
	if err := e.bind.Delete(ctx, user.ID, project.ID, envID); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	items, err = e.bind.List(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty bindings, got %#v", items)
	}
}

func TestT3BindingUpsertValidation(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-upsert-user")
	other := createTestUser(t, e.db, "binding-upsert-other")
	project := createTestProject(t, e.db, user.ID)
	otherProject := createTestProject(t, e.db, other.ID)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
		map[string]any{"id": "p-no-root", "title": "NoRoot"},
	}})
	e.fake.mu.Unlock()
	envID := e.pairFakeEnv(t, ctx, user.ID, "工作站")
	otherEnvID := e.pairFakeEnv(t, ctx, other.ID, "他人机")
	deadEnv := createT3TestEnv(t, e.db, user.ID, "坏机")

	cases := []struct {
		name      string
		projectID string
		envID     string
		t3Project string
		wantCode  int
	}{
		{"empty t3_project_id", project.ID, envID, "  ", 40001},
		{"project not found", "no-project", envID, "p-1", 40401},
		{"cross-user project", otherProject.ID, envID, "p-1", 40401},
		{"env not found", project.ID, "no-env", "p-1", 40417},
		{"cross-user env", project.ID, otherEnvID, "p-1", 40417},
		{"env unreachable", project.ID, deadEnv.ID, "p-1", 50202},
		{"t3 project not in env", project.ID, envID, "p-missing", 40401},
		{"t3 project without workspaceRoot", project.ID, envID, "p-no-root", 40401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.bind.Upsert(ctx, user.ID, tc.projectID, tc.envID, tc.t3Project)
			if code := t3ErrCode(err); code != tc.wantCode {
				t.Fatalf("expected code %d, got err %v (code %d)", tc.wantCode, err, code)
			}
			// 不保存未校验的绑定。
			if _, findErr := e.repo.FindByID(ctx, tc.projectID, tc.envID); !errors.Is(findErr, gorm.ErrRecordNotFound) {
				t.Fatalf("expected no binding persisted, got err %v", findErr)
			}
		})
	}
}

// 绑定校验走 `/api/projects`：归档面失败与绑定无关；项目表就是完整事实，
// 不存在即 40401（INT-116 起不再有「归档不可信不下结论」的灰区）。
func TestT3BindingUpsertIndependentOfArchive(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-archive-user")
	project := createTestProject(t, e.db, user.ID)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
	}})
	e.fake.archiveFail = true
	e.fake.mu.Unlock()
	envID := e.pairFakeEnv(t, ctx, user.ID, "归档坏")

	// 归档订阅持续失败不阻断绑定。
	if _, err := e.bind.Upsert(ctx, user.ID, project.ID, envID, "p-1"); err != nil {
		t.Fatalf("expected upsert to succeed regardless of archive failure: %v", err)
	}
	if err := e.bind.Delete(ctx, user.ID, project.ID, envID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 项目不在项目表：40401（不再是 50202）。
	_, err := e.bind.Upsert(ctx, user.ID, project.ID, envID, "p-missing")
	if code := t3ErrCode(err); code != 40401 {
		t.Fatalf("expected 40401 for project absent from /api/projects, got err %v (code %d)", err, code)
	}
}

func TestT3BindingListProbeStatuses(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-list-user")
	project := createTestProject(t, e.db, user.ID)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
	}})
	e.fake.mu.Unlock()
	okEnvID := e.pairFakeEnv(t, ctx, user.ID, "健康机")
	deadEnv := createT3TestEnv(t, e.db, user.ID, "坏机")

	if _, err := e.bind.Upsert(ctx, user.ID, project.ID, okEnvID, "p-1"); err != nil {
		t.Fatalf("upsert healthy: %v", err)
	}
	// 坏机绑定无法实时校验——直接落库模拟「绑定后环境变坏」的存量数据。
	deadBinding := &model.ProjectT3Binding{
		ProjectID: project.ID, EnvironmentID: deadEnv.ID,
		T3ProjectID: "p-x", T3ProjectTitle: "Gone", WorkspaceRoot: "/ws/gone",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := e.repo.Upsert(ctx, deadBinding); err != nil {
		t.Fatalf("seed dead binding: %v", err)
	}

	items, err := e.bind.List(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 bindings, got %#v", items)
	}
	// environment_id ASC：坏机 ID 与健康机 ID 顺序不定，按 ID 查找断言。
	byEnv := map[string]struct {
		status T3EnvironmentStatus
		name   string
	}{}
	for _, item := range items {
		byEnv[item.EnvironmentId] = struct {
			status T3EnvironmentStatus
			name   string
		}{item.EnvironmentStatus, item.EnvironmentName}
	}
	if byEnv[okEnvID].status != T3EnvironmentStatusOK || byEnv[okEnvID].name != "健康机" {
		t.Fatalf("expected healthy binding ok, got %#v", items)
	}
	if byEnv[deadEnv.ID].status != T3EnvironmentStatusUnreachable || byEnv[deadEnv.ID].name != "坏机" {
		t.Fatalf("expected dead binding unreachable with env name, got %#v", items)
	}
}

func TestT3BindingListProjectNotFound(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-list-404")
	ctx := context.Background()

	if _, err := e.bind.List(ctx, user.ID, "no-project"); !errors.Is(err, errs.ErrProjectNotFound) {
		t.Fatalf("expected 40401, got %v", err)
	}
	if err := e.bind.Delete(ctx, user.ID, "no-project", "no-env"); !errors.Is(err, errs.ErrProjectNotFound) {
		t.Fatalf("expected 40401 on delete, got %v", err)
	}
}

func TestT3BindingCascade(t *testing.T) {
	e := setupT3BindingTest(t)
	user := createTestUser(t, e.db, "binding-cascade-user")
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Alpha", "workspaceRoot": "/ws/alpha"},
	}})
	e.fake.mu.Unlock()
	envID := e.pairFakeEnv(t, ctx, user.ID, "工作站")

	// 删项目 → 绑定级联删除。
	project := createTestProject(t, e.db, user.ID)
	if _, err := e.bind.Upsert(ctx, user.ID, project.ID, envID, "p-1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := e.db.Delete(&model.Project{}, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := e.repo.FindByID(ctx, project.ID, envID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected binding cascaded on project delete, got %v", err)
	}

	// 删环境 → 绑定级联删除。
	project2 := createTestProject(t, e.db, user.ID)
	if _, err := e.bind.Upsert(ctx, user.ID, project2.ID, envID, "p-1"); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if err := e.db.Delete(&model.T3Environment{}, "id = ?", envID).Error; err != nil {
		t.Fatalf("delete env: %v", err)
	}
	if _, err := e.repo.FindByID(ctx, project2.ID, envID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected binding cascaded on env delete, got %v", err)
	}
}
