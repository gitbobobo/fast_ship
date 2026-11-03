package service

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strconv"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// 审查尺寸缩略图参数（INT-186）：大折叠屏/平板截图长边 ≥2400px，审查代理逐张
// 读原图会撑爆模型网关的请求上限（413），而视觉判断布局/间距/对比度根本不需要
// 原图——服务端出缩放变体，原图保留按需获取途径。
const (
	// reviewScreenshotMaxEdge 清单默认给的审查尺寸长边，落在 1280-1600 建议带内。
	reviewScreenshotMaxEdge = 1536
	// reviewScreenshotQuality 清单默认的 JPEG 编码质量。
	reviewScreenshotQuality = 80
	// reviewScreenshotMaxBytes 单张缩放变体的字节预算：提示词「读图预算」向
	// 审查代理承诺单张 ≤500KB——服务端强制版，编码后查字节数，超了降质量重
	// 编、仍超对半降边，而不是指望模型自律（不可压缩的繁忙截图 q80 一遍编码
	// 就会超线）。
	reviewScreenshotMaxBytes = 500 << 10
)

// 缩放参数的 clamp 边界与像素预算：清单 URL 由服务端按常量生成，agent 手改
// 参数只影响自己读到的清晰度，超界收进边界而不是报错；像素预算对齐
// screenshot_annotation 的解码上限——恶意大图不能在服务端解码时爆内存。
const (
	reviewScaleMinEdge    = 320
	reviewScaleMaxEdge    = 2000
	reviewScaleMinQuality = 30
	reviewScaleMaxQuality = 95
	reviewScaleMaxPixels  = 64_000_000
)

// errReviewScaleTooLarge 原图超过解码像素预算：不做缩放，调用方按原图字节透传
// （原图流式下发不经内存解码，是安全的）。
var errReviewScaleTooLarge = errors.New("review screenshot exceeds decode pixel budget")

// errReviewScaleUnscalable 有文件但出不了缩放变体（嗅探失败、头/像素解码失败、
// 编码失败）：调用方同样按原图字节透传——原始字节流式下发始终安全，默认审查
// 尺寸地址保持可读，而不是把清单里已下发的地址变成 404。文件打不开（含不存
// 在）不算此类，仍走普通错误 → 40406。
var errReviewScaleUnscalable = errors.New("review screenshot cannot be scaled")

// reviewScaleParams 图片端点的缩放参数（parseReviewScaleQuery 产出）。
type reviewScaleParams struct {
	Max     int
	Format  string // "jpeg" 或 "png"
	Quality int
}

// ParseReviewScaleQuery 供 handler 层解析图片端点的 max/format/quality query；
// 实现见 parseReviewScaleQuery。
func ParseReviewScaleQuery(maxRaw, formatRaw, qualityRaw string) (*reviewScaleParams, error) {
	return parseReviewScaleQuery(maxRaw, formatRaw, qualityRaw)
}

// parseReviewScaleQuery 解析缩放参数：三个参数全空 → nil（原图透传语义）；
// 给了任何一个则其余按默认值补全；非法数值报错（调用方回 400）。
func parseReviewScaleQuery(maxRaw, formatRaw, qualityRaw string) (*reviewScaleParams, error) {
	if maxRaw == "" && formatRaw == "" && qualityRaw == "" {
		return nil, nil
	}
	p := &reviewScaleParams{
		Max:     reviewScreenshotMaxEdge,
		Format:  "jpeg",
		Quality: reviewScreenshotQuality,
	}
	if maxRaw != "" {
		n, err := strconv.Atoi(maxRaw)
		if err != nil {
			return nil, fmt.Errorf("max 不是合法整数")
		}
		p.Max = clampReviewScale(n, reviewScaleMinEdge, reviewScaleMaxEdge)
	}
	if formatRaw != "" {
		// webp 只支持解码不支持编码（x/image 无 webp 编码器），不在选项里。
		if formatRaw != "jpeg" && formatRaw != "png" {
			return nil, fmt.Errorf("format 只支持 jpeg 或 png")
		}
		p.Format = formatRaw
	}
	if qualityRaw != "" {
		n, err := strconv.Atoi(qualityRaw)
		if err != nil {
			return nil, fmt.Errorf("quality 不是合法整数")
		}
		p.Quality = clampReviewScale(n, reviewScaleMinQuality, reviewScaleMaxQuality)
	}
	return p, nil
}

// clampReviewScale 把参数收进 [lo, hi]。
func clampReviewScale(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// reviewScaleQuery 清单 url 追加的缩放参数串：审查尺寸默认值按常量拼，
// 不散落魔法数。
func reviewScaleQuery() string {
	return "?max=" + strconv.Itoa(reviewScreenshotMaxEdge) +
		"&format=jpeg&quality=" + strconv.Itoa(reviewScreenshotQuality)
}

// sniffReviewScaleFormat 读文件头魔数判真实图片类型：storage 的 MimeType 可能
// 为空/octet-stream/说谎，缩放分派只信魔数。判不出 png/jpeg/gif/webp（本服务
// 能解码的位图）返回空串。
func sniffReviewScaleFormat(head []byte) string {
	switch {
	case len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(head) >= 2 && head[0] == 0xFF && head[1] == 0xD8:
		return "image/jpeg"
	case len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		return "image/gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// reviewScreenshotHead 读文件前 16 字节做嗅探样本；短文件按实际读到的算。
func reviewScreenshotHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var head [16]byte
	n, _ := io.ReadFull(f, head[:])
	return head[:n], nil
}

// reviewScreenshotDims 原图像素尺寸（EXIF 方向应用后）：先按魔数嗅探真实类型
// 再解码文件头。打不开或解不出 → 0, 0（清单里 width/height 置 null），清单
// 不因单张元数据失败而中断。
func reviewScreenshotDims(path string) (int, int) {
	head, err := reviewScreenshotHead(path)
	if err != nil {
		return 0, 0
	}
	kind := sniffReviewScaleFormat(head)
	if kind == "" {
		return 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, err := decodeScreenshotImageConfig(f, kind)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// scaleReviewScreenshot 生成审查尺寸缩放变体：魔数嗅探真实类型 → 解码原图尺寸
// → 像素预算守卫 → 长边不大于目标时返回 nil data（调用方按原图透传，不放大）
// → 解码像素（JPEG 应用 EXIF 方向）→ 按字节预算编码（见 encodeReviewScaleBytes）。
// 错误分两类：文件打不开（含不存在）返回普通错误 → 调用方 40406；其余「有文
// 件但缩不了」返回 errReviewScaleUnscalable → 调用方按原图透传。mimeType 只是
// storage 的声明，真值以嗅探为准。返回 nil data 且 err 为 nil 表示不需要缩放变体。
func scaleReviewScreenshot(path, mimeType string, p *reviewScaleParams) (data []byte, mime string, err error) {
	head, err := reviewScreenshotHead(path)
	if err != nil {
		return nil, "", err // 打不开（含不存在）→ 调用方 40406
	}
	kind := sniffReviewScaleFormat(head)
	if kind == "" {
		return nil, "", fmt.Errorf("%w: 嗅探不出可解码的位图格式", errReviewScaleUnscalable)
	}
	cf, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	cfg, cfgErr := decodeScreenshotImageConfig(cf, kind)
	cf.Close()
	if cfgErr != nil {
		return nil, "", fmt.Errorf("%w: 解码图片头失败: %v", errReviewScaleUnscalable, cfgErr)
	}
	// 像素预算守卫：头里声明的尺寸超限时根本不解码像素（防解码爆内存），
	// 报超限错误由调用方回退原图透传。
	if int64(cfg.Width)*int64(cfg.Height) > reviewScaleMaxPixels {
		return nil, "", errReviewScaleTooLarge
	}
	long := cfg.Width
	if cfg.Height > long {
		long = cfg.Height
	}
	if long <= p.Max {
		return nil, "", nil
	}
	img, err := decodeReviewScreenshotPixels(path, kind)
	if err != nil {
		return nil, "", err
	}
	return encodeReviewScaleBytes(img, p)
}

// encodeReviewScaleBytes 缩放并按字节预算编码：先缩到 p.Max 长边按请求档位编
// 码（典型 UI 截图这一遍就达标，零额外成本）；超 reviewScreenshotMaxBytes 时
// JPEG 降质量重编、最低档仍超就对半降目标长边（下限 reviewScaleMinEdge）从
// p.Quality 重新走梯次，第一份进预算的胜出。长边到下限仍超（病态输入）兑底
// 返回全循环字节最小的一份——预算是尽力保证，最小的一份也总好过把清单已下
// 发的地址变成 404。
func encodeReviewScaleBytes(img image.Image, p *reviewScaleParams) ([]byte, string, error) {
	// JPEG 质量梯次：请求档位打头，其后只收严格更低的预设档（请求 50 不回
	// 跳 65；请求档本身是低档时自然去重）。PNG 没有质量旋钮，每个长边只编
	// 一遍（qualities 里的档位不被用到）。
	qualities := []int{p.Quality}
	for _, q := range [...]int{65, 50, 35} {
		if q < p.Quality {
			qualities = append(qualities, q)
		}
	}
	pngOut := p.Format == "png"
	var best []byte
	bestMime := ""
	edge := p.Max
	for {
		scaled := scaleToReviewEdge(img, edge)
		var flat *image.RGBA
		if !pngOut {
			flat = flattenReviewAlphaToWhite(scaled)
		}
		for _, q := range qualities {
			var buf bytes.Buffer
			var encErr error
			mime := "image/jpeg"
			if pngOut {
				mime = "image/png"
				encErr = png.Encode(&buf, scaled)
			} else {
				encErr = jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q})
			}
			if encErr != nil {
				return nil, "", fmt.Errorf("%w: 变体编码失败: %v", errReviewScaleUnscalable, encErr)
			}
			if best == nil || buf.Len() < len(best) {
				best, bestMime = buf.Bytes(), mime
			}
			if buf.Len() <= reviewScreenshotMaxBytes {
				return buf.Bytes(), mime, nil
			}
		}
		if edge <= reviewScaleMinEdge {
			break
		}
		// 当前长边的最低档仍超预算：目标长边对半（下限 reviewScaleMinEdge）
		// 重走梯次。
		edge /= 2
		if edge < reviewScaleMinEdge {
			edge = reviewScaleMinEdge
		}
	}
	return best, bestMime, nil
}

// decodeReviewScreenshotPixels 按嗅探结果解码整图像素：png/webp 走各自解码器，
// gif 按逻辑画布合成首帧（首帧可能小于 DecodeConfig 的画布且带偏移），jpeg
// 解码后重开文件扫 EXIF 方向再重排（方向不在像素流里，流已被解码耗尽）。解
// 码失败包成 errReviewScaleUnscalable（os.Open 失败除外——那是 40406 语义）。
// 与 screenshot_annotation 的裁剪解码分开——这里要全图，走紧凑分派。
func decodeReviewScreenshotPixels(path, kind string) (image.Image, error) {
	unscalable := func(err error) error {
		return fmt.Errorf("%w: %v", errReviewScaleUnscalable, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	switch kind {
	case "image/png":
		img, err := png.Decode(f)
		if err != nil {
			return nil, unscalable(err)
		}
		return img, nil
	case "image/webp":
		img, err := webp.Decode(f)
		if err != nil {
			return nil, unscalable(err)
		}
		return img, nil
	case "image/gif":
		// 首帧可能小于 DecodeConfig 的逻辑画布且带偏移（画布留给后续帧/背景）：
		// 直接缩首帧会裁掉留白甚至反向放大。重开文件取逻辑画布尺寸建 RGBA，
		// 首帧在原偏移处 Over 合成进去再返回——缩放决策用的 DecodeConfig 画
		// 布尺寸与合成结果一致。
		c2, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer c2.Close()
		ccfg, err := gif.DecodeConfig(c2)
		if err != nil {
			return nil, unscalable(err)
		}
		frame, err := gif.Decode(f)
		if err != nil {
			return nil, unscalable(err)
		}
		canvas := image.NewRGBA(image.Rect(0, 0, ccfg.Width, ccfg.Height))
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		return canvas, nil
	case "image/jpeg":
		img, err := jpeg.Decode(f)
		if err != nil {
			return nil, unscalable(err)
		}
		r2, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer r2.Close()
		_, orientation, err := jpegScanHeader(r2)
		if err != nil {
			orientation = 0
		}
		return applyJPEGExifOrientation(img, orientation), nil
	}
	return nil, fmt.Errorf("unsupported screenshot format %q", kind)
}

// scaleToReviewEdge 等比缩到长边 = max（短边按整数四舍五入，至少 1px）：
// CatmullRom 卷积核在文字与边缘上的重采样质量明显好于近邻，适合审查读图；
// 目标固定 RGBA。
func scaleToReviewEdge(src image.Image, max int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := max, max
	if w >= h {
		nh = (h*max + w/2) / w
	} else {
		nw = (w*max + h/2) / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}

// flattenReviewAlphaToWhite 把缩放结果合成到白底：JPEG 没有透明通道，RGBA 的
// 透明像素直接编码会变成黑（零值），视觉审查会把留白看成黑块。
func flattenReviewAlphaToWhite(src *image.RGBA) *image.RGBA {
	canvas := image.NewRGBA(src.Bounds())
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), src, src.Bounds().Min, draw.Over)
	return canvas
}
