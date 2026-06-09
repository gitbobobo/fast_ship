package model

import "testing"

func TestIsGitHubConfigured(t *testing.T) {
	tests := []struct {
		name  string
		input Project
		want  bool
	}{
		{
			name: "fully configured",
			input: Project{
				GithubOwner:          "owner",
				GithubRepo:           "repo",
				GithubTokenEncrypted: []byte("encrypted-token"),
			},
			want: true,
		},
		{
			name: "empty owner",
			input: Project{
				GithubOwner:          "",
				GithubRepo:           "repo",
				GithubTokenEncrypted: []byte("token"),
			},
			want: false,
		},
		{
			name: "empty repo",
			input: Project{
				GithubOwner:          "owner",
				GithubRepo:           "",
				GithubTokenEncrypted: []byte("token"),
			},
			want: false,
		},
		{
			name: "nil token",
			input: Project{
				GithubOwner:          "owner",
				GithubRepo:           "repo",
				GithubTokenEncrypted: nil,
			},
			want: false,
		},
		{
			name: "empty token",
			input: Project{
				GithubOwner:          "owner",
				GithubRepo:           "repo",
				GithubTokenEncrypted: []byte{},
			},
			want: false,
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
