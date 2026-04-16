package githubmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const ProxyPath = "/api/github/media-proxy"

const (
	defaultCacheTTL        = 24 * time.Hour
	defaultMaxCacheBytes   = 256 << 20
	defaultRequestTimeout  = 45 * time.Second
	defaultTempFilePattern = "media-*.tmp"
)

var (
	ErrInvalidMediaURL          = errors.New("invalid github media url")
	ErrUnsupportedUpstreamMedia = errors.New("unsupported upstream media content")
)

type UpstreamStatusError struct {
	StatusCode int
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("upstream returned status %d", e.StatusCode)
}

type cacheEntry struct {
	URL          string    `json:"url"`
	ContentType  string    `json:"content_type"`
	CacheControl string    `json:"cache_control"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Size         int64     `json:"size"`
	CachedAt     time.Time `json:"cached_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (e cacheEntry) expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt)
}

type CachedMedia struct {
	Metadata cacheEntry
	Path     string
}

type PassThroughResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type ProxyService struct {
	client            *http.Client
	cacheDir          string
	cacheTTL          time.Duration
	maxCacheEntrySize int64
	allowURL          func(*url.URL) bool

	mu       sync.Mutex
	download map[string]*sync.Mutex
}

func NewProxyService(cacheRoot string) *ProxyService {
	return newProxyService(cacheRoot, defaultCacheTTL, defaultMaxCacheBytes, nil, isAllowedGitHubMediaURL)
}

func newProxyService(cacheRoot string, cacheTTL time.Duration, maxCacheEntrySize int64, client *http.Client, allowURL func(*url.URL) bool) *ProxyService {
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	if maxCacheEntrySize <= 0 {
		maxCacheEntrySize = defaultMaxCacheBytes
	}
	if client == nil {
		client = &http.Client{
			Timeout: defaultRequestTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if !isAllowedGitHubMediaURL(req.URL) {
					return ErrInvalidMediaURL
				}
				return nil
			},
		}
	}
	if allowURL == nil {
		allowURL = isAllowedGitHubMediaURL
	}
	return &ProxyService{
		client:            client,
		cacheDir:          filepath.Join(cacheRoot, "github-media-cache"),
		cacheTTL:          cacheTTL,
		maxCacheEntrySize: maxCacheEntrySize,
		allowURL:          allowURL,
		download:          make(map[string]*sync.Mutex),
	}
}

func BuildProxyURL(rawURL string) string {
	normalized, err := normalizeMediaURL(rawURL)
	if err != nil {
		return rawURL
	}
	return ProxyPath + "?url=" + url.QueryEscape(normalized)
}

func RewriteHTMLMediaSources(fragment string) string {
	if strings.TrimSpace(fragment) == "" {
		return fragment
	}

	root := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), root)
	if err != nil {
		return fragment
	}

	for _, node := range nodes {
		rewriteNodeMediaSources(node)
	}

	var buf bytes.Buffer
	for _, node := range nodes {
		if err := html.Render(&buf, node); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func rewriteNodeMediaSources(node *html.Node) {
	if node == nil {
		return
	}

	if node.Type == html.ElementNode {
		switch node.Data {
		case "img":
			rewriteAttr(node, "src")
		case "video":
			rewriteAttr(node, "src")
			rewriteAttr(node, "poster")
		case "source":
			rewriteAttr(node, "src")
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		rewriteNodeMediaSources(child)
	}
}

func rewriteAttr(node *html.Node, key string) {
	for i := range node.Attr {
		if strings.EqualFold(node.Attr[i].Key, key) {
			node.Attr[i].Val = BuildProxyURL(node.Attr[i].Val)
		}
	}
}

func RewriteMediaURL(rawURL string) string {
	return BuildProxyURL(rawURL)
}

func (s *ProxyService) GetCached(rawURL string) (*CachedMedia, bool, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, false, err
	}
	return s.getCachedNormalized(normalized)
}

func (s *ProxyService) FetchAndCache(ctx context.Context, rawURL string) (*CachedMedia, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, err
	}

	lock := s.acquireDownloadLock(normalized)
	lock.Lock()
	defer lock.Unlock()

	if cached, ok, err := s.getCachedNormalized(normalized); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}

	if err := os.MkdirAll(s.cacheDir, 0o750); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "fast-ship-media-proxy/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &UpstreamStatusError{StatusCode: resp.StatusCode}
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isAllowedMediaContentType(contentType, normalized) {
		return nil, ErrUnsupportedUpstreamMedia
	}

	if contentLength := resp.ContentLength; contentLength <= 0 || contentLength > s.maxCacheEntrySize {
		return nil, &UpstreamStatusError{StatusCode: http.StatusRequestEntityTooLarge}
	}

	tempFile, err := os.CreateTemp(s.cacheDir, defaultTempFilePattern)
	if err != nil {
		return nil, err
	}

	downloadOK := false
	defer func() {
		tempFile.Close()
		if !downloadOK {
			_ = os.Remove(tempFile.Name())
		}
	}()

	written, err := io.Copy(tempFile, io.LimitReader(resp.Body, s.maxCacheEntrySize+1))
	if err != nil {
		return nil, err
	}
	if written > s.maxCacheEntrySize {
		return nil, &UpstreamStatusError{StatusCode: http.StatusRequestEntityTooLarge}
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	entry := cacheEntry{
		URL:          normalized,
		ContentType:  contentType,
		CacheControl: buildCacheControl(s.cacheTTL),
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
		Size:         written,
		CachedAt:     now,
		ExpiresAt:    now.Add(s.cacheTTL),
	}

	cachePath := s.dataPath(normalized)
	metaPath := s.metaPath(normalized)

	if err := os.Rename(tempFile.Name(), cachePath); err != nil {
		return nil, err
	}
	metaBytes, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o640); err != nil {
		return nil, err
	}

	downloadOK = true
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, nil
}

func (s *ProxyService) ProxyPass(ctx context.Context, rawURL, rangeHeader string, headOnly bool) (*PassThroughResponse, error) {
	normalized, err := s.normalize(rawURL)
	if err != nil {
		return nil, err
	}

	method := http.MethodGet
	if headOnly {
		method = http.MethodHead
	}

	req, err := http.NewRequestWithContext(ctx, method, normalized, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "fast-ship-media-proxy/1.0")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		return nil, &UpstreamStatusError{StatusCode: resp.StatusCode}
	}

	return &PassThroughResponse{
		StatusCode: resp.StatusCode,
		Header:     cloneRelevantHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

func (s *ProxyService) normalize(rawURL string) (string, error) {
	return normalizeAllowedURL(rawURL, s.allowURL)
}

func (s *ProxyService) getCachedNormalized(normalized string) (*CachedMedia, bool, error) {
	metaPath := s.metaPath(normalized)
	cachePath := s.dataPath(normalized)

	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var entry cacheEntry
	if err := json.Unmarshal(metaBytes, &entry); err != nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(cachePath)
		return nil, false, nil
	}

	if entry.expired(time.Now().UTC()) {
		_ = os.Remove(metaPath)
		_ = os.Remove(cachePath)
		return nil, false, nil
	}

	info, err := os.Stat(cachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(metaPath)
			return nil, false, nil
		}
		return nil, false, err
	}

	entry.Size = info.Size()
	return &CachedMedia{
		Metadata: entry,
		Path:     cachePath,
	}, true, nil
}

func (s *ProxyService) acquireDownloadLock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock, ok := s.download[key]
	if !ok {
		lock = &sync.Mutex{}
		s.download[key] = lock
	}
	return lock
}

func (s *ProxyService) dataPath(normalized string) string {
	return filepath.Join(s.cacheDir, cacheKey(normalized)+".bin")
}

func (s *ProxyService) metaPath(normalized string) string {
	return filepath.Join(s.cacheDir, cacheKey(normalized)+".json")
}

func cloneRelevantHeaders(header http.Header) http.Header {
	out := make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control", "ETag", "Last-Modified"} {
		for _, value := range header.Values(key) {
			out.Add(key, value)
		}
	}
	if out.Get("Cache-Control") == "" {
		out.Set("Cache-Control", buildCacheControl(defaultCacheTTL))
	}
	return out
}

func normalizeAllowedURL(rawURL string, allowURL func(*url.URL) bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", ErrInvalidMediaURL
	}
	if parsed == nil || parsed.Host == "" || parsed.Scheme == "" {
		return "", ErrInvalidMediaURL
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", ErrInvalidMediaURL
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", ErrInvalidMediaURL
	}
	if !allowURL(parsed) {
		return "", ErrInvalidMediaURL
	}
	return parsed.String(), nil
}

func normalizeMediaURL(rawURL string) (string, error) {
	return normalizeAllowedURL(rawURL, isAllowedGitHubMediaURL)
}

func isAllowedGitHubMediaURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "github.com":
		return strings.HasPrefix(parsed.EscapedPath(), "/user-attachments/assets/")
	case host == "githubusercontent.com":
		return true
	case host == "github.githubassets.com":
		return true
	case host == "githubassets.com":
		return true
	case strings.HasPrefix(host, "github-production-user-asset-") && strings.HasSuffix(host, ".s3.amazonaws.com"):
		return true
	case strings.HasSuffix(host, ".githubusercontent.com"):
		return true
	case strings.HasSuffix(host, ".githubassets.com"):
		return true
	default:
		return false
	}
}

func isAllowedMediaContentType(contentType string, rawURL string) bool {
	if contentType == "" {
		return hasMediaExtension(rawURL)
	}

	baseType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(baseType, "image/") || strings.HasPrefix(baseType, "video/") {
		return true
	}
	if baseType == "application/octet-stream" {
		return hasMediaExtension(rawURL)
	}
	return false
}

func hasMediaExtension(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".mp4", ".mov", ".webm", ".m4v":
		return true
	default:
		return false
	}
}

func buildCacheControl(ttl time.Duration) string {
	seconds := int(ttl / time.Second)
	if seconds <= 0 {
		seconds = int(defaultCacheTTL / time.Second)
	}
	return "public, max-age=" + strconv.Itoa(seconds)
}

func cacheKey(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
