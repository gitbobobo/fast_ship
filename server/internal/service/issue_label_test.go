package service

import (
	"context"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	gh "github.com/google/go-github/v62/github"
)

func TestExtractLabelNames(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "new format: string array",
			input:    `["bug","enhancement"]`,
			expected: []string{"bug", "enhancement"},
		},
		{
			name:     "old format: object array",
			input:    `[{"name":"bug","color":"d73a4a"},{"name":"ios","color":"0969da"}]`,
			expected: []string{"bug", "ios"},
		},
		{
			name:     "old format with empty name",
			input:    `[{"name":"bug","color":"d73a4a"},{"name":"","color":"000000"}]`,
			expected: []string{"bug"},
		},
		{
			name:     "invalid json",
			input:    `not-json`,
			expected: nil,
		},
		{
			name:     "whitespace only",
			input:    `   `,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLabelNames(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
			for i := range tt.expected {
				if got[i] != tt.expected[i] {
					t.Fatalf("expected %v, got %v", tt.expected, got)
				}
			}
		})
	}
}

func TestResolveLabels_WithCache(t *testing.T) {
	svc := setupTestServices(t)
	projectID := "test-project"

	// Seed cache
	now := time.Now().UTC()
	labels := []model.GitHubRepoLabel{
		{ProjectID: projectID, Name: "bug", Color: "d73a4a", Description: "Something isn't working", SyncedAt: now},
		{ProjectID: projectID, Name: "enhancement", Color: "a2eeef", Description: "New feature", SyncedAt: now},
	}
	for _, l := range labels {
		if err := svc.issueService.githubRepoLabelRepo.Save(&l); err != nil {
			t.Fatalf("seed label: %v", err)
		}
	}

	result := svc.issueService.resolveLabels(projectID, []string{"bug", "unknown"}, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(result))
	}
	if result[0].Name != "bug" || result[0].Color != "d73a4a" {
		t.Fatalf("unexpected bug label: %+v", result[0])
	}
	if result[1].Name != "unknown" || result[1].Color != "999999" {
		t.Fatalf("unexpected unknown label: %+v", result[1])
	}
}

func TestResolveLabels_WithLabelMap(t *testing.T) {
	svc := setupTestServices(t)
	projectID := "test-project"

	labelMap := map[string]model.GitHubRepoLabel{
		"bug": {ProjectID: projectID, Name: "bug", Color: "d73a4a", Description: "Bug desc"},
	}

	result := svc.issueService.resolveLabels(projectID, []string{"bug", "missing"}, labelMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(result))
	}
	if result[0].Color != "d73a4a" {
		t.Fatalf("expected cached color, got %+v", result[0])
	}
	if result[1].Color != "999999" {
		t.Fatalf("expected fallback color, got %+v", result[1])
	}
}

func TestResolveLabels_EmptyInput(t *testing.T) {
	svc := setupTestServices(t)
	result := svc.issueService.resolveLabels("p", nil, nil)
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestSyncRepositoryLabels(t *testing.T) {
	svc := setupTestServices(t)
	projectID := "test-project"

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("Bug desc")},
			{Name: stringPtr("feature"), Color: stringPtr("a2eeef"), Description: stringPtr("Feature desc")},
		},
	}

	err := svc.issueService.syncRepositoryLabels(context.Background(), fake, projectID)
	if err != nil {
		t.Fatalf("sync repository labels: %v", err)
	}

	cached, err := svc.issueService.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list cached labels: %v", err)
	}
	if len(cached) != 2 {
		t.Fatalf("expected 2 cached labels, got %d", len(cached))
	}

	// Verify atomic replace: sync again with different labels
	fake2 := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("docs"), Color: stringPtr("0075ca"), Description: stringPtr("Docs desc")},
		},
	}
	err = svc.issueService.syncRepositoryLabels(context.Background(), fake2, projectID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	cached, err = svc.issueService.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list cached labels after second sync: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("expected 1 cached label after replace, got %d", len(cached))
	}
	if cached[0].Name != "docs" {
		t.Fatalf("expected docs label, got %q", cached[0].Name)
	}
}

func TestGitHubRepoLabelRepository(t *testing.T) {
	svc := setupTestServices(t)
	repo := svc.issueService.githubRepoLabelRepo
	projectID := "proj-1"
	now := time.Now().UTC()

	l1 := &model.GitHubRepoLabel{ProjectID: projectID, Name: "bug", Color: "d73a4a", Description: "Bug", SyncedAt: now}
	l2 := &model.GitHubRepoLabel{ProjectID: projectID, Name: "feat", Color: "a2eeef", Description: "Feat", SyncedAt: now}

	// Save
	if err := repo.Save(l1); err != nil {
		t.Fatalf("save l1: %v", err)
	}
	if err := repo.Save(l2); err != nil {
		t.Fatalf("save l2: %v", err)
	}

	// Find
	found, err := repo.Find(projectID, "bug")
	if err != nil {
		t.Fatalf("find bug: %v", err)
	}
	if found.Color != "d73a4a" {
		t.Fatalf("unexpected color: %s", found.Color)
	}

	// ListByProject
	all, err := repo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(all))
	}

	// ReplaceAllForProject
	newLabels := []model.GitHubRepoLabel{
		{ProjectID: projectID, Name: "docs", Color: "0075ca", Description: "Docs", SyncedAt: now},
	}
	if err := repo.ReplaceAllForProject(projectID, newLabels); err != nil {
		t.Fatalf("replace all: %v", err)
	}
	all, err = repo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list after replace: %v", err)
	}
	if len(all) != 1 || all[0].Name != "docs" {
		t.Fatalf("unexpected labels after replace: %+v", all)
	}

	// DeleteByProject
	if err := repo.DeleteByProject(projectID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, err = repo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 labels after delete, got %d", len(all))
	}
}
