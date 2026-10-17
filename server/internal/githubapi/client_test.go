package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuth 固定 bearer 并记录 Invalidate 次数。
type fakeAuth struct {
	token      string
	bearerHits int
	invalids   int
	failBearer bool
}

func (a *fakeAuth) Bearer(ctx context.Context, installationID int64) (string, error) {
	if a.failBearer {
		return "", errors.New("bearer unavailable")
	}
	a.bearerHits++
	return a.token, nil
}

func (a *fakeAuth) Invalidate(installationID int64) {
	a.invalids++
}

func TestGetPullRequest_MapsAllFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/hw/pulls/7" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q, want bearer token", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"number":    7,
			"title":     "Fix crash",
			"body":      "long story",
			"html_url":  "https://github.com/octocat/hw/pull/7",
			"state":     "closed",
			"draft":     true,
			"merged":    true,
			"merged_at": "2026-01-02T03:04:05Z",
			"closed_at": "2026-01-02T03:04:06Z",
			"user":      map[string]any{"login": "bob"},
			"head":      map[string]any{"sha": "abc123", "ref": "fix-crash", "repo": map[string]any{"full_name": "fork/other"}},
			"base":      map[string]any{"ref": "main", "repo": map[string]any{"full_name": "octocat/hw"}},
		})
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "tok"}
	c := NewClient(auth, ts.URL)
	pr, err := c.GetPullRequest(context.Background(), 1, "octocat/hw", 7)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	mergedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closedAt := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	want := PullRequest{
		RepoFullName: "octocat/hw",
		Number:       7,
		Title:        "Fix crash",
		Body:         "long story",
		HTMLURL:      "https://github.com/octocat/hw/pull/7",
		State:        "closed",
		Draft:        true,
		Merged:       true,
		MergedAt:     &mergedAt,
		ClosedAt:     &closedAt,
		HeadSHA:      "abc123",
		HeadRef:      "fix-crash",
		BaseRef:      "main",
		AuthorLogin:  "bob",
	}
	if !reflect.DeepEqual(*pr, want) {
		t.Fatalf("pull request mismatch:\n got %+v\nwant %+v", *pr, want)
	}
}

func TestRESTClient_UnauthorizedInvalidatesAndRetriesOnce(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 9001, "body": "ok"})
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "stale"}
	c := NewClient(auth, ts.URL)
	commentID, err := c.CreatePullRequestComment(context.Background(), 1, "octocat/hw", 7, "hi")
	if err != nil {
		t.Fatalf("CreatePullRequestComment: %v", err)
	}
	if commentID != 9001 {
		t.Fatalf("comment id = %d, want 9001", commentID)
	}
	if auth.invalids != 1 || auth.bearerHits != 2 {
		t.Fatalf("invalidate=%d bearer=%d, want 1/2", auth.invalids, auth.bearerHits)
	}
}

func TestRESTClient_PersistentUnauthorizedPropagatesStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "stale"}
	c := NewClient(auth, ts.URL)
	_, err := c.GetPullRequestDiff(context.Background(), 1, "octocat/hw", 7)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected *StatusError 401, got %v", err)
	}
	if auth.invalids != 1 || auth.bearerHits != 2 {
		t.Fatalf("invalidate=%d bearer=%d, want 1/2", auth.invalids, auth.bearerHits)
	}
}

func TestStatusErrorHelpers(t *testing.T) {
	if !IsForbidden(&StatusError{StatusCode: http.StatusForbidden}) {
		t.Fatal("403 must be forbidden")
	}
	if !IsForbidden(ErrForbidden) {
		t.Fatal("sentinel must be forbidden")
	}
	if !IsNotFound(&StatusError{StatusCode: http.StatusNotFound}) {
		t.Fatal("404 must be not found")
	}
	if IsForbidden(&StatusError{StatusCode: http.StatusBadRequest}) {
		t.Fatal("400 must not be forbidden")
	}
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := splitRepo("octocat/hw")
	if err != nil || owner != "octocat" || name != "hw" {
		t.Fatalf("splitRepo = %q %q %v", owner, name, err)
	}
	if _, _, err := splitRepo("octocat"); err == nil {
		t.Fatal("single segment must fail")
	}
	if _, _, err := splitRepo("a/b/c"); err == nil {
		t.Fatal("three segments must fail")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}
