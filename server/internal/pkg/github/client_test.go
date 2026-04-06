package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	gh "github.com/google/go-github/v62/github"
)

func TestClientCreateTag_SkipsWhenTagExists(t *testing.T) {
	var getRefCalls int
	var createRefCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/git/ref/tags/v1.0.0":
			getRefCalls++
			writeJSON(t, w, http.StatusOK, map[string]any{
				"ref": "refs/tags/v1.0.0",
				"object": map[string]any{
					"sha": "abc123",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/git/refs":
			createRefCalls++
			t.Fatalf("CreateRef should not be called when tag already exists")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := client.CreateTag(context.Background(), "v1.0.0", "abc123"); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	if getRefCalls != 1 {
		t.Fatalf("expected GetRef once, got %d", getRefCalls)
	}
	if createRefCalls != 0 {
		t.Fatalf("expected CreateRef not to be called, got %d", createRefCalls)
	}
}

func TestClientCreateTag_ReturnsErrorWhenLookupFails(t *testing.T) {
	var createRefCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/git/ref/tags/v1.0.0":
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{
				"message": "github unavailable",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/git/refs":
			createRefCalls++
			t.Fatalf("CreateRef should not be called when GetRef returns non-404 error")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := client.CreateTag(context.Background(), "v1.0.0", "abc123"); err == nil {
		t.Fatalf("expected CreateTag to fail")
	}
	if createRefCalls != 0 {
		t.Fatalf("expected CreateRef not to be called, got %d", createRefCalls)
	}
}

func TestClientCreateRelease_SkipsWhenReleaseExists(t *testing.T) {
	var getReleaseCalls int
	var createReleaseCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases/tags/v1.0.0":
			getReleaseCalls++
			writeJSON(t, w, http.StatusOK, map[string]any{
				"id":       42,
				"tag_name": "v1.0.0",
				"html_url": "https://example.com/release/v1.0.0",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/releases":
			createReleaseCalls++
			t.Fatalf("CreateRelease should not be called when release already exists")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	release, err := client.CreateRelease(context.Background(), "v1.0.0", "v1.0.0", "notes")
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if release.GetID() != 42 {
		t.Fatalf("expected existing release id 42, got %d", release.GetID())
	}
	if getReleaseCalls != 1 {
		t.Fatalf("expected GetReleaseByTag once, got %d", getReleaseCalls)
	}
	if createReleaseCalls != 0 {
		t.Fatalf("expected CreateRelease not to be called, got %d", createReleaseCalls)
	}
}

func TestClientCreateRelease_ReturnsErrorWhenLookupFails(t *testing.T) {
	var createReleaseCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases/tags/v1.0.0":
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{
				"message": "github unavailable",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/releases":
			createReleaseCalls++
			t.Fatalf("CreateRelease should not be called when lookup returns non-404 error")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if _, err := client.CreateRelease(context.Background(), "v1.0.0", "v1.0.0", "notes"); err == nil {
		t.Fatalf("expected CreateRelease to fail")
	}
	if createReleaseCalls != 0 {
		t.Fatalf("expected CreateRelease not to be called, got %d", createReleaseCalls)
	}
}

func TestClientUploadAsset_DeletesExistingAssetBeforeUpload(t *testing.T) {
	var deleteCalls int
	var uploadCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases/42/assets":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"id": 9, "name": "app.apk"},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/releases/assets/9":
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/releases/42/assets":
			uploadCalls++
			if got := r.URL.Query().Get("name"); got != "app.apk" {
				t.Fatalf("expected upload asset query name=app.apk, got %q", got)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"id":   100,
				"name": "app.apk",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	file := createTempFile(t, "app.apk", "apk-data")
	defer os.Remove(file.Name())
	defer file.Close()

	if err := client.UploadAsset(context.Background(), 42, "app.apk", file); err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteReleaseAsset once, got %d", deleteCalls)
	}
	if uploadCalls != 1 {
		t.Fatalf("expected upload once, got %d", uploadCalls)
	}
}

func TestClientUploadAsset_ReturnsErrorWhenListingAssetsFails(t *testing.T) {
	var uploadCalls int

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases/42/assets":
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{
				"message": "list assets failed",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/releases/42/assets":
			uploadCalls++
			t.Fatalf("upload should not continue when listing assets fails")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	file := createTempFile(t, "app.apk", "apk-data")
	defer os.Remove(file.Name())
	defer file.Close()

	if err := client.UploadAsset(context.Background(), 42, "app.apk", file); err == nil {
		t.Fatalf("expected UploadAsset to fail")
	}
	if uploadCalls != 0 {
		t.Fatalf("expected upload not to be attempted, got %d", uploadCalls)
	}
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}

	ghClient := gh.NewClient(server.Client())
	ghClient.BaseURL = baseURL
	ghClient.UploadURL = baseURL

	return &Client{
		client: ghClient,
		owner:  "owner",
		repo:   "repo",
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

func createTempFile(t *testing.T, name, contents string) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), filepath.Base(name)+"-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("seek temp file: %v", err)
	}
	return file
}
