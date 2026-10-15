package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListReviewComments_Paginates(t *testing.T) {
	pageHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageHits++
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page=%s, want 100", r.URL.Query().Get("per_page"))
		}
		page := r.URL.Query().Get("page")
		n := 100
		base := int64(1000)
		if page == "2" {
			n = 3
			base = 2000
		}
		items := make([]map[string]any, n)
		for i := range items {
			items[i] = map[string]any{
				"id":         base + int64(i),
				"user":       map[string]any{"login": "octocat"},
				"body":       "c",
				"path":       "a.go",
				"created_at": "2026-01-01T00:00:00Z",
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer ts.Close()

	store := &InstallationTokenStore{
		http: &httpClient{baseURL: ts.URL},
		cache: map[int64]cachedInstallToken{
			7: {token: "tok", expiresAt: time.Now().Add(time.Hour)},
		},
	}
	c := NewClient(store)
	got, err := c.ListReviewComments(context.Background(), 7, "octocat/hw", 1)
	if err != nil {
		t.Fatalf("ListReviewComments: %v", err)
	}
	if pageHits != 2 {
		t.Fatalf("pages fetched = %d, want 2", pageHits)
	}
	if len(got) != 103 {
		t.Fatalf("comments = %d, want 103", len(got))
	}
	if got[0].ID != 1000 || got[100].ID != 2000 {
		t.Fatalf("ids = %d, %d", got[0].ID, got[100].ID)
	}
}
