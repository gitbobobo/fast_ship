package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// INT-186 审查尺寸缩略图：清单给审查尺寸地址（长边 ≤1536 的 JPEG）、原图按需
// 获取；大折叠屏/平板截图逐张读原图会撑爆审查代理的模型网关（413）。

// writeTestPNG 真编码一张 w×h 纯色 PNG 落临时目录当 StoragePath（纯色压缩快，
// 缩放判断只关心头声明的尺寸与长边）。
func writeTestPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 30, 90, 200, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

// writeFakeHugePNG 手拼一张只带头的 PNG（IHDR 声明 8000×8001 ≈ 6400 万像素，
// 超出解码预算）：像素预算守卫要在解码像素之前拦住，测试不需要真落 64MP 数据。
func writeFakeHugePNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		var head [4]byte
		binary.BigEndian.PutUint32(head[:], uint32(len(data)))
		buf.Write(head[:])
		buf.WriteString(kind)
		buf.Write(data)
		var crc [4]byte
		binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(append([]byte(kind), data...)))
		buf.Write(crc[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 8000)
	binary.BigEndian.PutUint32(ihdr[4:8], 8001)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // truecolor RGB
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	path := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write fake huge png: %v", err)
	}
	return path
}

// writeCorruptPixelsPNG 手拼「头可解但像素损坏」的 PNG：真 PNG 签名 + IHDR
// 声明 2000×2000（长边超目标 → 清单判 scaled）+ 截断的 IDAT（像素解码必败），
// 用来锁「scaled 行失败不得静默降级原图」。
func writeCorruptPixelsPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		var head [4]byte
		binary.BigEndian.PutUint32(head[:], uint32(len(data)))
		buf.Write(head[:])
		buf.WriteString(kind)
		buf.Write(data)
		var crc [4]byte
		binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(append([]byte(kind), data...)))
		buf.Write(crc[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 2000)
	binary.BigEndian.PutUint32(ihdr[4:8], 2000)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // truecolor RGB
	chunk("IHDR", ihdr)
	chunk("IDAT", []byte{0x78, 0x9c, 0x00, 0x11, 0x22}) // 截断的 zlib 流，解不出像素
	path := filepath.Join(t.TempDir(), "corrupt.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write corrupt png: %v", err)
	}
	return path
}

// writeNoisePNG 逐像素确定性噪声 PNG：压不下去，字节量级同真实繁忙截图，
// 用来证明字节预算强制真的在工作（纯色 PNG 压得太小，锁不住预算路径）。
func writeNoisePNG(t *testing.T, w, h int, seed int64) string {
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
	path := filepath.Join(t.TempDir(), "noise.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

// writeNoiseRGBAPNG 同 writeNoisePNG 但连 alpha 也逐像素随机：缩放输出不再
// 是不透明图，PNG 编码器走 RGBA 档（不透明图会被写成 RGB 档的 3 字节/像素，
// 384 边长就压进预算了），锁得住「每个长边都超预算 → 一路对半到下限」的降
// 边路径。
func writeNoiseRGBAPNG(t *testing.T, w, h int, seed int64) string {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			row[x*4] = byte(rng.Intn(256))
			row[x*4+1] = byte(rng.Intn(256))
			row[x*4+2] = byte(rng.Intn(256))
			row[x*4+3] = byte(rng.Intn(256))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "noise-rgba.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

// clampReviewByte 把像素通道值收进 [0,255]。
func clampReviewByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// writeHeavySmallPNG 「小而重」的 PNG：长边在审查目标内（1512×982 的桌面截图
// 常态），但文件字节远超 500KB 预算。做法是大幅平滑底纹叠逐像素小幅噪声——
// PNG 的滤波压不住噪声（文件大），JPEG 的 DCT 量化把它抹平（原尺寸编码仍然
// 很小），恰好落在「透传会击穿字节预算、原尺寸重编码轻松达标」的区间。
func writeHeavySmallPNG(t *testing.T, w, h int, seed int64) string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			base := int(127 + 80*math.Sin(float64(x)/97)*math.Sin(float64(y)/61))
			n := rng.Intn(33) - 16
			row[x*4] = byte(clampReviewByte(base + n))
			row[x*4+1] = byte(clampReviewByte(base - n))
			row[x*4+2] = byte(clampReviewByte(255 - base))
			row[x*4+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "heavy-small.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

func TestParseReviewScaleQuery(t *testing.T) {
	// 三参数全空 → nil（原图透传语义）。
	if p, err := parseReviewScaleQuery("", "", ""); p != nil || err != nil {
		t.Fatalf("empty query = %v, %v; want nil, nil", p, err)
	}
	// 只给一个参数，其余按默认值补全。
	p, err := parseReviewScaleQuery("", "png", "")
	if err != nil || p == nil {
		t.Fatalf("format-only: %v, %v", p, err)
	}
	if p.Max != reviewScreenshotMaxEdge || p.Quality != reviewScreenshotQuality || p.Format != "png" {
		t.Fatalf("format-only defaults: %+v", p)
	}
	// 越界 clamp；同时覆盖「只给 max/quality，format 补 jpeg 默认」。
	for _, c := range []struct{ maxRaw, qualityRaw, wantMax, wantQuality string }{
		{"100", "10", "320", "30"},
		{"5000", "99", "2000", "95"},
		{"800", "50", "800", "50"},
	} {
		p, err := parseReviewScaleQuery(c.maxRaw, "", c.qualityRaw)
		if err != nil || p == nil {
			t.Fatalf("parse(%s, %s): %v, %v", c.maxRaw, c.qualityRaw, p, err)
		}
		if got := strconv.Itoa(p.Max) + "/" + strconv.Itoa(p.Quality); got != c.wantMax+"/"+c.wantQuality {
			t.Fatalf("clamp(%s, %s) = %s, want %s/%s", c.maxRaw, c.qualityRaw, got, c.wantMax, c.wantQuality)
		}
		if p.Format != "jpeg" {
			t.Fatalf("format = %q, want 缺省 jpeg", p.Format)
		}
	}
	// 非法值报错（含 webp：x/image 无 webp 编码器，不在选项里）。
	for _, bad := range [][3]string{
		{"abc", "", ""},
		{"", "webp", ""},
		{"", "", "x"},
	} {
		if _, err := parseReviewScaleQuery(bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("query %v 应报错", bad)
		}
	}
}

func TestScaleReviewScreenshotJPEGDims(t *testing.T) {
	path := writeTestPNG(t, 2480, 2248)
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{
		Max: reviewScreenshotMaxEdge, Format: "jpeg", Quality: reviewScreenshotQuality,
	})
	if err != nil || data == nil {
		t.Fatalf("scale: %v, %d bytes", err, len(data))
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scaled output decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != reviewScreenshotMaxEdge {
		t.Fatalf("scaled long edge = %d, want %d", b.Dx(), reviewScreenshotMaxEdge)
	}
	// 2248×2480 等比到长边 1536：短边整数四舍五入 = 1392。
	if b.Dy() != 1392 {
		t.Fatalf("scaled short edge = %d, want 1392（等比）", b.Dy())
	}
}

// 长边不大于目标且原图字节在预算内（小而轻）：零损失透传，返回 nil data 由
// 调用方按原图下发；小而重的对偶场景见 TestScaleReviewScreenshotSmallButHeavy。
func TestScaleReviewScreenshotNoUpscale(t *testing.T) {
	path := writeTestPNG(t, 640, 400)
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 3000, Format: "jpeg", Quality: 80})
	if err != nil || data != nil || mime != "" {
		t.Fatalf("no-upscale = %d bytes, %q, %v; want nil data（不放大）", len(data), mime, err)
	}
}

// 小而重：长边在目标内、原图文件字节超预算（1512×982 的桌面截图常态）——
// 不再无条件透传击穿字节承诺：按原尺寸重编码出 jpeg 变体，尺寸不变、字节落
// 在预算内。
func TestScaleReviewScreenshotSmallButHeavy(t *testing.T) {
	path := writeHeavySmallPNG(t, 1512, 982, 21)
	// 先证明确实「小而重」：文件字节超预算（否则 nil data 透传语义就对了）。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() <= reviewScreenshotMaxBytes {
		t.Fatalf("png 只有 %d bytes，锁不到小而重路径", fi.Size())
	}
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{
		Max: reviewScreenshotMaxEdge, Format: "jpeg", Quality: reviewScreenshotQuality,
	})
	if err != nil || data == nil || mime != "image/jpeg" {
		t.Fatalf("small-but-heavy scale: %d bytes, %q, %v", len(data), mime, err)
	}
	if len(data) > reviewScreenshotMaxBytes {
		t.Fatalf("variant = %d bytes, 超出单张 500KB 预算", len(data))
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("variant decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 1512 || b.Dy() != 982 {
		t.Fatalf("variant = %dx%d, want 原尺寸 1512×982（不放大不缩边）", b.Dx(), b.Dy())
	}
}

// reviewScreenshotServed（清单 served 标示与取图端点共用的判定）七态：小而轻
// → original_light；小而重、长边超目标 → scaled；非位图、伪 heic、超 64MP、
// 文件缺失 → original_heavy。
func TestReviewScreenshotServed(t *testing.T) {
	note := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(note, []byte("definitely not an image"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	heic := filepath.Join(t.TempDir(), "shot.heic")
	if err := os.WriteFile(heic, []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1hevc"), 0o600); err != nil {
		t.Fatalf("write heic: %v", err)
	}
	for _, c := range []struct {
		name string
		path string
		want ReviewScreenshotServed
	}{
		{"小而轻", writeTestPNG(t, 640, 400), ServedOriginalLight},
		{"小而重", writeHeavySmallPNG(t, 1512, 982, 21), ServedScaled},
		{"长边超目标", writeTestPNG(t, 2480, 2248), ServedScaled},
		{"非位图", note, ServedOriginalHeavy},
		{"伪 heic", heic, ServedOriginalHeavy},
		{"超 64MP", writeFakeHugePNG(t), ServedOriginalHeavy},
		{"文件缺失", filepath.Join(t.TempDir(), "missing.png"), ServedOriginalHeavy},
	} {
		if got := reviewScreenshotServed(c.path, reviewScreenshotMaxEdge); got != c.want {
			t.Fatalf("%s: served = %q, want %q", c.name, got, c.want)
		}
	}
}

// 解码字节闸权重按格式口径（与截图标注裁剪共用同一份估算函数）：16 位 PNG
// 手拼 IHDR（bit depth=16）权 16；8 位 PNG/webp/未知权 8；jpeg/gif 权 12；
// 样本截断或 IHDR 不在首位（损坏）按最大档 16 预留。
func TestScreenshotDecodeBytesPerPixel(t *testing.T) {
	makePNGHead := func(bitDepth, colorType byte) []byte {
		head := make([]byte, 32)
		copy(head, "\x89PNG\r\n\x1a\n")
		binary.BigEndian.PutUint32(head[8:12], 13) // IHDR 数据长度
		copy(head[12:16], "IHDR")
		binary.BigEndian.PutUint32(head[16:20], 2000)
		binary.BigEndian.PutUint32(head[20:24], 1000)
		head[24] = bitDepth
		head[25] = colorType
		return head
	}
	cases := []struct {
		name string
		kind string
		head []byte
		want int64
	}{
		{"16 位 PNG", "image/png", makePNGHead(16, 2), 16},
		{"16 位灰度 PNG", "image/png", makePNGHead(16, 0), 16},
		{"8 位 PNG", "image/png", makePNGHead(8, 2), 8},
		{"调色板 PNG", "image/png", makePNGHead(4, 3), 8},
		{"jpeg", "image/jpeg", []byte("\xff\xd8\xff\xe0"), 12},
		{"gif", "image/gif", []byte("GIF89a"), 12},
		{"webp", "image/webp", []byte("RIFF....WEBP"), 8},
		{"未知格式", "application/octet-stream", []byte("junk"), 8},
		{"样本截断", "image/png", makePNGHead(8, 2)[:20], 16},
		{"IHDR 不在首位", "image/png", func() []byte {
			head := makePNGHead(8, 2)
			copy(head[12:16], "XXXX")
			return head
		}(), 16},
	}
	for _, c := range cases {
		if got := screenshotDecodeBytesPerPixel(c.kind, c.head); got != c.want {
			t.Fatalf("%s: bytes/px = %d, want %d", c.name, got, c.want)
		}
	}
}

// 像素预算：IHDR 声明超限的图经共享判定归 original_heavy，直接透传不解码
// （取图端点的防御分支见 TestAgentScreenshotImageScale）。
func TestScaleReviewScreenshotPixelBudget(t *testing.T) {
	path := writeFakeHugePNG(t)
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 1536, Format: "jpeg", Quality: 80})
	if err != nil || data != nil {
		t.Fatalf("over-budget = %d bytes, %v; want original_heavy 原图透传（nil data）", len(data), err)
	}
}

// 头可解但像素损坏（真 PNG 头声明 2000×2000 进 scaled 行 + 截断的 IDAT）：
// 不再静默降级透传原图——清单已承诺该地址是预算内变体，返回
// errReviewScaleVariantFailed 由调用方 40406，代理读到错误记 unmeasurable。
func TestScaleReviewScreenshotCorruptPixels(t *testing.T) {
	path := writeCorruptPixelsPNG(t)
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 1536, Format: "jpeg", Quality: 80})
	if !errors.Is(err, errReviewScaleVariantFailed) || errors.Is(err, errReviewScaleTooLarge) || data != nil {
		t.Fatalf("corrupt pixels = %d bytes, %v; want errReviewScaleVariantFailed", len(data), err)
	}
}

// gif 可缩放：标准库能编码的格式走完整链路（嗅探 → 解码首帧 → 缩放 → 编码）。
func TestScaleReviewScreenshotGIF(t *testing.T) {
	pal := color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	img := image.NewPaletted(image.Rect(0, 0, 800, 600), pal)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	path := filepath.Join(t.TempDir(), "shot.gif")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write gif: %v", err)
	}
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 500, Format: "jpeg", Quality: 80})
	if err != nil || data == nil || mime != "image/jpeg" {
		t.Fatalf("gif scale: %d bytes, %q, %v", len(data), mime, err)
	}
	imgOut, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scaled decode: %v", err)
	}
	if b := imgOut.Bounds(); b.Dx() != 500 || b.Dy() != 375 {
		t.Fatalf("gif scaled = %dx%d, want 500×375（等比）", b.Dx(), b.Dy())
	}
}

// webp 魔数 + 乱体：嗅探认出 webp、头解码失败 → 经共享判定归 original_heavy，
// 原图透传（nil data 且无错误——清单已按最重原图标示，承诺一致，不报错也不
// 出变体）。
func TestScaleReviewScreenshotWebpUndecodable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.webp")
	body := append([]byte("RIFF\x24\x00\x00\x00WEBP"), make([]byte, 36)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write webp: %v", err)
	}
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 1536, Format: "jpeg", Quality: 80})
	if err != nil || data != nil {
		t.Fatalf("webp garbage = %d bytes, %v; want original_heavy 原图透传（nil data）", len(data), err)
	}
}

// 存储内容不是位图：嗅探失败 → 经共享判定归 original_heavy，原图透传而不是
// 404（下载面本来就允许入库有声明类型的非解码位图，如 heic/tiff）。
func TestScaleReviewScreenshotNonBitmap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("definitely not an image"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 1536, Format: "jpeg", Quality: 80})
	if err != nil || data != nil {
		t.Fatalf("non-bitmap = %d bytes, %v; want original_heavy 原图透传（nil data）", len(data), err)
	}
}

// 存储 MimeType 说谎（声明 webp、实际 PNG 内容）：缩放分派只信魔数，照常出
// jpeg 变体。
// 接缝测试（INT-186 第 3 轮审计钉死）：真编码 8 位与 16 位 PNG 落盘，经
// reviewScreenshotHead 取样本后算解码闸权重——第 2 轮曾把样本数组留在 16
// 字节而 16 位判定要求 ≥25 字节，纯函数测试用手拼头，测不到这条接缝。
func TestReviewScreenshotHeadFeedsBytesPerPixel(t *testing.T) {
	writePNG := func(bitDepth int) string {
		var img image.Image
		if bitDepth == 16 {
			img = image.NewNRGBA64(image.Rect(0, 0, 32, 32))
		} else {
			img = image.NewNRGBA(image.Rect(0, 0, 32, 32))
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("encode %d-bit png: %v", bitDepth, err)
		}
		path := filepath.Join(t.TempDir(), strconv.Itoa(bitDepth)+".png")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return path
	}
	for _, c := range []struct {
		bitDepth int
		want     int64
	}{{8, 8}, {16, 16}} {
		head, err := reviewScreenshotHead(writePNG(c.bitDepth))
		if err != nil {
			t.Fatalf("head: %v", err)
		}
		if got := screenshotDecodeBytesPerPixel("image/png", head); got != c.want {
			t.Fatalf("bit depth %d: weight = %d, want %d（接缝断在样本长度上）", c.bitDepth, got, c.want)
		}
	}
}

// 字节预算强制：不可压缩的噪声 PNG（繁忙截图的字节量级）q80 一遍编码超
// 提示词承诺的单张 ≤500KB → 服务端降质量重编，交出去的字节必须落在预算内。
func TestScaleReviewScreenshotByteBudgetJPEG(t *testing.T) {
	path := writeNoisePNG(t, 2480, 2248, 7)
	// 先证明走的是预算路径：同尺寸 q80 一遍编码确实超预算（否则本测试没锁到东西）。
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var firstPass bytes.Buffer
	if err := jpeg.Encode(&firstPass, flattenReviewAlphaToWhite(scaleToReviewEdge(img, reviewScreenshotMaxEdge)), &jpeg.Options{Quality: reviewScreenshotQuality}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if firstPass.Len() <= reviewScreenshotMaxBytes {
		t.Fatalf("q80 一遍编码只有 %d bytes，本测试锁不到预算路径", firstPass.Len())
	}
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{
		Max: reviewScreenshotMaxEdge, Format: "jpeg", Quality: reviewScreenshotQuality,
	})
	if err != nil || data == nil || mime != "image/jpeg" {
		t.Fatalf("scale: %d bytes, %q, %v", len(data), mime, err)
	}
	if len(data) > reviewScreenshotMaxBytes {
		t.Fatalf("variant = %d bytes, 超出单张 500KB 预算", len(data))
	}
	t.Logf("noise 2480×2248: q80 first-pass %d bytes → budgeted variant %d bytes", firstPass.Len(), len(data))
}

// 字节预算的降边路径：PNG 没有质量旋钮，不可压缩的噪声方图在每个长边都
// 超预算 → 目标长边逐次对半（1536 → 768 → 384 → 320）直到进预算，输出等比、
// 字节落在预算内。（JPEG 噪声在 q35 全长边就能进预算，锁不住降边——降边
// 对 JPEG 是防更病态内容的兑底。）
func TestScaleReviewScreenshotByteBudgetEdgeHalving(t *testing.T) {
	path := writeNoiseRGBAPNG(t, 2000, 2000, 11)
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{
		Max: reviewScreenshotMaxEdge, Format: "png", Quality: reviewScreenshotQuality,
	})
	if err != nil || data == nil || mime != "image/png" {
		t.Fatalf("scale: %d bytes, %q, %v", len(data), mime, err)
	}
	if len(data) > reviewScreenshotMaxBytes {
		t.Fatalf("variant = %d bytes, 超出单张 500KB 预算", len(data))
	}
	out, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := out.Bounds()
	if b.Dx() != 320 || b.Dy() != 320 {
		t.Fatalf("halved edge = %dx%d, want 320×320（下限长边）", b.Dx(), b.Dy())
	}
	t.Logf("noise 2000×2000 png: variant %d bytes at %dx%d (edge halved to floor)", len(data), b.Dx(), b.Dy())
}

// gif 逻辑画布：首帧可能小于 DecodeConfig 的逻辑画布且带偏移——缩放决策用
// 画布尺寸，像素也得是合成后的整张画布，而不是把首帧反向放大裁掉留白。
func TestScaleReviewScreenshotGIFLogicalCanvas(t *testing.T) {
	pal := color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	// 首帧 100×100、画布内偏移 (100,150)；逻辑画布 800×600 走 Config 声明。
	frame := image.NewPaletted(image.Rect(100, 150, 200, 250), pal)
	draw.Draw(frame, frame.Bounds(), image.NewUniform(pal[0]), image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image:  []*image.Paletted{frame},
		Delay:  []int{0},
		Config: image.Config{Width: 800, Height: 600, ColorModel: pal},
	}); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	path := filepath.Join(t.TempDir(), "canvas.gif")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write gif: %v", err)
	}
	data, mime, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 500, Format: "jpeg", Quality: 80})
	if err != nil || data == nil || mime != "image/jpeg" {
		t.Fatalf("gif canvas scale: %d bytes, %q, %v", len(data), mime, err)
	}
	out, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scaled decode: %v", err)
	}
	// 输出等比于 800×600 逻辑画布（500×375），不是首帧 100×100 的放大。
	if b := out.Bounds(); b.Dx() != 500 || b.Dy() != 375 {
		t.Fatalf("gif canvas scaled = %dx%d, want 500×375（等比于 800×600 画布）", b.Dx(), b.Dy())
	}
}

// 全透明 PNG 缩成 JPEG：alpha 合成白底后应是白色——透明像素直接编码会变黑
// （RGBA 零值），视觉审查会把留白看成黑块。
func TestScaleReviewScreenshotAlphaFlattensToWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8)) // 零值 = 全透明
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "transparent.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 4, Format: "jpeg", Quality: 80})
	if err != nil || data == nil {
		t.Fatalf("scale: %v, %d bytes", err, len(data))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := decoded.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := decoded.At(x, y).RGBA()
			if r < 65000 || g < 65000 || bl < 65000 {
				t.Fatalf("pixel(%d,%d) = %d,%d,%d; want 白底（≈65535）", x, y, r>>8, g>>8, bl>>8)
			}
		}
	}
}

// 半透明混色：50% 红叠白底 ≈ (255,127,127)——锁住 premultiplied 合成正确
// （全透明测试锁不住混色系数：错一半颜色也照样全白）。
func TestScaleReviewScreenshotAlphaBlendsOverWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 0, 0, 128
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "half-red.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{Max: 4, Format: "jpeg", Quality: 80})
	if err != nil || data == nil {
		t.Fatalf("scale: %v, %d bytes", err, len(data))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r, g, bl, _ := decoded.At(2, 2).RGBA()
	r8, g8, b8 := r>>8, g>>8, bl>>8
	if r8 < 248 || g8 < 118 || g8 > 138 || b8 < 118 || b8 > 138 {
		t.Fatalf("50%% 红叠白底 = (%d,%d,%d), want ≈(255,127,127)", r8, g8, b8)
	}
}

// JPEG EXIF 方向接线：方向 6（顺时针 90°）的 JPEG 走缩放链路，展示尺寸
// （宽高互换）才进缩放决策与输出——存储 2480×1200 展示为 1200×2480，缩到
// 长边 1536 后是 743×1536；存储左半红右半蓝转后变上红下蓝。
func TestScaleReviewScreenshotJPEGExifOrientation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotated.jpg")
	if err := os.WriteFile(path, makeJPEGWithOrientation(t, 6, 2480, 1200), 0o600); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
	data, _, err := scaleReviewScreenshot(context.Background(), path, &reviewScaleParams{
		Max: reviewScreenshotMaxEdge, Format: "jpeg", Quality: reviewScreenshotQuality,
	})
	if err != nil || data == nil {
		t.Fatalf("scale: %v, %d bytes", err, len(data))
	}
	out, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := out.Bounds()
	if b.Dx() != 743 || b.Dy() != 1536 {
		t.Fatalf("exif scaled = %dx%d, want 743×1536（方向 6 宽高互换后等比）", b.Dx(), b.Dy())
	}
	checkRGB := func(x, y int, wantRed bool) {
		r, g, bl, _ := out.At(x, y).RGBA()
		r8, g8, b8 := r>>8, g>>8, bl>>8
		if wantRed && (r8 < 200 || g8 > 100 || b8 > 100) {
			t.Fatalf("pixel(%d,%d) = (%d,%d,%d), want 红（上半天）", x, y, r8, g8, b8)
		}
		if !wantRed && (b8 < 200 || r8 > 100 || g8 > 100) {
			t.Fatalf("pixel(%d,%d) = (%d,%d,%d), want 蓝（下半天）", x, y, r8, g8, b8)
		}
	}
	checkRGB(370, 300, true)
	checkRGB(370, 1200, false)
}

// INT-186 清单给审查尺寸：有 StoragePath 的行 url 带缩放参数、original_url
// 不带、width/height 是原图尺寸、served 标示该地址返回哪种字节（小而轻
// original_light，长边超目标/小而重 scaled，非位图/超 64MP original_heavy）；
// 下载失败的行维持 error 且可选字段全部缺省。
func TestAgentScreenshotsReviewSizeItems(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	okURL := "https://github.com/user-attachments/assets/ok1"
	bigURL := "https://github.com/user-attachments/assets/big1"
	noteURL := "https://github.com/user-attachments/assets/note1"
	hugeURL := "https://github.com/user-attachments/assets/huge1"
	failURL := "https://github.com/user-attachments/assets/fail1"
	body := "![界面](" + okURL + ")\n\n![大图](" + bigURL + ")\n\n![笔记](" + noteURL + ")\n\n![假大图](" + hugeURL + ")\n\n![缺图](" + failURL + ")"
	e.fake.SetPR(repo, 99, &githubapi.PullRequest{
		RepoFullName: repo, Number: 99, HeadSHA: "sha-rs",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature", Body: body,
	})
	run := e.seedRun(t, repo, 99, "sha-rs", model.ReviewRunRunning, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "dispatch_started_at": now, "dispatched_at": now,
	})
	// 真 PNG 落盘当缓存产物（宽高可解码），failURLs 注入另一张的下载失败。
	media := &fakeReviewMedia{
		cached:   githubmedia.CachedImage(writeTestPNG(t, 640, 400), "image/png", 15),
		failURLs: map[string]error{failURL: errors.New("boom")},
	}
	// 大图、假大图与非位图三行直接落库（带 StoragePath，ensureAssets 查重跳过
	// 下载）：下载桩只有一份缓存，铺不出多种存储内容。大图行 served=scaled；
	// 假大图行宽高解不出但头声明超预算、非位图行嗅探不出，都归 original_heavy。
	seedScaledAsset := func(id string, pos int, path, mime string) {
		t.Helper()
		a := &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ID:            id, RepoFullName: repo, PRNumber: 99, HeadSHA: "sha-rs",
			Position: pos, Label: id, SourceURL: "https://github.com/user-attachments/assets/" + id,
			StoragePath: path, MimeType: mime, SizeBytes: 10, CreatedAt: now,
		}
		if err := e.svc.db.Create(a).Error; err != nil {
			t.Fatalf("seed asset %s: %v", id, err)
		}
	}
	seedScaledAsset("big1", 1, writeTestPNG(t, 2480, 2248), "image/png")
	notePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(notePath, []byte("definitely not an image"), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	seedScaledAsset("note1", 2, notePath, "application/octet-stream")
	seedScaledAsset("huge1", 3, writeFakeHugePNG(t), "image/png")
	review := e.withT3(nil, media, func(context.Context, string, int64) (string, error) {
		return "tok", nil
	})

	items, _, err := review.AgentScreenshots(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("AgentScreenshots: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	ok := items[0]
	wantOriginal := "/api/review-agent/runs/" + run.ID + "/screenshots/" + ok.Id
	if ok.Url == nil || *ok.Url == "" || ok.Error != nil {
		t.Fatalf("ok item = %+v, want url 非空且无 error", ok)
	}
	if *ok.Url != wantOriginal+reviewScaleQuery() {
		t.Fatalf("ok url = %q, want %q（审查尺寸地址）", *ok.Url, wantOriginal+reviewScaleQuery())
	}
	if ok.OriginalUrl == nil || *ok.OriginalUrl != wantOriginal {
		t.Fatalf("ok original_url = %v, want %q", ok.OriginalUrl, wantOriginal)
	}
	if ok.Width == nil || *ok.Width != 640 || ok.Height == nil || *ok.Height != 400 {
		t.Fatalf("ok dims = %v×%v, want 640×400", ok.Width, ok.Height)
	}
	// 小而轻（长边在目标内、字节远小于预算）：served=original_light——原图字
	// 节但落在预算内，代理可正常读。
	if ok.Served == nil || *ok.Served != "original_light" {
		t.Fatalf("ok served = %v, want original_light（小而轻原图）", ok.Served)
	}
	// 大图行：长边超目标，served=scaled。
	big := items[1]
	if big.Error != nil || big.Url == nil || big.Width == nil || *big.Width != 2480 || big.Height == nil || *big.Height != 2248 {
		t.Fatalf("big item = %+v, want 正常大图行", big)
	}
	if big.Served == nil || *big.Served != "scaled" {
		t.Fatalf("big served = %v, want scaled（长边超目标）", big.Served)
	}
	// 非位图行：宽高解不出（字段缺省）、served=original_heavy——原图字节且
	// 不受预算保护。
	note := items[2]
	if note.Error != nil || note.Url == nil || note.Width != nil || note.Height != nil {
		t.Fatalf("note item = %+v, want 宽高缺省的资产行", note)
	}
	if note.Served == nil || *note.Served != "original_heavy" {
		t.Fatalf("note served = %v, want original_heavy（非位图）", note.Served)
	}
	// 假大图行：IHDR 声明超 64MP（头解得动也不解码像素），served=original_heavy。
	huge := items[3]
	if huge.Error != nil || huge.Url == nil || huge.Served == nil || *huge.Served != "original_heavy" {
		t.Fatalf("huge item = %+v, want served=original_heavy（超 64MP）", huge)
	}
	fail := items[4]
	if fail.Error == nil || *fail.Error == "" || fail.Url != nil || fail.OriginalUrl != nil || fail.Width != nil || fail.Served != nil {
		t.Fatalf("fail item = %+v, want error 非空且无 url/original_url/width/served", fail)
	}
	// 线上表示：失败行的可选字段在 JSON 里是缺省（不是空值），锁住不含
	// url/original_url/width/height 键。
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal items: %v", err)
	}
	for _, key := range []string{"url", "original_url", "width", "height", "served"} {
		if _, present := rows[4][key]; present {
			t.Fatalf("失败行 JSON 含 %q 键: %s", key, raw)
		}
	}
	// 有 StoragePath 的行 served 必返：正常行 JSON 都含 served 键。
	for i := range 4 {
		if _, present := rows[i]["served"]; !present {
			t.Fatalf("行 %d JSON 缺 served 键: %s", i, raw)
		}
	}
}

// AgentScreenshotImage：无参数 → path 透传；带参数 → jpeg 变体字节；清单已标
// original_heavy 的（超像素预算、伪 heic）照常原图透传；served=scaled 行手造
// 「头可解但像素损坏」的 PNG → 40406 而非原图（不静默降级）；StoragePath
// 指向不存在的文件 → 40406 语义。
func TestAgentScreenshotImageScale(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 91, "sha-img", model.ReviewRunRunning, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "dispatch_started_at": now, "dispatched_at": now,
	})
	bigPath := writeTestPNG(t, 2480, 2248)
	missingPath := filepath.Join(t.TempDir(), "missing.png")
	seedAsset := func(id, path string) {
		t.Helper()
		a := &model.ReviewAsset{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ID:            id, RepoFullName: repo, PRNumber: 91, HeadSHA: "sha-img",
			Position: 0, Label: "界面", SourceURL: "https://github.com/user-attachments/assets/" + id,
			StoragePath: path, MimeType: "image/png", SizeBytes: 10, CreatedAt: now,
		}
		if err := e.svc.db.Create(a).Error; err != nil {
			t.Fatalf("seed asset: %v", err)
		}
	}
	seedAsset("asset-big", bigPath)
	seedAsset("asset-missing", missingPath)
	// 缺失行的原因必须优先透出（R13 契约：asset.Error > 通用文案）。
	if err := e.svc.db.Model(&model.ReviewAsset{}).Where("id = ?", "asset-missing").
		Update("error", "下载超时").Error; err != nil {
		t.Fatalf("seed missing reason: %v", err)
	}
	seedAsset("asset-huge", writeFakeHugePNG(t))
	seedAsset("asset-corrupt", writeCorruptPixelsPNG(t))
	// 伪 heic：ftyp 盒魔数、有 Content-Type 位图声明但本服务无解码器。
	heicPath := filepath.Join(t.TempDir(), "shot.heic")
	heicBody := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1hevc")
	if err := os.WriteFile(heicPath, heicBody, 0o600); err != nil {
		t.Fatalf("write heic: %v", err)
	}
	heic := &model.ReviewAsset{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ID:            "asset-heic", RepoFullName: repo, PRNumber: 91, HeadSHA: "sha-img",
		Position: 0, Label: "界面", SourceURL: "https://github.com/user-attachments/assets/asset-heic",
		StoragePath: heicPath, MimeType: "image/heic", SizeBytes: int64(len(heicBody)), CreatedAt: now,
	}
	if err := e.svc.db.Create(heic).Error; err != nil {
		t.Fatalf("seed heic asset: %v", err)
	}
	ctx := context.Background()
	review := e.review

	// 无缩放参数：path + 存储 mime 原样返回（Data nil，服务端不开文件）。
	file, err := review.AgentScreenshotImage(ctx, run.ID, "asset-big", nil)
	if err != nil || file.Path != bigPath || file.MIME != "image/png" || file.Data != nil {
		t.Fatalf("passthrough = %+v, %v", file, err)
	}

	// 带缩放参数：jpeg 变体字节，长边 = 审查尺寸。
	scale := &reviewScaleParams{Max: reviewScreenshotMaxEdge, Format: "jpeg", Quality: reviewScreenshotQuality}
	file, err = review.AgentScreenshotImage(ctx, run.ID, "asset-big", scale)
	if err != nil || file.Data == nil || file.MIME != "image/jpeg" {
		t.Fatalf("scaled = %d bytes, mime %q, %v", len(file.Data), file.MIME, err)
	}
	img, err := jpeg.Decode(bytes.NewReader(file.Data))
	if err != nil {
		t.Fatalf("scaled decode: %v", err)
	}
	if img.Bounds().Dx() != reviewScreenshotMaxEdge {
		t.Fatalf("scaled long edge = %d, want %d", img.Bounds().Dx(), reviewScreenshotMaxEdge)
	}

	// 原图超像素预算：清单已标 original_heavy，照常原图透传（Path + 存储
	// mime，Data nil）。
	file, err = review.AgentScreenshotImage(ctx, run.ID, "asset-huge", scale)
	if err != nil || file.Data != nil || file.Path == "" || file.MIME != "image/png" {
		t.Fatalf("over-budget passthrough = %+v, %v; want 原图透传", file, err)
	}

	// StoragePath 指向不存在的文件：带参数走缩放 → 40406 语义。
	// 按错误码断言而非 errors.Is：INT-184 的缺失文件归因对这条路返回同码
	// (40406) 带原因的消息（AssetError 优先），AppError 没有按码的 Is 方法，
	// errors.Is 对定制消息实例不成立；全局加 Is 会改变既有同码区分逻辑。
	_, err = review.AgentScreenshotImage(ctx, run.ID, "asset-missing", scale)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrIssueAssetNotFound.Code {
		t.Fatalf("missing file = %v, want code %d", err, errs.ErrIssueAssetNotFound.Code)
	}
	// 原因优先于通用文案（R13 契约的回归闸）：改回裸 sentinel 或丢掉
	// asset.Error 优先级时这里失败，而不是随 404 语义一起静默退化。
	if appErr.Message != "下载超时" {
		t.Fatalf("message = %q, want 行上记录的 asset.Error", appErr.Message)
	}

	// 伪 heic（有文件但无解码器，清单已标 original_heavy）：带缩放参数 → 原
	// 图透传（Data nil、Path 原样）而不是 40406——默认审查尺寸地址保持可读，
	// 与清单承诺一致。
	file, err = review.AgentScreenshotImage(ctx, run.ID, "asset-heic", scale)
	if err != nil || file.Data != nil || file.Path != heicPath || file.MIME != "image/heic" {
		t.Fatalf("original_heavy passthrough = %+v, %v; want 原图透传", file, err)
	}
	// 同文件不带参数：照常透传。
	file, err = review.AgentScreenshotImage(ctx, run.ID, "asset-heic", nil)
	if err != nil || file.Data != nil || file.Path != heicPath || file.MIME != "image/heic" {
		t.Fatalf("unscalable passthrough = %+v, %v; want 原图透传", file, err)
	}

	// served=scaled 行但像素损坏（真 PNG 头 + 截断 IDAT）：不再静默降级透传
	// 原图——清单承诺该地址是预算内变体，降级等于把十几 MB 塞给代理；返回
	// 40406 让代理记 unmeasurable。
	_, err = review.AgentScreenshotImage(ctx, run.ID, "asset-corrupt", scale)
	if !errors.Is(err, errs.ErrIssueAssetNotFound) {
		t.Fatalf("corrupt pixels = %v, want ErrIssueAssetNotFound（不降级原图）", err)
	}
}

// 动画 WebP（VP8X/ANIM）头能过 DecodeConfig 但 Decode 不支持 ANIM——清单
// 必须标 original_heavy 透传原图，否则取图 404 且原图明明可读（INT-186 第
// 5 轮）。样本直接写文件经 reviewScreenshotHead 取，钉死「嗅探样本 ≥28 字
// 节」这条接缝（第 3 轮 head 长度教训：只测纯函数会放过样本截断）。
func TestReviewScreenshotServedAnimatedWebp(t *testing.T) {
	// VP8X 精确布局：offset 12-15 "VP8X"、16-19 chunk size、20 flags、21-23 保
	// 留、24-26 画布宽-1 小端、27-29 画布高-1 小端；ANIM=flags bit1（0x02）。
	writeWebpHead := func(flags byte) string {
		t.Helper()
		var buf bytes.Buffer
		buf.WriteString("RIFF")
		buf.Write([]byte{0, 0, 0, 0})
		buf.WriteString("WEBP")
		buf.WriteString("VP8X")
		buf.Write([]byte{10, 0, 0, 0})      // chunk 长度 10
		buf.WriteByte(flags)                // flags 字节在偏移 20
		buf.Write([]byte{0, 0, 0})          // 保留 3 字节（21-23）
		buf.Write([]byte{0x2F, 0x00, 0x00}) // 画布宽-1 = 47（24-26）
		buf.Write([]byte{0x2F, 0x00, 0x00}) // 画布高-1 = 47（27-29）
		path := filepath.Join(t.TempDir(), "shot.webp")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("write webp head: %v", err)
		}
		return path
	}
	// flags=ANIM → original_heavy（DecodeConfig 能过但 Decode 不支持 ANIM，
	// 原图透传可读）。
	if got := reviewScreenshotServed(writeWebpHead(0x02), reviewScreenshotMaxEdge); got != ServedOriginalHeavy {
		t.Fatalf("animated webp served = %v, want original_heavy", got)
	}
	// flags=0 的静态 VP8X：不误伤为动画。上一版把 head[27] 高度低字节当
	// flags 读时 0x2F&0x02≠0 会误判——本用例锁 flags 在偏移 20 不被读错。
	still := writeWebpHead(0x00)
	head, err := reviewScreenshotHead(still)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if sniffWebpAnimation(head) {
		t.Fatalf("static vp8x flags=0 被误判为动画（head[27] 读错位）")
	}
	// 样本只到 flags 字节之前（<21 字节）→ 按保守返回 false，不越界读。
	if sniffWebpAnimation(head[:20]) {
		t.Fatalf("样本不足 flags 位（<21 字节）应返回 false")
	}
}
