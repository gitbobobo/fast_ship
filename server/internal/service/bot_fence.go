package service

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
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

// botEscapeFenceTags 把内容里出现的围栏标签转义成 HTML 实体（只替换标签的
// 起始 `<`，其余字节原样保留，正文里的普通 HTML/Markdown 不受影响）。
// 载荷大多是 json.Marshal 产物：正文里的 < 早已是 \u003c 形态，模型按 JSON
// 转义还原后等于围栏内留了未转义的闭合标签——所以字面的 < 与 \u003c、
// 斜杠的 / 与 \u002f、乃至标签名里逐字符的 \u00XX 写法都要识别。
func botEscapeFenceTags(text string) string {
	var b strings.Builder
	escaped := false
	for i := 0; i < len(text); {
		if n := botFenceTagOpenLenAt(text, i); n > 0 {
			b.WriteString("&lt;")
			i += n
			escaped = true
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	if !escaped {
		return text
	}
	return b.String()
}

// botResultFenceExemptTools 是不进围栏的工具动作：只有平台完全自有的元数据/回执
// 才豁免（记忆的路径与版本列表、帮助、停止/取消回执、state_set 的 ack）。
// 带正文的读取一律走围栏：记忆内容与检索片段可能是模型抄进来的外部文本，
// 续读结果也由出口统一包。键是 tool/action；新工具/动作默认进围栏。
var botResultFenceExemptTools = map[string]bool{
	"fs_memory/status": true, "fs_memory/list": true, "fs_memory/history": true,
	"fs_memory/create": true, "fs_memory/replace": true, "fs_memory/append": true,
	"fs_memory/delete": true, "fs_memory/restore": true,
	"fs_help/read":          true,
	"fs_stop/stop":          true,
	"fs_cancel/cancel":      true,
	"fs_schedule/state_set": true,
}

// botFenceToolResult 把**整条**工具结果包进一层不可信围栏（INT-136 重构）：
// 判定放在模型输入的出口，默认拒绝——新增工具、新增字段、嵌套对象、远端错误
// 文本都自动落在围栏内，不需要在每处投影里逐字段列清单（那样是 fail-open：
// 漏列就是裸文本）。调用方的投影仍然负责瘦身与脱敏，两者互补。
// 平台自有的控制性回执（记忆、帮助、停止/取消、任务小状态）按
// botResultFenceExemptTools 豁免；payload 里的围栏标签先转义，无法提前闭合。
func botFenceToolResult(nonce, toolName, action, payload string) string {
	if nonce == "" || payload == "" || botResultFenceExemptTools[toolName+"/"+action] {
		return payload
	}
	// 工具名来自模型（未知工具也会走到这里），进围栏属性前先收紧成安全字符，
	// 否则一个带引号的伪造工具名可以在 opener 里注入额外属性。
	return botFenceUntrusted(nonce, "tool_result:"+botFenceSourceToken(toolName), payload)
}

// botFenceSourceToken 把模型可控的名字收敛成围栏属性安全的形式：只留
// [a-z0-9_.-]，小写、限长；空则回退 "unknown"。
func botFenceSourceToken(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// botFenceTagName 是围栏标签名；匹配时不区分 ASCII 大小写——外部内容用
// </UNTRUSTED_DATA> 之类的写法同样会提前闭合围栏，只拦小写等于没拦。
const botFenceTagName = "untrusted_data"

// botFenceTagOpenLenAt 报告 s[i:] 是否以开或闭围栏标签开头，是则返回起始
// `<` 等价物占的字节数（字面 < 为 1，\u003c 为 6），否则 0。标签名允许
// 逐字符写成 \u00XX 转义——只按字面匹配等于给绕过留了编码通道。
func botFenceTagOpenLenAt(s string, i int) int {
	open := 0
	switch {
	case i < len(s) && s[i] == '<':
		open = 1
	case botFoldPrefixAt(s, i, `\u003c`):
		open = len(`\u003c`)
	default:
		return 0
	}
	j := i + open
	if j < len(s) && s[j] == '/' {
		j++
	} else if botFoldPrefixAt(s, j, `\u002f`) {
		j += len(`\u002f`)
	}
	if !botFenceTagNameAt(s, j) {
		return 0
	}
	return open
}

// botFenceTagNameAt 报告 s[i:] 是否以围栏标签名开头；每个字符取字面
// （ASCII 大小写折叠）或其 \u00XX 转义两种形态之一。
func botFenceTagNameAt(s string, i int) bool {
	for k := 0; k < len(botFenceTagName); k++ {
		want := botFenceTagName[k]
		if i < len(s) && s[i] == want {
			i++
			continue
		}
		if i < len(s) && s[i] >= 'A' && s[i] <= 'Z' && s[i]+'a'-'A' == want {
			i++
			continue
		}
		if botFoldPrefixAt(s, i, `\u00`) && i+6 <= len(s) && botUnhexByte(s[i+4], s[i+5]) == want {
			i += 6
			continue
		}
		return false
	}
	return true
}

// botUnhexByte 把两个 hex 字符解码成字节；非法字符返回 0xFF（必不等于任何
// 目标字符，调用处按不匹配处理）。
func botUnhexByte(a, b byte) byte {
	hi, ok1 := botHexVal(a)
	lo, ok2 := botHexVal(b)
	if !ok1 || !ok2 {
		return 0xFF
	}
	return hi<<4 | lo
}

func botHexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// botFoldPrefixAt 报告 s[i:] 是否以 needle 开头；只做 ASCII 大小写折叠，
// 不依赖 strings.ToLower（后者会改变部分非 ASCII 字符的字节长度）。
func botFoldPrefixAt(s string, i int, needle string) bool {
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
