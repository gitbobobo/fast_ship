// Command apidocgen 把 server/api/openapi.yaml 转成技能参考文档。
// 在 server 目录执行：go run ./cmd/apidocgen
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	specPath = "api/openapi.yaml"
	outPath  = "../skills/fast-ship/references/api.md"
	banner   = "<!-- 由 server/cmd/apidocgen 从 server/api/openapi.yaml 生成，请勿手改 -->"
)

var methodOrder = []string{"get", "head", "post", "put", "patch", "delete", "options", "trace"}

func main() {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apidocgen: 读取 %s 失败（请在 server 目录运行）: %v\n", specPath, err)
		os.Exit(1)
	}
	doc, err := render(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apidocgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("../skills/fast-ship/references", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "apidocgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "apidocgen: %v\n", err)
		os.Exit(1)
	}
}

type opView struct {
	method   string
	path     string
	pathItem *yaml.Node
	node     *yaml.Node
}

type tagGroup struct {
	name string
	desc string
	ops  []opView
}

func render(raw []byte) (string, error) {
	sp, err := loadSpec(raw)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(banner)
	b.WriteString("\n\n# ")
	info := child(sp.root, "info")
	title := "Fast Ship API"
	if t, ok := scalar(info, "title"); ok && t != "" {
		title = t
	}
	b.WriteString(title)
	b.WriteString("\n\n")
	if desc, ok := scalar(info, "description"); ok {
		if text := cleanText(desc); text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	}
	b.WriteString("各端点小节只展开 `data` 的形状，不再重复 `code` 与 `message`。\n\n")

	ops := collectOps(sp)
	if sp.err != nil {
		return "", sp.err
	}
	for _, group := range groupByTag(sp, ops) {
		b.WriteString("## ")
		b.WriteString(group.name)
		b.WriteString("\n\n")
		if group.desc != "" {
			b.WriteString(group.desc)
			b.WriteString("\n\n")
		}
		for _, op := range group.ops {
			if primary := primaryTag(op); primary != group.name {
				// 多 tag 端点只在首个 tag 下展开，其余章节留交叉引用，避免同一小节重复出现。
				b.WriteString("### ")
				b.WriteString(strings.ToUpper(op.method))
				b.WriteString(" `")
				b.WriteString(op.path)
				b.WriteString("` —— 见 `")
				b.WriteString(primary)
				b.WriteString("` 章\n\n")
				continue
			}
			writeOp(&b, sp, op)
			if sp.err != nil {
				return "", sp.err
			}
		}
	}
	if sp.err != nil {
		return "", sp.err
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func collectOps(sp *spec) []opView {
	paths := child(sp.root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		sp.fail("paths is not a mapping")
		return nil
	}
	var ops []opView
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathNode := derefAlias(paths.Content[i])
		item := derefAlias(paths.Content[i+1])
		if pathNode == nil || pathNode.Kind != yaml.ScalarNode || item == nil {
			continue
		}
		present := map[string]*yaml.Node{}
		if item.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(item.Content); j += 2 {
				key := derefAlias(item.Content[j])
				if key != nil && key.Kind == yaml.ScalarNode {
					present[key.Value] = derefAlias(item.Content[j+1])
				}
			}
		}
		for _, method := range methodOrder {
			node := present[method]
			if node == nil {
				continue
			}
			ops = append(ops, opView{method: method, path: pathNode.Value, pathItem: item, node: node})
		}
	}
	return ops
}

// primaryTag 返回端点声明的第一个 tag；未分类端点归入「未分类」。
func primaryTag(op opView) string {
	if names := seqScalars(child(op.node, "tags")); len(names) > 0 {
		return names[0]
	}
	return "未分类"
}

func groupByTag(sp *spec, ops []opView) []tagGroup {
	var groups []tagGroup
	index := map[string]int{}
	if tags := child(sp.root, "tags"); tags != nil && tags.Kind == yaml.SequenceNode {
		for _, item := range tags.Content {
			item = derefAlias(item)
			name, ok := scalar(item, "name")
			if !ok || name == "" {
				continue
			}
			if _, exists := index[name]; exists {
				continue
			}
			desc, _ := scalar(item, "description")
			index[name] = len(groups)
			groups = append(groups, tagGroup{name: name, desc: cleanText(desc)})
		}
	}
	for _, op := range ops {
		names := seqScalars(child(op.node, "tags"))
		if len(names) == 0 {
			names = []string{"未分类"}
		}
		for _, name := range names {
			i, ok := index[name]
			if !ok {
				index[name] = len(groups)
				groups = append(groups, tagGroup{name: name})
				i = index[name]
			}
			groups[i].ops = append(groups[i].ops, op)
		}
	}
	out := groups[:0]
	for _, g := range groups {
		if len(g.ops) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func writeOp(b *strings.Builder, sp *spec, op opView) {
	b.WriteString("### ")
	b.WriteString(strings.ToUpper(op.method))
	b.WriteString(" `")
	b.WriteString(op.path)
	b.WriteString("`\n\n")
	if id, ok := scalar(op.node, "operationId"); ok && id != "" {
		b.WriteString("<!-- operationId: ")
		b.WriteString(id)
		b.WriteString(" -->\n\n")
	}
	summary, _ := scalar(op.node, "summary")
	summary = cleanText(summary)
	desc, _ := scalar(op.node, "description")
	desc = cleanText(desc)
	if summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if desc != "" && desc != summary {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}

	params := sp.params(op.pathItem, op.node)
	b.WriteString("**鉴权**：" + authLabel(op.node, params) + "\n\n")
	writeParams(b, "路径参数", filterParams(params, "path"))
	writeParams(b, "Query 参数", filterParams(params, "query"))
	writeParams(b, "Header 参数", filterParams(params, "header"))
	var other []parameter
	for _, p := range params {
		if p.in != "path" && p.in != "query" && p.in != "header" {
			other = append(other, p)
		}
	}
	writeParams(b, "其他参数", other)
	writeBody(b, sp, op.node)
	writeResponses(b, sp, op.node)
}

type parameter struct {
	name     string
	in       string
	required bool
	typ      string
	desc     string
}

func (sp *spec) params(pathItem, op *yaml.Node) []parameter {
	var nodes []*yaml.Node
	if p := child(pathItem, "parameters"); p != nil && p.Kind == yaml.SequenceNode {
		nodes = append(nodes, p.Content...)
	}
	if p := child(op, "parameters"); p != nil && p.Kind == yaml.SequenceNode {
		nodes = append(nodes, p.Content...)
	}
	var out []parameter
	index := map[string]int{}
	for _, n := range nodes {
		n = derefAlias(n)
		if ref, ok := scalar(n, "$ref"); ok {
			n = sp.lookup(ref)
		}
		if n == nil {
			continue
		}
		name, _ := scalar(n, "name")
		in, _ := scalar(n, "in")
		if name == "" {
			continue
		}
		key := in + "\x00" + name
		required, _ := boolVal(n, "required")
		sch := sp.resolve(child(n, "schema"), map[string]bool{})
		paramDesc, _ := scalar(n, "description")
		desc := combineDesc(paramDesc, sch.description)
		p := parameter{name: name, in: in, required: required, typ: sch.typeLabel(), desc: desc}
		if i, ok := index[key]; ok {
			out[i] = p
			continue
		}
		index[key] = len(out)
		out = append(out, p)
	}
	return out
}

func filterParams(params []parameter, in string) []parameter {
	var out []parameter
	for _, p := range params {
		if p.in == in {
			out = append(out, p)
		}
	}
	return out
}

func authLabel(op *yaml.Node, params []parameter) string {
	label := "公开"
	sec := child(op, "security")
	if sec != nil && sec.Kind == yaml.SequenceNode && len(sec.Content) > 0 {
		hasJWT, hasKey := false, false
		var other []string
		for _, req := range sec.Content {
			req = derefAlias(req)
			if req == nil || req.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(req.Content); i += 2 {
				key := derefAlias(req.Content[i])
				if key == nil || key.Kind != yaml.ScalarNode {
					continue
				}
				switch key.Value {
				case "jwtAuth":
					hasJWT = true
				case "apiKeyAuth":
					hasKey = true
				default:
					other = append(other, key.Value)
				}
			}
		}
		switch {
		case hasJWT && hasKey:
			label = "JWT+API Key"
		case hasJWT:
			label = "仅 JWT"
		case hasKey:
			label = "仅 API Key"
		case len(other) > 0:
			sort.Strings(other)
			other = uniqueStrings(other)
			label = strings.Join(other, "+")
		default:
			label = "公开"
		}
	}
	for _, p := range params {
		if p.in == "query" && p.name == "token" {
			return label + "，支持 ?token="
		}
	}
	return label
}

func writeParams(b *strings.Builder, title string, params []parameter) {
	if len(params) == 0 {
		return
	}
	b.WriteString("**")
	b.WriteString(title)
	b.WriteString("**\n\n")
	rows := make([][]string, len(params))
	for i, p := range params {
		req := "否"
		if p.required {
			req = "是"
		}
		rows[i] = []string{"`" + p.name + "`", p.typ, req, p.desc}
	}
	writeTable(b, []string{"名称", "类型", "必填", "说明"}, rows)
}

func writeBody(b *strings.Builder, sp *spec, op *yaml.Node) {
	body := child(op, "requestBody")
	if body == nil {
		return
	}
	body = derefAlias(body)
	if ref, ok := scalar(body, "$ref"); ok {
		body = sp.lookup(ref)
	}
	if body == nil {
		return
	}
	required, ok := boolVal(body, "required")
	if !ok {
		required = false
	}
	content := child(body, "content")
	types := contentTypes(content)
	if len(types) == 0 {
		return
	}
	b.WriteString("**请求体**\n\n")
	for _, media := range types {
		mediaNode := child(content, media)
		schNode := child(mediaNode, "schema")
		sch := sp.resolve(schNode, map[string]bool{})
		flag := "可选"
		if required {
			flag = "必填"
		}
		b.WriteString("`")
		b.WriteString(media)
		b.WriteString("`，")
		b.WriteString(flag)
		if media == "multipart/form-data" {
			b.WriteString("。表单字段，不是 JSON")
		}
		b.WriteString("。\n\n")
		writeSchema(b, sch, false)
	}
}

func writeResponses(b *strings.Builder, sp *spec, op *yaml.Node) {
	resp := child(op, "responses")
	if resp == nil || resp.Kind != yaml.MappingNode {
		return
	}
	type statusResp struct {
		code int
		text string
		node *yaml.Node
	}
	var all []statusResp
	for i := 0; i+1 < len(resp.Content); i += 2 {
		key := derefAlias(resp.Content[i])
		if key == nil || key.Kind != yaml.ScalarNode {
			continue
		}
		code, err := strconv.Atoi(key.Value)
		if err != nil {
			sp.fail("bad status %q", key.Value)
			return
		}
		node := derefAlias(resp.Content[i+1])
		if ref, ok := scalar(node, "$ref"); ok {
			node = sp.lookup(ref)
		}
		all = append(all, statusResp{code: code, text: key.Value, node: node})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].code < all[j].code })

	var okResp, errResp []statusResp
	for _, item := range all {
		if item.code >= 200 && item.code < 300 {
			okResp = append(okResp, item)
			continue
		}
		errResp = append(errResp, item)
	}
	if len(okResp) > 0 {
		b.WriteString("**成功响应**\n\n")
		for _, item := range okResp {
			writeSuccess(b, sp, item.text, item.node)
		}
	}
	if len(errResp) > 0 {
		b.WriteString("**错误**\n\n")
		rows := make([][]string, len(errResp))
		for i, item := range errResp {
			desc := ""
			if item.node != nil {
				desc, _ = scalar(item.node, "description")
			}
			rows[i] = []string{item.text, cleanText(desc)}
		}
		writeTable(b, []string{"HTTP", "说明"}, rows)
	}
}

func writeSuccess(b *strings.Builder, sp *spec, status string, node *yaml.Node) {
	desc := ""
	if node != nil {
		desc, _ = scalar(node, "description")
	}
	b.WriteString("**")
	b.WriteString(status)
	b.WriteString("**")
	if text := cleanText(desc); text != "" {
		b.WriteString(" ")
		b.WriteString(text)
	}
	b.WriteString("\n\n")
	content := child(node, "content")
	types := contentTypes(content)
	if len(types) == 0 {
		return
	}
	if jsonNode := child(content, "application/json"); jsonNode != nil {
		sch := sp.resolve(child(jsonNode, "schema"), map[string]bool{})
		data, enveloped := unwrapData(sch)
		if !enveloped {
			data = sch
		}
		writeSchema(b, data, true)
		return
	}
	if allBinary(sp, content, types) {
		b.WriteString("二进制内容（")
		b.WriteString(strings.Join(types, "、"))
		b.WriteString("），无 JSON 信封。\n\n")
		return
	}
	for _, media := range types {
		sch := sp.resolve(child(child(content, media), "schema"), map[string]bool{})
		b.WriteString("`")
		b.WriteString(media)
		b.WriteString("`：")
		b.WriteString(sch.typeLabel())
		b.WriteString("\n\n")
		if sch.description != "" {
			b.WriteString(cleanText(sch.description))
			b.WriteString("\n\n")
		}
	}
}

func allBinary(sp *spec, content *yaml.Node, types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, media := range types {
		sch := sp.resolve(child(child(content, media), "schema"), map[string]bool{})
		if sch.format == "binary" || strings.HasPrefix(media, "image/") || media == "application/octet-stream" {
			continue
		}
		return false
	}
	return true
}

func writeSchema(b *strings.Builder, sch schema, asData bool) {
	if isNullData(sch) {
		if asData {
			b.WriteString("`data` 为 null。\n\n")
		}
		if text := cleanText(sch.description); text != "" {
			b.WriteString(text)
			b.WriteString("\n\n")
		}
		return
	}
	rows := flatten(sch, "")
	array := sch.isArray() && !sch.hasProps()
	if asData {
		if array {
			b.WriteString("`data` 为数组。\n\n")
		} else if len(rows) > 0 {
			b.WriteString("`data`：\n\n")
		} else {
			b.WriteString("`data`：")
			b.WriteString(sch.typeLabel())
			b.WriteString("\n\n")
		}
	}
	if text := cleanText(sch.description); text != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	if len(rows) == 0 {
		return
	}
	table := make([][]string, len(rows))
	for i, row := range rows {
		table[i] = []string{"`" + row.name + "`", row.typ, row.req, row.desc}
	}
	writeTable(b, []string{"字段", "类型", "必填", "说明"}, table)
}

func contentTypes(content *yaml.Node) []string {
	content = derefAlias(content)
	if content == nil || content.Kind != yaml.MappingNode {
		return nil
	}
	types := make([]string, 0, len(content.Content)/2)
	for i := 0; i+1 < len(content.Content); i += 2 {
		key := derefAlias(content.Content[i])
		if key != nil && key.Kind == yaml.ScalarNode {
			types = append(types, key.Value)
		}
	}
	sort.SliceStable(types, func(i, j int) bool {
		ri, rj := contentRank(types[i]), contentRank(types[j])
		if ri != rj {
			return ri < rj
		}
		return types[i] < types[j]
	})
	return types
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	var prev string
	for i, s := range in {
		if i == 0 || s != prev {
			out = append(out, s)
			prev = s
		}
	}
	return out
}

func contentRank(media string) int {
	switch media {
	case "application/json":
		return 0
	case "multipart/form-data":
		return 1
	default:
		return 2
	}
}

func writeTable(b *strings.Builder, headers []string, rows [][]string) {
	b.WriteString("| ")
	b.WriteString(strings.Join(headers, " | "))
	b.WriteString(" |\n| ")
	seps := make([]string, len(headers))
	for i := range seps {
		seps[i] = "---"
	}
	b.WriteString(strings.Join(seps, " | "))
	b.WriteString(" |\n")
	for _, row := range rows {
		b.WriteString("| ")
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = escapeCell(c)
		}
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	b.WriteByte('\n')
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
