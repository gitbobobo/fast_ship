package model

import "testing"

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
