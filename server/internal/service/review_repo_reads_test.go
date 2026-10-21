package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// reviewRepoReadAllowlist 允许在 review_*.go 里读项目行 live 仓库（CodeRepo /
// CodeRepoFullName）的函数。规则见 docs/internal/reviews.md「审查仓库的读取口径」：
//
//   - 写入侧（生成/保存快照，以及必须等于「即将写入的键」的预占预判）读 live；
//   - 运行侧判定读设置行的 repo_full_name 快照；
//   - 展示可读 live（保留用户输入的原样大小写）。
//
// 这条白名单让「没有第四个漏改点」由测试证明，而不是靠下一轮审查去找。
var reviewRepoReadAllowlist = map[string]string{
	"GetSettings":         "展示：设置页回显开启后会审查哪个仓库（原样大小写）",
	"UpdateSettings":      "写入侧：生成待保存的 repo_full_name 快照与事务内复核",
	"reviewPrerequisites": "写入侧：开启前预判 App 是否已安装到候选仓库",
	"reviewTakenBy":       "写入侧：预判开启后是否被同仓库占用（40922）",
	"reviewOccupiers":     "写入侧：总览里预测同仓库占用",
	"ListReviewProjects":  "展示：总览的 owner/repo 字段（原样大小写）",
	"normalizeCodeRepo":   "写入侧：项目更新时的归一收尾",
	"CodeRepo":            "定义本身",
	"CodeRepoFullName":    "定义本身",
}

// TestReviewChainReadsReviewRepoFromSettingsSnapshot 扫 review_*.go，检查 live
// 仓库读取只出现在白名单函数里：运行侧判定一律走 settings.repo_full_name。
func TestReviewChainReadsReviewRepoFromSettingsSnapshot(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	var offenders []string
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "review_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "CodeRepo", "CodeRepoFullName":
					if strings.Contains(path, "model") {
						return true
					}
					seen[fn.Name.Name] = true
					if _, allowed := reviewRepoReadAllowlist[fn.Name.Name]; !allowed {
						offenders = append(offenders, path+": "+fn.Name.Name)
					}
				}
				return true
			})
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("review_*.go 里出现未列入白名单的 live 仓库读取（运行侧判定应读 settings.repo_full_name）:\n%s",
			strings.Join(offenders, "\n"))
	}
	if !seen["reviewTakenBy"] || !seen["reviewPrerequisites"] {
		t.Fatalf("白名单函数未被扫到，扫描逻辑可能失效: %v", seen)
	}
}

// TestListReviewRecordsCanRerunUsesSettingsSnapshot 记录列表的可重跑判定与
// rerun 接口同口径：按设置行的 repo_full_name 快照，而不是项目行 live 仓库。
func TestListReviewRecordsCanRerunUsesSettingsSnapshot(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "records-canrerun")
	// live 代码仓库 = octo/live，设置行快照 = octo/snap（生产不可达的分叉状态，
	// 由 divergeLiveCodeRepo 的同类手法直接改库构造）。
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "octo"
		p.GithubRepo = "public"
		p.CodeGithubOwner = "octo"
		p.CodeGithubRepo = "live"
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID, RepoFullName: "octo/snap", Enabled: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	createTestReviewPR(t, svc.db, user.ID, project.ID, "octo/live", 11, 1)
	createTestReviewPR(t, svc.db, user.ID, project.ID, "octo/snap", 12, 1)

	rs, _ := readyReviewService(t, svc)
	records, err := rs.ListReviewRecords(context.Background(), user.ID, project.ID, 10)
	if err != nil {
		t.Fatalf("ListReviewRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2（历史记录都要保留）", len(records))
	}
	for _, rec := range records {
		wantRerun := rec.RepoFullName == "octo/snap"
		if rec.CanRerun != wantRerun {
			t.Fatalf("repo=%s can_rerun=%v, want %v（判定键应为设置行快照）",
				rec.RepoFullName, rec.CanRerun, wantRerun)
		}
	}
}
