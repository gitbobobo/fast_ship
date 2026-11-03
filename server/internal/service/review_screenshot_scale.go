package service

import (
	"bytes"
	"context"
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

// reviewScaleSemCapacity 解码字节闸按容量 clamp 的上限，与 screenshotCropBytesSem
// 的总量一致：Acquire 权重大于总量会永远失败，必须 clamp。
const reviewScaleSemCapacity = 1024 << 20

// ReviewScreenshotServed 审查尺寸地址对本次请求返回什么字节的归类：清单
// served 字段与取图端点的变体/透传决策共用同一套枚举（reviewScreenshotServed
// 一处判定，两处消费，永不打架）。
type ReviewScreenshotServed string

const (
	// ServedScaled 返回审查尺寸缩放变体：长边超目标的降边缩放、小而重原图按
	// 原尺寸重编码压预算——单张字节落在预算内，代理正常读。
	ServedScaled ReviewScreenshotServed = "scaled"
	// ServedOriginalLight 返回原图字节但在预算内：长边 ≤ 清单默认目标且文件
	// ≤500KB——零损失透传，代理可正常读（1440×900 桌面截图这类最常见证据）。
	ServedOriginalLight ReviewScreenshotServed = "original_light"
	// ServedOriginalHeavy 返回原图字节且不受预算保护：宽×高 >64MP 解码预算、
	// 嗅探/头解码不出或文件打不开——代理尽量不读。
	ServedOriginalHeavy ReviewScreenshotServed = "original_heavy"
)

// errReviewScaleTooLarge 原图超过解码像素预算：清单已按 original_heavy 标示
// （判定与清单共用），取图保留此防御分支按原图字节透传（原图流式下发不经
// 内存解码，是安全的）。
var errReviewScaleTooLarge = errors.New("review screenshot exceeds decode pixel budget")

// errReviewScaleVariantFailed 清单已标 ServedScaled 的行生成变体失败（像素
// 解码、变体编码、解码字节闸等待被取消）：不再静默降级透传原图——清单承诺
// 该地址是预算内的审查尺寸变体，降级等于把十几 MB 原图塞给代理。调用方返回
// 明确错误（40406 语义），代理读到错误记 unmeasurable。
var errReviewScaleVariantFailed = errors.New("review screenshot variant generation failed")

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

// sniffWebpAnimation 判样本是否为动画 WebP：WEBP 头后首个 chunk 是 VP8X
// （偏移 12-15 "VP8X"、16-19 chunk size、20-23 保留、24-26 画布宽-1 小端、
// 27-29 画布高-1 小端），特性位是 payload 第 1 字节（偏移 20），ANIM=bit1
// （0x02）。样本不足或首 chunk 不是 VP8X 返回 false（DecodeConfig 会把不可
// 解的挡到 heavy——本函数只提前拦「头能过但 Decode 不支持 ANIM」的动画）。
func sniffWebpAnimation(head []byte) bool {
	if len(head) < 21 || string(head[12:16]) != "VP8X" {
		return false
	}
	return head[20]&0x02 != 0
}

// reviewScreenshotHead 读文件前 32 字节做嗅探与判定样本：PNG 位深（IHDR
// bit depth 字节在偏移 24）、WebP 动画位（VP8X 在偏移 12，payload 第 4 字节
// bit 6，样本至少 30 字节才覆盖）；短文件按实际读到的算。
func reviewScreenshotHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var head [32]byte
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

// reviewScreenshotServed 判定审查尺寸地址对本次请求返回什么字节：清单算
// served 标示与取图端点的变体/透传决策共用同一份逻辑，两处永不打架。
//   - ServedScaled：返回缩放变体（长边超目标的降边缩放、小而重原图按原尺寸
//     重编码压预算）——长边超目标、或长边在目标内但原图文件字节超 500KB 预算。
//   - ServedOriginalLight：返回原图字节但在预算内（长边 ≤ 目标且文件 ≤500KB
//     的小而轻原图，零损失透传）。
//   - ServedOriginalHeavy：返回原图字节且不受预算保护（嗅探/头解码失败、超
//     64MP 解码预算；文件打不开也归此类兜底——端点本就会 40406，清单按最重
//     原图语义标示）。
func reviewScreenshotServed(path string, maxEdge int) ReviewScreenshotServed {
	head, err := reviewScreenshotHead(path)
	if err != nil {
		return ServedOriginalHeavy
	}
	kind := sniffReviewScaleFormat(head)
	if kind == "" {
		return ServedOriginalHeavy
	}
	// 动画 WebP（VP8X 带 ANIM 位）：DecodeConfig 能返回画布尺寸但 Decode 不
	// 支持 ANIM/ANMF 首帧，清单标 scaled 会让取图 404——原图可读，按 heavy
	// 透传。
	if kind == "image/webp" && sniffWebpAnimation(head) {
		return ServedOriginalHeavy
	}
	f, err := os.Open(path)
	if err != nil {
		return ServedOriginalHeavy
	}
	cfg, err := decodeScreenshotImageConfig(f, kind)
	f.Close()
	if err != nil {
		return ServedOriginalHeavy
	}
	if int64(cfg.Width)*int64(cfg.Height) > reviewScaleMaxPixels {
		return ServedOriginalHeavy
	}
	if max(cfg.Width, cfg.Height) > maxEdge {
		return ServedScaled
	}
	// 长边在目标内时按原图文件字节分流（os.Stat 一次，清单已算过 dims，便宜）：
	// 预算内零损失透传，超预算的原尺寸重编码出变体——审查尺寸承诺的是字节上
	// 限而不是像素下限，小而重的桌面截图（1512×982 PNG 常 1-3MB）单张就能击
	// 穿预算，不能因为「不用缩」就无条件透传。
	fi, err := os.Stat(path)
	if err != nil {
		return ServedOriginalHeavy
	}
	if fi.Size() > reviewScreenshotMaxBytes {
		return ServedScaled
	}
	return ServedOriginalLight
}

// scaleReviewScreenshot 生成审查尺寸缩放变体：先过共享判定
// （reviewScreenshotServed）——original_light/original_heavy 原图透传（与清
// 单承诺一致）；ServedScaled 解码重编码：嗅探 → 解码原图尺寸 → 像素预算守卫
// （防御分支）→ 占解码字节闸（权重按格式口径，见
// screenshotDecodeBytesPerPixel）→ 解码像素（JPEG 应用 EXIF 方向）→ 按字节
// 预算编码（见 encodeReviewScaleBytes）。scaled 路径的像素解码、变体编码、
// 字节闸失败返回 errReviewScaleVariantFailed → 调用方 40406，不静默降级透传
// 原图（清单已承诺该地址是预算内变体）；文件打不开（含不存在）返回普通错误
// → 调用方 40406。返回 nil
// data 且 err 为 nil 表示不需要缩放变体（original_* 原图透传）。
func scaleReviewScreenshot(ctx context.Context, path string, p *reviewScaleParams) (data []byte, mime string, err error) {
	head, err := reviewScreenshotHead(path)
	if err != nil {
		return nil, "", err // 打不开（含不存在）→ 调用方 40406
	}
	// 变体/透传决策与清单 served 标示共用同一份逻辑（reviewScreenshotServed）：
	// original_* 原图透传（与清单承诺一致）；ServedScaled 走到下面解码重编码。
	if served := reviewScreenshotServed(path, p.Max); served != ServedScaled {
		return nil, "", nil
	}
	kind := sniffReviewScaleFormat(head)
	if kind == "" {
		// 判定时嗅探得过、现在嗅探不过：文件在两次读之间被换掉了。按变体失败
		// 兜底（与像素解码失败同路），不透传与清单承诺矛盾的原图。
		return nil, "", fmt.Errorf("%w: 嗅探不出可解码的位图格式", errReviewScaleVariantFailed)
	}
	cf, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	cfg, cfgErr := decodeScreenshotImageConfig(cf, kind)
	cf.Close()
	if cfgErr != nil {
		// 同上：判定时头解得动、现在解不动——按变体失败兜底。
		return nil, "", fmt.Errorf("%w: 解码图片头失败: %v", errReviewScaleVariantFailed, cfgErr)
	}
	// 像素预算守卫（防御分支，判定已过滤超限；文件中途被换时兜住，防解码爆
	// 内存）：报超限错误由调用方回退原图透传。
	if int64(cfg.Width)*int64(cfg.Height) > reviewScaleMaxPixels {
		return nil, "", errReviewScaleTooLarge
	}
	long := max(cfg.Width, cfg.Height)
	// 解码字节闸与截图标注裁剪路径共用（总量 1GiB）：并行视觉运行的解码驻留
	// 不叠加爆内存，等待随请求 ctx 取消。权重按驻留估，口径与 crop 同源——
	// jpeg/gif 12（EXIF 重排/画布合成多一份全尺寸副本）、16 位 PNG 16（RGBA64
	// 8B/px 源 + 缩放输出 8B/px）、其余 8；超总容量按容量 clamp。
	weight := int64(cfg.Width) * int64(cfg.Height) * screenshotDecodeBytesPerPixel(kind, head)
	if weight > reviewScaleSemCapacity {
		weight = reviewScaleSemCapacity
	}
	if err := screenshotCropBytesSem.Acquire(ctx, weight); err != nil {
		return nil, "", fmt.Errorf("%w: 解码字节闸等待被取消: %v", errReviewScaleVariantFailed, err)
	}
	defer screenshotCropBytesSem.Release(weight)
	img, err := decodeReviewScreenshotPixels(path, kind)
	if err != nil {
		// 头是好的、像素是坏的（截断下载、IDAT 损坏）：不再伪装成「已知不可
		// 解码」透传原图，返回变体失败由调用方 40406。
		return nil, "", err
	}
	// 初始长边取 min(目标, 原长边)：降边缩放取目标；小而重重编码取原长边
	// （不放大，也不做 1:1 重采样软化）。
	return encodeReviewScaleBytes(img, min(p.Max, long), p)
}

// encodeReviewScaleBytes 缩放并按字节预算编码：先把长边缩到 edge0（调用方
// 取 min(目标, 原长边)，小而重重编码时等于原图长边）按请求档位编码（典型 UI
// 截图这一遍就达标，零额外成本）；超 reviewScreenshotMaxBytes 时 JPEG 降质量
// 重编、最低档仍超就对半降目标长边（下限 reviewScaleMinEdge）从 p.Quality 重
// 新走梯次，第一份进预算的胜出。长边到下限仍超（病态输入）兑底返回全循环字
// 节最小的一份——预算是尽力保证，最小的一份也总好过把清单已下发的地址变成
// 404。
func encodeReviewScaleBytes(img image.Image, edge0 int, p *reviewScaleParams) ([]byte, string, error) {
	// JPEG 质量梯次：请求档位打头，其后只收严格更低的预设档（请求 50 不回
	// 跳 65；请求档本身是低档时自然去重）。PNG 没有质量旋钮，qualities 只留
	// 请求档单元素——每个长边只编一遍，不白编。
	pngOut := p.Format == "png"
	qualities := []int{p.Quality}
	if !pngOut {
		for _, q := range [...]int{65, 50, 35} {
			if q < p.Quality {
				qualities = append(qualities, q)
			}
		}
	}
	srcLong := max(img.Bounds().Dx(), img.Bounds().Dy())
	var best []byte
	bestMime := ""
	edge := edge0
	for {
		// 长边不小于原图时不重采样（1:1 CatmullRom 只会软化像素），直接编码原
		// 图像素；降边路径才走缩放。
		var pixels image.Image = img
		if edge < srcLong {
			pixels = scaleToReviewEdge(img, edge)
		}
		var flat *image.RGBA
		if !pngOut {
			flat = flattenReviewAlphaToWhite(pixels)
		}
		for _, q := range qualities {
			var buf bytes.Buffer
			var encErr error
			mime := "image/jpeg"
			if pngOut {
				mime = "image/png"
				encErr = png.Encode(&buf, pixels)
			} else {
				encErr = jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q})
			}
			if encErr != nil {
				return nil, "", fmt.Errorf("%w: 变体编码失败: %v", errReviewScaleVariantFailed, encErr)
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
// 码失败包成 errReviewScaleVariantFailed（os.Open 失败除外——那是 40406 语义）。
// 与 screenshot_annotation 的裁剪解码分开——这里要全图，走紧凑分派。
func decodeReviewScreenshotPixels(path, kind string) (image.Image, error) {
	unscalable := func(err error) error {
		return fmt.Errorf("%w: %v", errReviewScaleVariantFailed, err)
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

// flattenReviewAlphaToWhite 把待编码像素合成到白底：JPEG 没有透明通道，透明
// 像素直接编码会变成黑（零值），视觉审查会把留白看成黑块。输入任意
// image.Image——小而重的原尺寸编码路径没有经过缩放产出 RGBA。
func flattenReviewAlphaToWhite(src image.Image) *image.RGBA {
	canvas := image.NewRGBA(src.Bounds())
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), src, src.Bounds().Min, draw.Over)
	return canvas
}
