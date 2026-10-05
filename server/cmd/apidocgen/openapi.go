package main

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// spec 是 openapi.yaml 的节点视图。输出顺序跟文件里的 mapping 顺序走，不用 Go map 遍历。
type spec struct {
	root *yaml.Node
	err  error
}

func loadSpec(raw []byte) (*spec, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	root := &doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil, fmt.Errorf("empty openapi document")
		}
		root = doc.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("openapi root is not a mapping")
	}
	s := &spec{root: root}
	if child(root, "paths") == nil {
		return nil, fmt.Errorf("openapi missing paths")
	}
	return s, nil
}

func (s *spec) fail(format string, args ...any) {
	if s.err == nil {
		s.err = fmt.Errorf(format, args...)
	}
}

func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Kind == yaml.ScalarNode && n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node, key string) (string, bool) {
	v := child(n, key)
	if v == nil {
		return "", false
	}
	v = derefAlias(v)
	if v.Kind != yaml.ScalarNode {
		return "", false
	}
	return v.Value, true
}

func derefAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func seqScalars(n *yaml.Node) []string {
	n = derefAlias(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		c = derefAlias(c)
		if c != nil && c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
		}
	}
	return out
}

func (s *spec) lookup(ref string) *yaml.Node {
	if !strings.HasPrefix(ref, "#/") {
		s.fail("unsupported $ref %q", ref)
		return nil
	}
	cur := s.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		cur = derefAlias(cur)
		if cur == nil || cur.Kind != yaml.MappingNode {
			s.fail("broken $ref %s", ref)
			return nil
		}
		cur = child(cur, part)
		if cur == nil {
			s.fail("unknown $ref %s", ref)
			return nil
		}
	}
	return derefAlias(cur)
}

type schema struct {
	typ         string
	format      string
	description string
	enum        []string
	pattern     string
	def         string
	hasDefault  bool
	min         string
	hasMin      bool
	max         string
	hasMax      bool
	minLen      string
	hasMinLen   bool
	maxLen      string
	hasMaxLen   bool
	minItems    string
	hasMinItems bool
	maxItems    string
	hasMaxItems bool
	nullable    bool
	required    []string
	props       []prop
	items       *schema
}

type prop struct {
	name   string
	schema schema
}

func (s schema) hasProps() bool {
	return len(s.props) > 0
}

func (s schema) isArray() bool {
	return s.typ == "array" || s.items != nil
}

// resolve 展开 $ref 与 allOf。引用点上的 description、nullable 会叠到目标 schema 上，
// 不丢掉字段级说明。同一 $ref 在展开栈上再次出现时停住，避免循环。
func (s *spec) resolve(n *yaml.Node, stack map[string]bool) schema {
	n = derefAlias(n)
	if n == nil || s.err != nil {
		return schema{}
	}
	if n.Kind != yaml.MappingNode {
		s.fail("schema at line %d is not a mapping", n.Line)
		return schema{}
	}
	if child(n, "oneOf") != nil || child(n, "anyOf") != nil || child(n, "not") != nil {
		s.fail("unsupported schema composition at line %d", n.Line)
		return schema{}
	}

	var out schema
	if ref, ok := scalar(n, "$ref"); ok {
		if stack[ref] {
			out.description = "循环引用 " + ref
			s.applyLocal(n, &out, stack)
			return out
		}
		target := s.lookup(ref)
		if target == nil {
			return schema{}
		}
		stack[ref] = true
		out = s.resolve(target, stack)
		delete(stack, ref)
	}
	if all := child(n, "allOf"); all != nil {
		out = mergeSchema(out, s.mergeAllOf(all, stack))
	}
	s.applyLocal(n, &out, stack)
	return out
}

func (s *spec) mergeAllOf(seq *yaml.Node, stack map[string]bool) schema {
	seq = derefAlias(seq)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		s.fail("allOf is not a sequence")
		return schema{}
	}
	var acc schema
	for i, item := range seq.Content {
		part := s.resolve(item, stack)
		if i == 0 {
			acc = part
			continue
		}
		acc = mergeSchema(acc, part)
	}
	return acc
}

// mergeSchema 把后面的 allOf 分支叠到前面上。同名字段整体替换，用来表达
// Envelope 与 PaginatedData 上被兄弟 schema 收窄的 data / items。
func mergeSchema(base, over schema) schema {
	out := base
	if over.typ != "" {
		out.typ = over.typ
	}
	if over.format != "" {
		out.format = over.format
	}
	if over.description != "" {
		out.description = combineDesc(over.description, base.description)
	}
	if len(over.enum) > 0 {
		out.enum = append([]string(nil), over.enum...)
	}
	if over.pattern != "" {
		out.pattern = over.pattern
	}
	if over.hasDefault {
		out.hasDefault = true
		out.def = over.def
	}
	if over.hasMin {
		out.hasMin = true
		out.min = over.min
	}
	if over.hasMax {
		out.hasMax = true
		out.max = over.max
	}
	if over.hasMinLen {
		out.hasMinLen = true
		out.minLen = over.minLen
	}
	if over.hasMaxLen {
		out.hasMaxLen = true
		out.maxLen = over.maxLen
	}
	if over.hasMinItems {
		out.hasMinItems = true
		out.minItems = over.minItems
	}
	if over.hasMaxItems {
		out.hasMaxItems = true
		out.maxItems = over.maxItems
	}
	if over.nullable {
		out.nullable = true
	}
	if over.items != nil {
		cp := *over.items
		out.items = &cp
	}
	out.props = mergeProps(base.props, over.props)
	out.required = unionNames(base.required, over.required)
	return out
}

func mergeProps(base, over []prop) []prop {
	if len(over) == 0 {
		return base
	}
	out := append([]prop(nil), base...)
	index := make(map[string]int, len(out))
	for i, p := range out {
		index[p.name] = i
	}
	for _, p := range over {
		if i, ok := index[p.name]; ok {
			out[i] = p
			continue
		}
		index[p.name] = len(out)
		out = append(out, p)
	}
	return out
}

func unionNames(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, name := range base {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range extra {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (s *spec) applyLocal(n *yaml.Node, dst *schema, stack map[string]bool) {
	if t, ok := scalar(n, "type"); ok {
		dst.typ = t
	}
	if f, ok := scalar(n, "format"); ok {
		dst.format = f
	}
	if d, ok := scalar(n, "description"); ok {
		dst.description = combineDesc(d, dst.description)
	}
	if e := child(n, "enum"); e != nil {
		dst.enum = seqScalars(e)
	}
	if p, ok := scalar(n, "pattern"); ok {
		dst.pattern = p
	}
	if v := child(n, "default"); v != nil && derefAlias(v).Kind == yaml.ScalarNode {
		dst.hasDefault = true
		dst.def = derefAlias(v).Value
	}
	setBound(n, "minimum", &dst.hasMin, &dst.min)
	setBound(n, "maximum", &dst.hasMax, &dst.max)
	setBound(n, "minLength", &dst.hasMinLen, &dst.minLen)
	setBound(n, "maxLength", &dst.hasMaxLen, &dst.maxLen)
	setBound(n, "minItems", &dst.hasMinItems, &dst.minItems)
	setBound(n, "maxItems", &dst.hasMaxItems, &dst.maxItems)
	if b, ok := boolVal(n, "nullable"); ok && b {
		dst.nullable = true
	}
	if items := child(n, "items"); items != nil {
		it := s.resolve(items, stack)
		dst.items = &it
	}
	if props := child(n, "properties"); props != nil {
		dst.props = mergeProps(dst.props, s.parseProps(props, stack))
	}
	if req := child(n, "required"); req != nil {
		dst.required = unionNames(dst.required, seqScalars(req))
	}
}

func setBound(n *yaml.Node, key string, has *bool, dest *string) {
	v := child(n, key)
	v = derefAlias(v)
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	*has = true
	*dest = v.Value
}

func boolVal(n *yaml.Node, key string) (bool, bool) {
	v, ok := scalar(n, key)
	if !ok {
		return false, false
	}
	switch strings.ToLower(v) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (s *spec) parseProps(props *yaml.Node, stack map[string]bool) []prop {
	props = derefAlias(props)
	if props == nil || props.Kind != yaml.MappingNode {
		s.fail("properties is not a mapping")
		return nil
	}
	out := make([]prop, 0, len(props.Content)/2)
	for i := 0; i+1 < len(props.Content); i += 2 {
		key := derefAlias(props.Content[i])
		if key == nil || key.Kind != yaml.ScalarNode {
			continue
		}
		out = append(out, prop{name: key.Value, schema: s.resolve(props.Content[i+1], stack)})
	}
	return out
}

func combineDesc(primary, extra string) string {
	primary = cleanText(primary)
	extra = cleanText(extra)
	if primary == "" {
		return extra
	}
	if extra == "" || primary == extra || strings.Contains(primary, extra) {
		return primary
	}
	if strings.Contains(extra, primary) {
		return extra
	}
	if strings.HasSuffix(primary, "。") || strings.HasSuffix(primary, "；") || strings.HasSuffix(primary, ".") {
		return primary + extra
	}
	return primary + "。" + extra
}

func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func (s schema) typeLabel() string {
	if s.isArray() {
		itemAnn := ""
		inner := "any"
		if s.items != nil {
			if s.items.hasProps() || s.items.typ == "object" {
				inner = "object"
			} else if s.items.typ == "" {
				inner = "any"
			} else if s.items.format == "binary" {
				inner = "binary"
			} else {
				inner = s.items.typ
			}
			itemAnn = s.items.annotation(false)
		}
		ann := joinAnn(itemAnn, s.annotation(true))
		return inner + "[]" + ann
	}
	base := s.typ
	if base == "" {
		if s.hasProps() {
			base = "object"
		} else {
			base = "any"
		}
	}
	if s.format == "binary" {
		base = "binary"
	}
	return base + s.annotation(false)
}

func (s schema) annotation(asArray bool) string {
	var parts []string
	if !asArray && s.format != "" && s.format != "binary" {
		parts = append(parts, s.format)
	}
	if !asArray && len(s.enum) > 0 {
		parts = append(parts, "enum: "+formatEnum(s.enum))
	}
	if !asArray && s.pattern != "" {
		parts = append(parts, "pattern: "+s.pattern)
	}
	if !asArray {
		if label := boundLabel("长度 ", s.hasMinLen, s.minLen, s.hasMaxLen, s.maxLen, ""); label != "" {
			parts = append(parts, label)
		}
		if label := boundLabel("", s.hasMin, s.min, s.hasMax, s.max, ""); label != "" {
			parts = append(parts, label)
		}
	}
	if asArray {
		if label := boundLabel("", s.hasMinItems, s.minItems, s.hasMaxItems, s.maxItems, " 项"); label != "" {
			parts = append(parts, label)
		}
	}
	if !asArray && s.hasDefault {
		shown := s.def
		if shown == "" {
			shown = `""`
		}
		parts = append(parts, "默认 "+shown)
	}
	if s.nullable {
		parts = append(parts, "可空")
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "，") + "）"
}

func joinAnn(a, b string) string {
	a = strings.TrimSuffix(strings.TrimPrefix(a, "（"), "）")
	b = strings.TrimSuffix(strings.TrimPrefix(b, "（"), "）")
	switch {
	case a == "" && b == "":
		return ""
	case a == "":
		return "（" + b + "）"
	case b == "":
		return "（" + a + "）"
	default:
		return "（" + a + "，" + b + "）"
	}
}

func boundLabel(prefix string, hasMin bool, min string, hasMax bool, max string, suffix string) string {
	switch {
	case hasMin && hasMax:
		return prefix + min + ".." + max + suffix
	case hasMin:
		return prefix + "≥" + min + suffix
	case hasMax:
		return prefix + "≤" + max + suffix
	default:
		return ""
	}
}

func formatEnum(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		if v == "" || strings.ContainsAny(v, " |，,") {
			parts[i] = strconv.Quote(v)
		} else {
			parts[i] = v
		}
	}
	return strings.Join(parts, " | ")
}

// unwrapData 识别 allOf Envelope 之后的成功响应，只留下 data。
func unwrapData(s schema) (schema, bool) {
	var data *schema
	hasCode, hasMessage := false, false
	for i := range s.props {
		switch s.props[i].name {
		case "code":
			hasCode = true
		case "message":
			hasMessage = true
		case "data":
			cp := s.props[i].schema
			data = &cp
		}
	}
	if hasCode && hasMessage && data != nil {
		return *data, true
	}
	return s, false
}

func isNullData(s schema) bool {
	return s.nullable && !s.hasProps() && s.items == nil && (s.typ == "object" || s.typ == "")
}

type fieldRow struct {
	name string
	typ  string
	req  string
	desc string
}

func flatten(s schema, prefix string) []fieldRow {
	if s.isArray() && !s.hasProps() {
		if s.items != nil && s.items.hasProps() {
			return flatten(*s.items, prefix)
		}
		return nil
	}
	req := make(map[string]struct{}, len(s.required))
	for _, name := range s.required {
		req[name] = struct{}{}
	}
	var rows []fieldRow
	for _, p := range s.props {
		name := p.name
		if prefix != "" {
			name = prefix + "." + p.name
		}
		mark := "否"
		if _, ok := req[p.name]; ok {
			mark = "是"
		}
		rows = append(rows, fieldRow{
			name: name,
			typ:  p.schema.typeLabel(),
			req:  mark,
			desc: cleanText(p.schema.description),
		})
		switch {
		case p.schema.hasProps():
			rows = append(rows, flatten(p.schema, name)...)
		case p.schema.isArray() && p.schema.items != nil && p.schema.items.hasProps():
			rows = append(rows, flatten(*p.schema.items, name+"[]")...)
		}
	}
	return rows
}
