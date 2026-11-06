package repository

// INT-209/INT-212 搜索延迟基准：万级 issue 夹具上测带 q 的列表 p50/p95，
// 用于 LIKE 全表扫描与 FTS 改造、count/list 合并的前后对比。常规
// `go test ./...` 不跑基准，复测命令：
//
//	go test ./internal/repository -run '^$' -bench BenchmarkIssueSearchLatency -benchtime 1x -v
//
// 夹具确定性（固定种子）：10k issue，正文长度接近真实分布（60% 短文
// 200–800B、30% 中篇 1–3KB、10% 长文 4–15KB，中英混排 + 日志块），
// 约一半挂 GitHub meta。基准复刻生产连接形状（临时文件库 + WAL +
// synchronous(NORMAL)），测生产列表路径 ListFilteredWithTotal 一次完整列表。

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// benchQueries 是固定查询集：覆盖英文词、中文词、带空格短语、reference
// 前缀、2 字符（始终走 LIKE 退化分支）、纯数字（追加序号精确匹配）、作者名。
var benchQueries = []string{
	"webhook",           // 英文，低频命中
	"retry",             // 英文，中频命中
	"超时",                // 中文，中频命中
	"数据库连接",             // 中文短语，低频命中
	"release checklist", // 带空格短语
	"INT-2",             // reference 前缀
	"ab",                // 2 字符（改造后仍走 LIKE）
	"404",               // 纯数字（追加序号精确匹配）
	"shipbobo",          // 作者名
}

// benchSeedIssues 生成确定性的 10k issue 夹具。
func benchSeedIssues(tb testing.TB, db *gorm.DB, projectID string, count int) {
	tb.Helper()
	rng := rand.New(rand.NewSource(209))

	titleWordsA := []string{"fix", "feat", "chore", "refactor", "webhook", "release", "search", "sync", "deploy", "review", "issue", "导入", "导出", "同步", "构建", "部署", "审查", "登录", "缓存"}
	titleWordsB := []string{"fails", "slow", "broken", "missing", "timeout", "duplicate", "crash", "超时", "失败", "异常", "丢失", "卡顿", "报错", "重复", "错位"}
	bodyOpeners := []string{
		"## 现状\n\n用户在同步 GitHub 仓库时遇到 webhook 推送失败，重试三次仍返回 404。",
		"## 背景\n\n列表页在数据量增大后明显变慢，数据库连接池经常打满，请求排队超时。",
		"## Steps to reproduce\n\n1. Open the release checklist page\n2. Trigger a retry\n3. Watch the deploy log stream",
		"## 现象\n\n搜索关键词后结果缺失，疑似中文分词或大小写折叠问题，重新构建后偶发恢复。",
		"When importing issues from the upstream repo, labels and assignees are dropped silently and the sync marks itself done.",
	}
	logLine := []string{
		"2026-01-02T03:04:05Z ERROR http_status=404 path=/api/projects/x/issues retry_after=30s",
		"2026-02-03T04:05:06Z WARN  db_pool waiting=12 busy_timeout=30000 connection=tenant-9",
		"panic: runtime error: index out of range [5] with length 5\ngoroutine 42 [running]:",
		"2026-03-04T05:06:07Z INFO  sync finished issues=128 comments=912 duration=41.3s errors=0",
		"GitHub-API rate limit remaining=12 reset_in=1180s resource=core token=***",
	}
	closers := []string{
		"\n\n---\n来源：用户反馈\n",
		"\n\n接受标准：重试后不丢数据，日志里能看到 request_id。\n",
		"\n\nRelated: INT-2, GH-404, docs/internal/sync.md\n",
		"\n\n<!-- synced from github, do not edit -->\n",
	}

	makeBody := func(seq int) string {
		var sb strings.Builder
		sb.WriteString(bodyOpeners[rng.Intn(len(bodyOpeners))])
		sizes := rng.Intn(10)
		var target int
		switch {
		case sizes < 6:
			target = 200 + rng.Intn(600)
		case sizes < 9:
			target = 1000 + rng.Intn(2000)
		default:
			target = 4000 + rng.Intn(11000)
		}
		for sb.Len() < target {
			if rng.Intn(9) == 0 {
				sb.WriteString("\n```\n")
				for i := 0; i < 4+rng.Intn(10); i++ {
					sb.WriteString(logLine[rng.Intn(len(logLine))])
					sb.WriteByte('\n')
				}
				sb.WriteString("```\n")
			} else {
				sb.WriteString("段落 ")
				fmt.Fprintf(&sb, "#%d", seq)
				sb.WriteString("：")
				sb.WriteString(titleWordsA[rng.Intn(len(titleWordsA))])
				sb.WriteString(" / ")
				sb.WriteString(titleWordsB[rng.Intn(len(titleWordsB))])
				sb.WriteString("，webhook retry 超时后数据库连接重置，release checklist 待更新。")
				if rng.Intn(5) == 0 {
					sb.WriteString("登陆页 404。")
				}
				sb.WriteByte('\n')
			}
			if rng.Intn(11) == 0 {
				sb.WriteString(closers[rng.Intn(len(closers))])
			}
		}
		return sb.String()
	}

	now := time.Now().UTC()
	batch := make([]model.Issue, 0, 500)
	for i := 1; i <= count; i++ {
		isGH := i%2 == 0
		author := "shipbobo"
		if i%3 == 0 {
			author = "agent-bot"
		} else if i%7 == 0 {
			author = "mirror-sync"
		}
		issue := model.Issue{
			ID:             fmt.Sprintf("bench-%06d", i),
			ProjectID:      projectID,
			Source:         model.IssueSourceInternal,
			SequenceNumber: i,
			State:          model.IssueStateOpen,
			Title:          fmt.Sprintf("%s: %s issue #%d (%s)", titleWordsA[rng.Intn(len(titleWordsA))], titleWordsB[rng.Intn(len(titleWordsB))], i, titleWordsA[rng.Intn(len(titleWordsA))]),
			Body:           makeBody(i),
			AuthorLogin:    author,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if isGH {
			issue.Source = model.IssueSourceGitHub
		}
		batch = append(batch, issue)
		if len(batch) == 500 {
			if err := db.CreateInBatches(batch, 500).Error; err != nil {
				tb.Fatalf("seed issues: %v", err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := db.CreateInBatches(batch, 500).Error; err != nil {
			tb.Fatalf("seed issues: %v", err)
		}
	}

	// 约一半挂 GitHub meta（真实项目混合内部与同步 issue）。
	metas := make([]model.IssueGitHubMeta, 0, 500)
	for i := 2; i <= count; i += 2 {
		metas = append(metas, model.IssueGitHubMeta{
			IssueID:       fmt.Sprintf("bench-%06d", i),
			ProjectID:     projectID,
			GitHubIssueID: int64(100000 + i),
			Number:        i,
			HTMLURL:       fmt.Sprintf("https://github.com/x/y/issues/%d", i),
			SyncedAt:      now,
		})
	}
	if err := db.CreateInBatches(metas, 500).Error; err != nil {
		tb.Fatalf("seed github meta: %v", err)
	}
}

// setupIssueSearchBenchDB 打开与生产连接形状一致的临时文件库并灌入夹具。
func setupIssueSearchBenchDB(tb testing.TB, count int) (*IssueRepository, string) {
	tb.Helper()
	dsn := "file:" + filepath.Join(tb.TempDir(), "bench.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(30000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		tb.Fatalf("open bench db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueGitHubMeta{}, &model.IssueInternalMeta{}); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	// INT-209：与生产启动顺序一致，AutoMigrate 之后补齐 FTS 派生索引（本次唯一
	// 改动点：基准文件原缺这一步，≥3 rune 查询会因 issues_fts 不存在而 Fatalf；
	// 夹具与查询集保持不变）。
	if err := EnsureIssueFTSSchema(db); err != nil {
		tb.Fatalf("ensure issue fts: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.User{ID: "bench-user-0001", Username: "bench-user", Email: "bench@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		tb.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{ID: "bench-project-0001", UserID: "bench-user-0001", Name: "bench", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		tb.Fatalf("seed project: %v", err)
	}
	projectID := "bench-project-0001"
	benchSeedIssues(tb, db, projectID, count)
	return &IssueRepository{db: db}, projectID
}

// percentile 对已排序样本取分位（0~1）。
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func BenchmarkIssueSearchLatency(b *testing.B) {
	repo, projectID := setupIssueSearchBenchDB(b, 10000)

	// 每个查询固定迭代次数，收集毫秒耗时后报 p50/p95。
	iters := 40
	type result struct {
		query    string
		hits     int64
		p50, p95 float64
	}
	results := make([]result, 0, len(benchQueries))

	for _, q := range benchQueries {
		listQuery := IssueListQuery{ProjectID: projectID, Query: q}
		latencies := make([]float64, 0, iters)
		var hits int64
		for i := 0; i < iters; i++ {
			start := time.Now()
			issues, total, err := repo.ListFilteredWithTotal(listQuery, "updated_desc", 0, 20)
			if err != nil {
				b.Fatalf("list %q: %v", q, err)
			}
			if i == 0 {
				hits = total
				if len(issues) > 20 {
					b.Fatalf("limit not applied")
				}
			}
			latencies = append(latencies, float64(time.Since(start).Microseconds())/1000.0)
		}
		sort.Float64s(latencies)
		results = append(results, result{
			query: q,
			hits:  hits,
			p50:   percentile(latencies, 0.50),
			p95:   percentile(latencies, 0.95),
		})
	}

	// 无关键词列表作为对照（不应被搜索改造拖慢）。
	listOnly := IssueListQuery{ProjectID: projectID}
	latencies := make([]float64, 0, iters)
	for i := 0; i < iters; i++ {
		start := time.Now()
		if _, _, err := repo.ListFilteredWithTotal(listOnly, "updated_desc", 0, 20); err != nil {
			b.Fatalf("list baseline: %v", err)
		}
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000.0)
	}
	sort.Float64s(latencies)
	results = append(results, result{query: "(no q)", p50: percentile(latencies, 0.50), p95: percentile(latencies, 0.95)})

	for _, r := range results {
		b.Logf("q=%-20q hits=%-6d p50=%8.2fms p95=%8.2fms", r.query, r.hits, r.p50, r.p95)
	}
}
