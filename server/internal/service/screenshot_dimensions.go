package service

import (
	"strings"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 三个变体维度（platform/locale/theme）的规范化与校验。
// 一律「trim 后空串 = 清除为未标注（nil）」；platform 开放词表小写化，
// locale 做 BCP 47 规范化（非法值 400），theme 只接受 light|dark。

const (
	maxScreenshotPlatformRunes = 100
	// RFC 5646 语言标签长度上限
	maxScreenshotLocaleRunes = 35
)

// normalizeScreenshotPlatform 规范化平台：trim+lowercase；空串返回 nil（未标注）。
// 保留字两类直接拒绝（40001）：逗号是过滤参数的保留分隔符（多值逗号拼接），
// 含逗号的值无法在 URL/批量过滤里往返；「未标注」哨兵 __none__ 是过滤保留字，
// 一经小写化必与 screenshotDimensionNone 同形，大小写变体一并覆盖。
func normalizeScreenshotPlatform(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.ToLower(strings.TrimSpace(*raw))
	if v == "" {
		return nil, nil
	}
	if v == screenshotDimensionNone {
		return nil, errs.ErrInvalidParams
	}
	if strings.Contains(v, ",") || utf8.RuneCountInString(v) > maxScreenshotPlatformRunes {
		return nil, errs.ErrInvalidParams
	}
	return &v, nil
}

// NormalizeScreenshotPlatform 是平台规范化的跨包入口：启动迁移等外部写入口
// 与服务内写入路径共用同一套规则（trim+lowercase、保留字拒绝）。
func NormalizeScreenshotPlatform(raw string) (*string, error) {
	return normalizeScreenshotPlatform(&raw)
}

// normalizeScreenshotLocale 规范化语言标签：trim 后按 BCP 47（RFC 5646）完整
// 语法校验（langtag / privateuse / grandfathered），并规范化大小写（语言与
// 扩展小写、script 首字母大写、region 大写）；空串返回 nil，非法值 400。
func normalizeScreenshotLocale(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(v) > maxScreenshotLocaleRunes {
		return nil, errs.ErrInvalidParams
	}
	return normalizeBCP47Locale(strings.ToLower(v))
}

// grandfatheredLocales 是 RFC 5646 注册的 grandfathered 标签（无法用 langtag
// 语法推导，必须整表匹配）；键为小写形式，值为规范化输出。
var grandfatheredLocales = map[string]string{
	"en-gb-oed": "en-GB-oed",
	"i-ami":     "i-ami", "i-bnn": "i-bnn", "i-default": "i-default",
	"i-enochian": "i-enochian", "i-hak": "i-hak", "i-klingon": "i-klingon",
	"i-lux": "i-lux", "i-mingo": "i-mingo", "i-navajo": "i-navajo",
	"i-pwn": "i-pwn", "i-tao": "i-tao", "i-tay": "i-tay", "i-tsu": "i-tsu",
	"sgn-be-fr": "sgn-BE-FR", "sgn-be-nl": "sgn-BE-NL", "sgn-ch-de": "sgn-CH-DE",
	"art-lojban": "art-lojban", "cel-gaulish": "cel-gaulish",
	"no-bok": "no-bok", "no-nyn": "no-nyn",
	"zh-guoyu": "zh-guoyu", "zh-hakka": "zh-hakka", "zh-min": "zh-min",
	"zh-min-nan": "zh-min-nan", "zh-xiang": "zh-xiang",
}

// normalizeBCP47Locale 按语法解析并规范化语言标签（输入须已小写）：
// langtag 依次为 language [extlang…] [script] [region] [variant…]
// [extension…] [privateuse]，各段结构与顺序都要合法；privateuse-only（x-…）
// 单独成类。输出保持各子标签的规范化大小写。
func normalizeBCP47Locale(lower string) (*string, error) {
	if canonical, ok := grandfatheredLocales[lower]; ok {
		return &canonical, nil
	}
	segments := strings.Split(lower, "-")
	if segments[0] == "x" {
		if len(segments) < 2 {
			return nil, errs.ErrInvalidParams
		}
		for _, sub := range segments[1:] {
			if !validSubtag(sub, 1, 8) {
				return nil, errs.ErrInvalidParams
			}
		}
		return &lower, nil
	}

	n := len(segments)
	i := 0
	lang := segments[0]
	if !isAlpha(lang) {
		return nil, errs.ErrInvalidParams
	}
	switch {
	case len(lang) >= 2 && len(lang) <= 3:
		// language = 2*3ALPHA ["-" extlang]，extlang = 3ALPHA *2("-" 3ALPHA)：
		// 最多跟 3 个连续 3 字母段（3 字母段落在 language 之后只可能是 extlang）
		i = 1
		for extlangs := 0; i < n && extlangs < 3 && isAlpha(segments[i]) && len(segments[i]) == 3; extlangs++ {
			i++
		}
	case len(lang) >= 4 && len(lang) <= 8:
		i = 1
	default:
		return nil, errs.ErrInvalidParams
	}

	// script：4 字母，至多一个
	if i < n && isAlpha(segments[i]) && len(segments[i]) == 4 {
		segments[i] = titleCase(segments[i])
		i++
	}
	// region：2 字母或 3 数字，至多一个
	if i < n && ((isAlpha(segments[i]) && len(segments[i]) == 2) || (isDigits(segments[i]) && len(segments[i]) == 3)) {
		if isAlpha(segments[i]) {
			segments[i] = strings.ToUpper(segments[i])
		}
		i++
	}
	// variant：5-8 字母数字，或数字开头的 4 字母，可多个但不得重复
	// （§2.2.5；输入已小写，直接按段值判重）
	isVariant := func(seg string) bool {
		if len(seg) >= 5 && len(seg) <= 8 && isAlnum(seg) {
			return true
		}
		return len(seg) == 4 && isDigits(seg[:1]) && isAlnum(seg)
	}
	seenVariants := map[string]bool{}
	for i < n && isVariant(segments[i]) {
		if seenVariants[segments[i]] {
			return nil, errs.ErrInvalidParams
		}
		seenVariants[segments[i]] = true
		i++
	}
	// extension：单字符 singleton（RFC 5646 §2.1 限字母数字，x 除外）
	// + 1~N 个 2-8 字母数字子标签；同一 singleton 不得重复
	seenSingletons := map[byte]bool{}
	for i < n && len(segments[i]) == 1 && isAlnum(segments[i]) && segments[i][0] != 'x' {
		singleton := segments[i][0]
		if seenSingletons[singleton] {
			return nil, errs.ErrInvalidParams
		}
		seenSingletons[singleton] = true
		i++
		subtags := 0
		for i < n && validSubtag(segments[i], 2, 8) {
			i++
			subtags++
		}
		if subtags == 0 {
			return nil, errs.ErrInvalidParams
		}
	}
	// privateuse：x + 1~N 个 1-8 字母数字子标签，必须收尾
	if i < n {
		if segments[i] != "x" {
			return nil, errs.ErrInvalidParams
		}
		i++
		subtags := 0
		for i < n && validSubtag(segments[i], 1, 8) {
			i++
			subtags++
		}
		if subtags == 0 || i != n {
			return nil, errs.ErrInvalidParams
		}
	}
	if i != n {
		return nil, errs.ErrInvalidParams
	}
	out := strings.Join(segments, "-")
	return &out, nil
}

// validSubtag 报告子标签是否为 lower~upper（含）长度的纯字母数字。
func validSubtag(seg string, lower, upper int) bool {
	if len(seg) < lower || len(seg) > upper {
		return false
	}
	return isAlnum(seg)
}

func isAlpha(seg string) bool {
	for i := 0; i < len(seg); i++ {
		if seg[i] < 'a' || seg[i] > 'z' {
			return false
		}
	}
	return len(seg) > 0
}

func isDigits(seg string) bool {
	for i := 0; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return false
		}
	}
	return len(seg) > 0
}

func isAlnum(seg string) bool {
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return len(seg) > 0
}

func titleCase(seg string) string {
	if seg == "" {
		return seg
	}
	return strings.ToUpper(seg[:1]) + seg[1:]
}

// normalizeScreenshotTheme 校验亮度主题：只接受 light|dark；空串返回 nil（未标注），
// 其他值 400。
func normalizeScreenshotTheme(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil, nil
	}
	if v != "light" && v != "dark" {
		return nil, errs.ErrInvalidParams
	}
	return &v, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseScreenshotDimensionFilterQuery 解析 query 上逗号分隔的维度过滤：
// `__none__` 摘出为「未标注」桶，其余逐值走与写入相同的规范化
// （platform 小写、locale BCP 47、theme light|dark），保证过滤值与库存值
// 能对上；非法值 40001。handler 与批量过滤共用同一口径。
func parseScreenshotDimensionFilterQuery(raw string, normalize func(*string) (*string, error)) ([]string, bool, error) {
	if raw == "" {
		return nil, false, nil
	}
	return normalizeScreenshotFilterValues(strings.Split(raw, ","), normalize)
}

// ParseScreenshotPlatformFilter 解析列表过滤的 platform 维度：值 trim+小写。
func ParseScreenshotPlatformFilter(raw string) ([]string, bool, error) {
	return parseScreenshotDimensionFilterQuery(raw, normalizeScreenshotPlatform)
}

// ParseScreenshotLocaleFilter 解析列表过滤的 locale 维度：按 BCP 47 规范化
// 大小写（en-us 与库存里的 en-US 匹配）；非法值 40001。
func ParseScreenshotLocaleFilter(raw string) ([]string, bool, error) {
	return parseScreenshotDimensionFilterQuery(raw, normalizeScreenshotLocale)
}

// ParseScreenshotThemeFilter 解析列表过滤的 theme 维度：只认 light|dark。
func ParseScreenshotThemeFilter(raw string) ([]string, bool, error) {
	return parseScreenshotDimensionFilterQuery(raw, normalizeScreenshotTheme)
}

// normalizeScreenshotFilterValues 规范化批量过滤里的一个维度值数组：
// 哨兵（不区分大小写，EqualFold）必须在 normalize 之前摘出为「未标注」桶，
// 否则 __NONE__ 会被平台词表当保留字拒成 400；其余逐值走与写入相同的规范化。
func normalizeScreenshotFilterValues(values []string, normalize func(*string) (*string, error)) ([]string, bool, error) {
	includeNone := false
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if strings.EqualFold(v, screenshotDimensionNone) {
			includeNone = true
			continue
		}
		normalized, err := normalize(&v)
		if err != nil {
			return nil, false, err
		}
		if normalized == nil {
			continue
		}
		out = append(out, *normalized)
	}
	if len(out) == 0 {
		out = nil
	}
	return out, includeNone, nil
}
