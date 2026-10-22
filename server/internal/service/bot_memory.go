package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// 机器人记忆服务（INT-102）：对模型是文件式接口（USER.md / MEMORY.md /
// daily/日期.md），底层存数据库。本文件负责路径白名单、注入正文与归档去重；
// 工具分发在 bot_tools_memory.go，检索走 repository.SearchFTS（FTS5 trigram）。

const (
	botMemoryPathUser   = "USER.md"
	botMemoryPathMemory = "MEMORY.md"
	botMemoryDailyPref  = "daily/"
)

var botDailyPathRe = regexp.MustCompile(`^daily/(\d{4})-(\d{2})-(\d{2})\.md$`)

// botValidateMemoryPath 白名单校验记忆路径：只允许 USER.md、MEMORY.md 与
// daily/YYYY-MM-DD.md（日期真实有效）。拒绝绝对路径、反斜杠、空段、.、..、
// 控制字符与其他任何形态——不清洗后接受。
func botValidateMemoryPath(path string) error {
	if path == "" {
		return errs.New(errs.ErrInvalidParams.Code, "path 必填")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return errs.New(errs.ErrInvalidParams.Code, "path 含控制字符")
		}
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return errs.New(errs.ErrInvalidParams.Code, "path 非法形态")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errs.New(errs.ErrInvalidParams.Code, "path 含非法段")
		}
	}
	if path == botMemoryPathUser || path == botMemoryPathMemory {
		return nil
	}
	if m := botDailyPathRe.FindStringSubmatch(path); m != nil {
		y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			if t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC); t.Year() == y && int(t.Month()) == mo && t.Day() == d {
				return nil
			}
		}
		return errs.New(errs.ErrInvalidParams.Code, "daily 路径日期无效")
	}
	return errs.New(errs.ErrInvalidParams.Code,
		"path 不在记忆白名单内（允许 USER.md、MEMORY.md、daily/YYYY-MM-DD.md）")
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// botMemoryPathLimit 返回某路径的注入字符上限；daily 文件不常驻注入（上限 0）。
func (s *BotService) botMemoryPathLimit(path string) int64 {
	switch path {
	case botMemoryPathUser:
		return s.cfg.Bots.Memory.UserMaxCharsV()
	case botMemoryPathMemory:
		return s.cfg.Bots.Memory.MemoryMaxCharsV()
	}
	return 0
}

// botDailyPathForBot 返回某时刻的 daily 文件路径：日期按机器人归属用户的
// 有效时区计算（INT-138；用户未声明时区时经 botOwnerTimezoneOf 回退部署
// 配置，与旧口径一致）。只对之后的写入生效，不回填/重命名已有 daily 文件。
func (s *BotService) botDailyPathForBot(bot *model.Bot, t time.Time) string {
	_, loc := s.botOwnerTimezoneOf(bot)
	return botDailyPathAt(t, loc)
}

func botDailyPathAt(t time.Time, loc *time.Location) string {
	local := t.In(loc)
	return fmt.Sprintf("daily/%04d-%02d-%02d.md", local.Year(), int(local.Month()), local.Day())
}

// ---------- 冻结注入快照 ----------

const botMemoryTruncateMark = "[注入已截断；文件仍完整保存。请用 fs_memory 读取并整理。]"

// botMemoryInjectionTx 在一个事务内取常驻文件的一致版本并渲染注入正文。
// USER.md 与 MEMORY.md 按序注入，各自有字符上限；超限只截断注入副本并附
// 标记（标记计入上限），原文与历史不截断。
func (s *BotService) botMemoryInjectionTx(tx *gorm.DB, botID string) (string, map[string]int64, error) {
	files, err := repository.NewBotMemoryRepository(tx).ListResidentTx(tx, botID)
	if err != nil {
		return "", nil, err
	}
	if len(files) == 0 {
		// 没有任何常驻记忆文件时不注入快照段：无记忆机器人保持 INT-101
		// 的前缀形态（系统提示之后直接是摘要/历史），不多一条空段。
		return "", map[string]int64{}, nil
	}
	versions := map[string]int64{}
	var b strings.Builder
	for _, path := range []string{botMemoryPathUser, botMemoryPathMemory} {
		f := files[path]
		b.WriteString("## ")
		b.WriteString(path)
		b.WriteString("\n")
		if f == nil {
			b.WriteString("(空)\n")
			continue
		}
		versions[path] = f.CurrentVersion
		limit := s.botMemoryPathLimit(path)
		content := f.Content
		if limit > 0 && int64(utf8.RuneCountInString(content)) > limit {
			content = botTruncateInjection(content, limit)
		}
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), versions, nil
}

// botTruncateInjection 按 Unicode 字符截断注入副本；截断标记计入上限，
// 上限比标记还短时截短标记本身，避免标记把上限撑爆。
func botTruncateInjection(content string, limit int64) string {
	runes := []rune(content)
	mark := []rune(botMemoryTruncateMark)
	if limit <= 0 {
		return ""
	}
	if int64(len(mark)) >= limit {
		return string(mark[:int(limit)])
	}
	keep := int(limit) - len(mark)
	if keep > len(runes) {
		keep = len(runes)
	}
	return string(runes[:keep]) + string(mark)
}

// ---------- 归档去重 ----------

// botNormalizeParagraph 归一段落：去首尾空白、去掉 Markdown 条目符号
// （daily 文件行普遍带 "- " 前缀），折叠内部全部空白为单空格。
// 哈希相同即视为重复段。
func botNormalizeParagraph(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	return strings.Join(strings.Fields(s), " ")
}

func botParagraphHash(s string) string {
	sum := sha256.Sum256([]byte(botNormalizeParagraph(s)))
	return hex.EncodeToString(sum[:])
}

// botShingles 取文本的 k 字符（rune）shingle 集合。
func botShingles(s string, k int) map[string]struct{} {
	runes := []rune(botNormalizeParagraph(s))
	out := map[string]struct{}{}
	if len(runes) < k {
		if len(runes) > 0 {
			out[string(runes)] = struct{}{}
		}
		return out
	}
	for i := 0; i+k <= len(runes); i++ {
		out[string(runes[i:i+k])] = struct{}{}
	}
	return out
}

// botShingleContainment 返回 shorter 的 shingle 被 longer 覆盖的比例。
func botShingleContainment(shorter, longer string) float64 {
	a := botShingles(shorter, 5)
	if len(a) == 0 {
		return 1
	}
	b := botShingles(longer, 5)
	hit := 0
	for k := range a {
		if _, ok := b[k]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(a))
}

// botShingleJaccard 两段文本 5 字符 shingle 的 Jaccard 相似度。
func botShingleJaccard(a, b string) float64 {
	sa, sb := botShingles(a, 5), botShingles(b, 5)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	inter := 0
	for k := range sa {
		if _, ok := sb[k]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

// botFilterArchiveItems 过滤归档条目：与目标文件既有段落及本批已接受条目
// 去重。规则：归一化哈希相同拒；≥128 字符大段标准化包含覆盖较短段 90% 或
// 5 字符 shingle Jaccard ≥0.9 拒。
func botFilterArchiveItems(existingBody string, items []string) []string {
	paragraphs := []string{}
	for _, p := range strings.Split(existingBody, "\n") {
		if botNormalizeParagraph(p) != "" {
			paragraphs = append(paragraphs, p)
		}
	}
	seenHash := map[string]bool{}
	for _, p := range paragraphs {
		seenHash[botParagraphHash(p)] = true
	}
	accepted := []string{}
	for _, item := range items {
		norm := botNormalizeParagraph(item)
		if norm == "" {
			continue
		}
		h := botParagraphHash(item)
		if seenHash[h] {
			continue
		}
		dup := false
		if utf8.RuneCountInString(norm) >= 128 {
			for _, p := range append(append([]string{}, paragraphs...), accepted...) {
				pn := botNormalizeParagraph(p)
				shorter, longer := norm, pn
				if utf8.RuneCountInString(shorter) > utf8.RuneCountInString(longer) {
					shorter, longer = longer, shorter
				}
				if strings.Contains(longer, shorter) ||
					botShingleContainment(shorter, longer) >= 0.9 ||
					botShingleJaccard(norm, pn) >= 0.9 {
					dup = true
					break
				}
			}
		}
		if dup {
			continue
		}
		seenHash[h] = true
		accepted = append(accepted, item)
	}
	return accepted
}
