package service

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"gorm.io/gorm"
)

// INT-96 回归：sequence_number 的 MAX+1 取号必须与 INSERT 同事务。生产
// MaxOpenConns=1 让事务整体串行，取号在事务外时并发创建会读到同一个 MAX、
// 撞 idx_issues_project_sequence 被映射成 500。这里把测试库压成同样的
// 单连接池，断言并发创建全部成功且序号唯一连续。
func TestCreateInternalIssue_ConcurrentCreatesGetUniqueSequences(t *testing.T) {
	svc := setupTestServices(t)
	limitPoolToSingleConn(t, svc)
	user := createTestUser(t, svc.db, "user-seq-conc")
	project := createTestProject(t, svc.db, user.ID)

	const n = 12
	results := runConcurrently(n, func(i int) error {
		_, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
			Title: fmt.Sprintf("并发创建 %d", i),
		})
		return err
	})
	for i, err := range results {
		if err != nil {
			t.Fatalf("concurrent create %d failed: %v", i, err)
		}
	}

	assertProjectSequences(t, svc, project.ID, n)
}

// 同一项目下 GitHub 镜像落库与手动创建并发：两条取号路径交叠时同样不能撞号。
func TestIssueMirrorUpsert_ConcurrentWithManualCreate(t *testing.T) {
	svc := setupTestServices(t)
	limitPoolToSingleConn(t, svc)
	user := createTestUser(t, svc.db, "user-seq-mirror")
	project := createTestProject(t, svc.db, user.ID)

	const mirrorN = 6
	const manualN = 6
	now := time.Now().UTC()

	results := runConcurrently(mirrorN+manualN, func(i int) error {
		if i%2 == 0 {
			n := i / 2
			item := &ghclient.Issue{Issue: gh.Issue{
				ID:        gh.Int64(int64(7000 + n)),
				Number:    gh.Int(700 + n),
				State:     gh.String("open"),
				Title:     gh.String(fmt.Sprintf("镜像问题 %d", n)),
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				User:      &gh.User{Login: gh.String("alice")},
			}}
			_, err := svc.issueService.mirror.upsertGitHubIssue(project.ID, item)
			return err
		}
		_, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
			Title: fmt.Sprintf("手动创建 %d", i/2),
		})
		return err
	})
	for i, err := range results {
		if err != nil {
			t.Fatalf("concurrent write %d failed: %v", i, err)
		}
	}

	assertProjectSequences(t, svc, project.ID, mirrorN+manualN)
}

// 同一 GitHub issue 并发首见：事务外的 meta 查询只是快照，并发方可能已抢先
// 落库——事务内必须复核身份转更新路径，否则留下无 meta 的孤儿行并双发事件。
func TestIssueMirrorUpsert_ConcurrentSameGitHubIssueIsDeduplicated(t *testing.T) {
	svc := setupTestServices(t)
	limitPoolToSingleConn(t, svc)
	user := createTestUser(t, svc.db, "user-seq-same")
	sink := &countingIssueEventSink{}
	svc.issueService.SetIssueEventSink(sink)

	const rounds = 6
	for r := 0; r < rounds; r++ {
		project := createTestProject(t, svc.db, user.ID)
		now := time.Now().UTC()
		itemFor := func() *ghclient.Issue {
			return &ghclient.Issue{Issue: gh.Issue{
				ID:        gh.Int64(4242),
				Number:    gh.Int(42),
				State:     gh.String("open"),
				Title:     gh.String("同一个 GitHub issue"),
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				User:      &gh.User{Login: gh.String("alice")},
			}}
		}
		results := runConcurrently(2, func(i int) error {
			_, err := svc.issueService.mirror.upsertGitHubIssue(project.ID, itemFor())
			return err
		})
		for i, err := range results {
			if err != nil {
				t.Fatalf("round %d upsert %d failed: %v", r, i, err)
			}
		}

		var issueCount, metaCount int64
		if err := svc.db.Model(&model.Issue{}).Where("project_id = ?", project.ID).Count(&issueCount).Error; err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if issueCount != 1 {
			t.Fatalf("round %d: expected exactly 1 issue row for same github id, got %d", r, issueCount)
		}
		if err := svc.db.Model(&model.IssueGitHubMeta{}).Where("project_id = ?", project.ID).Count(&metaCount).Error; err != nil {
			t.Fatalf("count metas: %v", err)
		}
		if metaCount != 1 {
			t.Fatalf("round %d: expected exactly 1 github meta row, got %d", r, metaCount)
		}
		var meta model.IssueGitHubMeta
		if err := svc.db.Where("project_id = ?", project.ID).First(&meta).Error; err != nil {
			t.Fatalf("load meta: %v", err)
		}
		var issue model.Issue
		if err := svc.db.Where("project_id = ?", project.ID).First(&issue).Error; err != nil {
			t.Fatalf("load issue: %v", err)
		}
		if meta.IssueID != issue.ID {
			t.Fatalf("round %d: meta points at %q but sole issue is %q", r, meta.IssueID, issue.ID)
		}
		if issue.SequenceNumber != 1 {
			t.Fatalf("round %d: sole issue has sequence_number %d, want 1", r, issue.SequenceNumber)
		}
	}

	if got := sink.createdCount(); got != rounds {
		t.Fatalf("expected %d issue_created events (one per project), got %d", rounds, got)
	}
}

type countingIssueEventSink struct {
	mu      sync.Mutex
	created int
}

func (s *countingIssueEventSink) EnqueueIssueEventTx(_ *gorm.DB, event IssueEvent) error {
	if event.Type == model.AutomationTriggerIssueCreated {
		s.mu.Lock()
		s.created++
		s.mu.Unlock()
	}
	return nil
}

func (s *countingIssueEventSink) createdCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

// runConcurrently 用统一起点栅栏把 n 个任务尽量压到同一时刻发出，按任务序号收错。
func runConcurrently(n int, fn func(i int) error) []error {
	results := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

// assertProjectSequences 断言项目下恰有 want 个 issue 且序号正好是 1..want。
// 只 Pluck sequence_number 单列，不读正文。
func assertProjectSequences(t *testing.T, svc *testServices, projectID string, want int) {
	t.Helper()

	seqs := make([]int, 0, want)
	if err := svc.db.Model(&model.Issue{}).
		Where("project_id = ?", projectID).
		Pluck("sequence_number", &seqs).Error; err != nil {
		t.Fatalf("pluck sequences: %v", err)
	}
	if len(seqs) != want {
		t.Fatalf("expected %d issues, got %d", want, len(seqs))
	}
	sort.Ints(seqs)
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("expected unique consecutive sequences 1..%d, got %v", want, seqs)
		}
	}
}
