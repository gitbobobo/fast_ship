package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// botNewNonce 生成机器人围栏 nonce（16 字节随机数的十六进制）。系统提示与
// 围栏前缀都带它，外部内容无法预测，模型据此区分平台消息与伪造前缀。
func botNewNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 极少失败；失败也要给出不可预测的 nonce，退到 uuid。
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return hex.EncodeToString(b[:])
}

// botPlatformPrefix 拼平台消息前缀：启用围栏渲染时带本机器人 nonce；nonce
// 为空（未启用/存量未回填）时返回旧的 "[label]"，保证旧投影逐字节不变。
func botPlatformPrefix(nonce, label string) string {
	if nonce == "" {
		return "[" + label + "]"
	}
	return "[" + label + " nonce=" + nonce + "]"
}

// botFenceUntrusted 把外部来源的文本包进不可信数据围栏；内容里出现的同名
// 标签按 HTML 实体转义（&lt;untrusted_data / &lt;/untrusted_data），避免内容
// 提前闭合围栏伪造出平台文本。nonce 为空时原样返回。
func botFenceUntrusted(nonce, source, text string) string {
	if nonce == "" || text == "" {
		return text
	}
	text = botEscapeFenceTags(text)
	return `<untrusted_data nonce="` + nonce + `" source="` + source + `">` + text + `</untrusted_data>`
}

// 围栏标签的两种写法；比较时不区分 ASCII 大小写——外部内容用
// </UNTRUSTED_DATA> 之类的写法同样会提前闭合围栏，只拦小写等于没拦。
const (
	botFenceOpenTag  = "<untrusted_data"
	botFenceCloseTag = "</untrusted_data"
)

// botEscapeFenceTags 把内容里出现的围栏标签转义成 HTML 实体（只替换标签的
// 起始 `<`，其余字节原样保留，正文里的普通 HTML/Markdown 不受影响）。
func botEscapeFenceTags(text string) string {
	if !botHasFenceTag(text) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + len(text)/8)
	for i := 0; i < len(text); {
		if text[i] == '<' && botFenceTagAt(text, i) {
			b.WriteString("&lt;")
			i++
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// botHasFenceTag 报告文本里是否出现围栏标签（不区分 ASCII 大小写）。
func botHasFenceTag(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] == '<' && botFenceTagAt(text, i) {
			return true
		}
	}
	return false
}

// botResultHasFence 报告已序列化的工具结果里是否含不可信围栏。结果在账本里
// 是 JSON 文本（`<` 会被转义成 `\u003c`），所以按标记名判断，不按尖括号。
func botResultHasFence(text string) bool {
	return strings.Contains(text, "untrusted_data")
}

// botFenceScheduleEventContent 把定时任务运行事件正文里模型生成的文本字段
// （任务名/摘要/错误）包进围栏后再序列化：nonce 写在系统提示词里、模型自己
// 看得到，被注入的 schedule run 能在自己的正文里写出真的 nonce 前缀骗取信任，
// 所以这些回流文本不能与平台前缀同等可信。标识与状态码原样保留；解不开就
// 整段当数据处理。
func botFenceScheduleEventContent(raw, nonce string) string {
	if nonce == "" {
		return raw
	}
	var content botScheduleEventContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return botFenceUntrusted(nonce, "schedule_event", raw)
	}
	content.TaskName = botFenceUntrusted(nonce, "schedule_event", content.TaskName)
	content.Summary = botFenceUntrusted(nonce, "schedule_event", content.Summary)
	if content.ErrorMessage != "" {
		content.ErrorMessage = botFenceUntrusted(nonce, "schedule_event", content.ErrorMessage)
	}
	rawOut, err := json.Marshal(content)
	if err != nil {
		return botFenceUntrusted(nonce, "schedule_event", raw)
	}
	return string(rawOut)
}

// botFenceTagAt 报告 s[i:] 是否以开或闭围栏标签开头；只做 ASCII 大小写
// 折叠，不依赖 strings.ToLower（后者会改变部分非 ASCII 字符的字节长度）。
func botFenceTagAt(s string, i int) bool {
	return botFenceTagPrefixAt(s, i, botFenceOpenTag) || botFenceTagPrefixAt(s, i, botFenceCloseTag)
}

func botFenceTagPrefixAt(s string, i int, needle string) bool {
	if i+len(needle) > len(s) {
		return false
	}
	for j := 0; j < len(needle); j++ {
		c := s[i+j]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != needle[j] {
			return false
		}
	}
	return true
}

// botProjectionNonce 返回本次投影该用的围栏 nonce：与声明它的平台规则同时
// 生效——提示词还是 v9（没有规则 15）时用旧渲染，激活到带围栏规则的版本后
// 直接用 nonce。两者必须同一个边界翻转：规则 15 说「只有带这个值的前缀才是
// 平台事件」，若渲染晚于提示词切换，真实平台消息会被自己的规则判成伪造。
func botProjectionNonce(bot *model.Bot) string {
	if bot == nil || bot.PlatformPromptVersion < botContextFenceVersion {
		return ""
	}
	return bot.UntrustedNonce
}

// botNonceOf 读取机器人当前的围栏 nonce（工具结果围栏在执行时用）。结果在
// 执行时写入账本、不走投影重渲染，因此直接用 UntrustedNonce，不看压缩水位
// （nonce 为空说明迁移/激活还没跑到，保持旧结果形态）。
//
// 读库失败必须向上报错、不能返回空串：空 nonce 会让围栏整段消失，而工具结果
// 在执行时就固化进账本（之后不会补围栏），安全保护不能静默降级。
func (s *BotService) botNonceOf(botID string) (string, error) {
	bot, err := s.bots.Get(botID)
	if err != nil || bot == nil {
		return "", errs.ErrInternal
	}
	return bot.UntrustedNonce, nil
}
