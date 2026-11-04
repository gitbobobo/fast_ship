package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// INT-186 端到端：真实 router + RequireRunToken 中间件下，图片端点按清单
// 给的审查尺寸地址返回缩放变体（长边 1536 的 JPEG），原图字节仍可按需取；
// 大图（长边 ≥2400px）单轮读图的字节总量落在提示词预算内（单张 ≤500KB、
// 总量 ≤4MB），不再触发模型网关 413。

// writeE2EPortfolioPNG 生成 w×h 像素的「类截图」PNG：逐像素确定性噪声让
// PNG 压不下去（真实截图的字节量级），纯色 PNG 会小到证明不了 413 的事。
func writeE2EPortfolioPNG(t *testing.T, dir, name string, w, h int, seed int64) string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			row[x*4] = byte(rng.Intn(256))
			row[x*4+1] = byte(rng.Intn(256))
			row[x*4+2] = byte(rng.Intn(256))
			row[x*4+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

func TestReviewAgentScreenshotScaleE2E(t *testing.T) {
	e := setupRouterTestEnv(t)
	user := createRouterTestUser(t, e.db, uuid.NewString(), "review-agent-e2e", "e2e@example.com")
	project := createRouterTestProject(t, e.db, user.ID)

	// 视觉运行：在途（running）+ 未过期，配一枚已知明文的运行令牌。
	now := time.Now().UTC().Truncate(time.Second)
	run := &model.ReviewRun{
		ID: uuid.NewString(), UserID: user.ID, ProjectID: project.ID,
		Kind: model.ReviewKindVisual, RepoFullName: project.GithubOwner + "/" + project.GithubRepo,
		PRNumber: 186, HeadSHA: "sha-e2e", RoundNumber: 1,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		EnvironmentID: "env-e2e", InstanceID: "inst-e2e", Model: "m-e2e",
		ThreadID: "thread-e2e", LaunchCommandID: "launch-e2e", InitialMessageID: "msg-e2e",
		Status: model.ReviewRunRunning, DeadlineAt: now.Add(30 * time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	accessToken := "fsr_e2e_" + uuid.NewString()
	sum := sha256.Sum256([]byte(accessToken))
	if err := e.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: hex.EncodeToString(sum[:]),
		CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("create run token: %v", err)
	}

	// 6 张长边 ≥2400px 的截图资产（INT-186 验收场景）。
	shotsDir := t.TempDir()
	sizes := [][2]int{{2480, 2248}, {2248, 2480}, {2560, 1440}, {2480, 2248}, {2248, 2480}, {1080, 2400}}
	assetIDs := make([]string, 0, len(sizes))
	originalBytes := make([]int, 0, len(sizes))
	for i, size := range sizes {
		path := writeE2EPortfolioPNG(t, shotsDir, fmt.Sprintf("shot-%d.png", i), size[0], size[1], int64(i+1))
		originalBytes = append(originalBytes, mustFileSize(t, path))
		id := uuid.NewString()
		assetIDs = append(assetIDs, id)
		if err := e.db.Create(&model.ReviewAsset{
			ID: id, IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			RepoFullName: run.RepoFullName, PRNumber: run.PRNumber, HeadSHA: run.HeadSHA,
			Position: i, Label: fmt.Sprintf("截图 %d", i+1),
			SourceURL:   "https://github.com/user-attachments/assets/e2e-" + id,
			StoragePath: path, MimeType: "image/png", SizeBytes: int64(originalBytes[i]), CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create asset: %v", err)
		}
	}

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		return rec
	}

	var variantBytes []int
	variantDims := make([][2]int, 0, len(sizes))
	for i, id := range assetIDs {
		review := get("/api/review-agent/runs/" + run.ID + "/screenshots/" + id + "?max=1536&format=jpeg&quality=80")
		if review.Code != http.StatusOK {
			t.Fatalf("review-size #%d: HTTP %d body %s", i, review.Code, review.Body.String())
		}
		if got := review.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Fatalf("review-size #%d Content-Type = %q, want image/jpeg", i, got)
		}
		// 缩放变体与原图同走 inline 图片档硬化头（对照 handler 的
		// egress.inlineImageHeaders：nosniff + CSP sandbox）。
		if got := review.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("review-size #%d X-Content-Type-Options = %q, want nosniff", i, got)
		}
		if csp := review.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
			t.Fatalf("review-size #%d Content-Security-Policy = %q, want 含 sandbox", i, csp)
		}
		img, err := jpeg.Decode(bytes.NewReader(review.Body.Bytes()))
		if err != nil {
			t.Fatalf("review-size #%d decode: %v", i, err)
		}
		b := img.Bounds()
		long := b.Dx()
		if b.Dy() > long {
			long = b.Dy()
		}
		if long != 1536 {
			t.Fatalf("review-size #%d long edge = %d, want 1536", i, long)
		}
		variantDims = append(variantDims, [2]int{b.Dx(), b.Dy()})
		variantBytes = append(variantBytes, review.Body.Len())

		// 原图按需获取途径：不带参数仍是 PNG 原字节。
		original := get("/api/review-agent/runs/" + run.ID + "/screenshots/" + id)
		if original.Code != http.StatusOK {
			t.Fatalf("original #%d: HTTP %d", i, original.Code)
		}
		if got := original.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("original #%d Content-Type = %q, want image/png", i, got)
		}
		if original.Body.Len() != originalBytes[i] {
			t.Fatalf("original #%d bytes = %d, want %d", i, original.Body.Len(), originalBytes[i])
		}
	}

	// 413 证据：单张变体 ≤500KB、整轮 ≤4MB（提示词「读图预算」的两条线）。
	var totalOriginal, totalVariant int
	t.Logf("%-6s %-12s %-12s", "shot", "original", "review-size")
	for i := range assetIDs {
		t.Logf("#%-5d %-12d %-12d (jpeg %dx%d)", i, originalBytes[i], variantBytes[i],
			variantDims[i][0], variantDims[i][1])
		totalOriginal += originalBytes[i]
		totalVariant += variantBytes[i]
		if variantBytes[i] > 500<<10 {
			t.Fatalf("variant #%d = %d bytes, 超出单张 500KB 预算", i, variantBytes[i])
		}
	}
	t.Logf("total original=%d bytes (%.1f MB) → review-size=%d bytes (%.2f MB)",
		totalOriginal, float64(totalOriginal)/(1<<20), totalVariant, float64(totalVariant)/(1<<20))
	if totalVariant > 4<<20 {
		t.Fatalf("total review-size bytes = %d, 超出整轮 4MB 预算", totalVariant)
	}
	if totalVariant >= totalOriginal/10 {
		t.Fatalf("缩放没有带来量级下降：original %d vs variant %d", totalOriginal, totalVariant)
	}

	// 参数护栏：非法 format 回 400；路径 runId 与令牌错配回 40307。
	badFormat := get("/api/review-agent/runs/" + run.ID + "/screenshots/" + assetIDs[0] + "?format=webp")
	if badFormat.Code != http.StatusBadRequest {
		t.Fatalf("bad format: HTTP %d, want 400", badFormat.Code)
	}
	var errBody struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(badFormat.Body.Bytes(), &errBody); err != nil || errBody.Code == 0 {
		t.Fatalf("bad format body = %s, want 错误 envelope", badFormat.Body.String())
	}
	otherRun := get("/api/review-agent/runs/" + uuid.NewString() + "/screenshots/" + assetIDs[0])
	if otherRun.Code != http.StatusForbidden {
		t.Fatalf("run mismatch: HTTP %d, want 403", otherRun.Code)
	}

	// 无凭证不许读图。
	noAuth := httptest.NewRequest(http.MethodGet, "/api/review-agent/runs/"+run.ID+"/screenshots/"+assetIDs[0], nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, noAuth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: HTTP %d, want 401", rec.Code)
	}
}

func mustFileSize(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return int(info.Size())
}
