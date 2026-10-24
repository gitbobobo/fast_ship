package model

import (
	"testing"
	"time"
)

func TestIsGitHubConfigured(t *testing.T) {
	tests := []struct {
		name  string
		input Project
		want  bool
	}{
		{
			name: "owner and repo set",
			input: Project{
				GithubOwner: "owner",
				GithubRepo:  "repo",
			},
			want: true,
		},
		{
			name: "empty owner",
			input: Project{
				GithubOwner: "",
				GithubRepo:  "repo",
			},
			want: false,
		},
		{
			name: "empty repo",
			input: Project{
				GithubOwner: "owner",
				GithubRepo:  "",
			},
			want: false,
		},
		{
			// Token 引用不再参与仓库关联判定：仓库已关联但未选公开仓库 Token
			// 时，同步/写操作按单独的「缺公开仓库 Token」错误报出。
			name: "repo without public token reference",
			input: Project{
				GithubOwner:         "owner",
				GithubRepo:          "repo",
				PublicGitHubTokenID: "",
			},
			want: true,
		},
		{
			name:  "all empty",
			input: Project{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.IsGitHubConfigured(); got != tt.want {
				t.Errorf("IsGitHubConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

// CodeRepo 是 PR 审查的仓库取值入口：两个 code 字段都非空时用显式配置，
// 否则回落公开仓库；半配置（只填一个）同样回落，避免拼出不存在的仓库。
func TestCodeRepo(t *testing.T) {
	tests := []struct {
		name         string
		input        Project
		wantOwner    string
		wantRepo     string
		wantFullName string
	}{
		{
			name:         "fallback to public repo when code fields empty",
			input:        Project{GithubOwner: "pub", GithubRepo: "Proj"},
			wantOwner:    "pub",
			wantRepo:     "Proj",
			wantFullName: "pub/Proj",
		},
		{
			name: "explicit code repo wins",
			input: Project{
				GithubOwner: "pub", GithubRepo: "proj",
				CodeGithubOwner: "gitbobobo", CodeGithubRepo: "musiver",
			},
			wantOwner:    "gitbobobo",
			wantRepo:     "musiver",
			wantFullName: "gitbobobo/musiver",
		},
		{
			name: "half configured falls back",
			input: Project{
				GithubOwner: "pub", GithubRepo: "proj",
				CodeGithubOwner: "lonely",
			},
			wantOwner:    "pub",
			wantRepo:     "proj",
			wantFullName: "pub/proj",
		},
		{
			name: "code repo configured without public repo",
			input: Project{
				CodeGithubOwner: "gitbobobo", CodeGithubRepo: "musiver",
			},
			wantOwner:    "gitbobobo",
			wantRepo:     "musiver",
			wantFullName: "gitbobobo/musiver",
		},
		{
			name:         "nothing configured",
			input:        Project{},
			wantOwner:    "",
			wantRepo:     "",
			wantFullName: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo := tt.input.CodeRepo()
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("CodeRepo() = %q/%q, want %q/%q", owner, repo, tt.wantOwner, tt.wantRepo)
			}
			if got := tt.input.CodeRepoFullName(); got != tt.wantFullName {
				t.Errorf("CodeRepoFullName() = %q, want %q", got, tt.wantFullName)
			}
		})
	}
}

// IsShelved 是「搁置中」的唯一判定（INT-165）：shelved_at 非空且（无截止即
// 无限期，或截止未到）。到期自动恢复没有定时任务，边界落在调用时刻——
// 截止恰好等于 now 视为已到期。repository 层的 SQL 过滤与此同口径。
func TestIsShelved(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name    string
		shelved *Project
		want    bool
	}{
		{"not shelved", &Project{}, false},
		{"indefinite shelved", &Project{ShelvedAt: &past}, true},
		{"shelved until future", &Project{ShelvedAt: &past, ShelvedUntil: &future}, true},
		{"shelved until passed", &Project{ShelvedAt: &past, ShelvedUntil: &past}, false},
		{"shelved until exactly now expires", &Project{ShelvedAt: &past, ShelvedUntil: &now}, false},
		// 只有截止、没有开始时刻不是搁置（防御脏数据）。
		{"until without shelved_at", &Project{ShelvedUntil: &future}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.shelved.IsShelved(now); got != tt.want {
				t.Errorf("IsShelved() = %v, want %v", got, tt.want)
			}
		})
	}
}
