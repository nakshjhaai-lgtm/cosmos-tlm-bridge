// Package yamlite is a dependency-free YAML subset parser, sufficient for
// Kubernetes and Helm manifests.
//
// It deliberately supports only the subset Kubernetes uses — block mappings,
// block sequences, flow collections, quoted and plain scalars, comments, and
// literal/folded block scalars — so the validator can run inside an air-gapped
// container with no third-party modules and no network.
package yamlite

import (
	"fmt"
	"strconv"
	"strings"
)

// Node is a parsed YAML value: map[string]any, []any, string, int64, float64,
// bool, or nil.
type Node = any

// Document is one YAML document with its starting line number.
type Document struct {
	Value Node
	Line  int
}

type line struct {
	indent int
	text   string
	number int
	raw    string
	blank  bool
}

// Parse splits input into documents and parses each.
func Parse(input string) ([]Document, error) {
	rawLines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")

	var docs []Document
	var current []line
	docStart := 1

	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		p := &parser{lines: current}
		val, err := p.parseBlock(0)
		if err != nil {
			return err
		}
		if val != nil {
			docs = append(docs, Document{Value: val, Line: docStart})
		}
		current = nil
		return nil
	}

	for i, r := range rawLines {
		trimmed := strings.TrimSpace(r)
		if trimmed == "---" || trimmed == "..." {
			if err := flush(); err != nil {
				return nil, err
			}
			docStart = i + 2
			continue
		}
		// Strip comments that are not inside quotes.
		content := stripComment(r)
		if strings.TrimSpace(content) == "" {
			current = append(current, line{blank: true, number: i + 1, raw: r})
			continue
		}
		indent := countIndent(content)
		current = append(current, line{
			indent: indent,
			text:   strings.TrimSpace(content),
			number: i + 1,
			raw:    r,
		})
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return docs, nil
}

// ParseOne parses a single-document YAML string.
func ParseOne(input string) (Node, error) {
	docs, err := Parse(input)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	return docs[0].Value, nil
}

func countIndent(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 2
		default:
			return n
		}
	}
	return n
}

// stripComment removes a trailing # comment, respecting quotes.
func stripComment(s string) string {
	inSingle, inDouble := false, false
	for i, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
					return s[:i]
				}
			}
		}
	}
	return s
}

type parser struct {
	lines []line
	pos   int
}

func (p *parser) peek() *line {
	for i := p.pos; i < len(p.lines); i++ {
		if !p.lines[i].blank {
			if i != p.pos {
				p.pos = i
			}
			return &p.lines[p.pos]
		}
	}
	return nil
}

func (p *parser) next() *line {
	l := p.peek()
	if l != nil {
		p.pos++
	}
	return l
}

// parseBlock parses a block at the given minimum indentation.
func (p *parser) parseBlock(minIndent int) (Node, error) {
	l := p.peek()
	if l == nil {
		return nil, nil
	}
	if l.indent < minIndent {
		return nil, nil
	}
	if strings.HasPrefix(l.text, "- ") || l.text == "-" {
		return p.parseSequence(l.indent)
	}
	return p.parseMapping(l.indent)
}

func (p *parser) parseSequence(indent int) (Node, error) {
	out := []any{}
	for {
		l := p.peek()
		if l == nil || l.indent != indent {
			break
		}
		if !strings.HasPrefix(l.text, "- ") && l.text != "-" {
			break
		}
		itemIndent := l.indent + 2
		rest := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
		p.pos++

		if rest == "" {
			child, err := p.parseBlock(itemIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, child)
			continue
		}

		// "- key: value" starts a mapping whose first key sits at itemIndent.
		if k, v, ok := splitKV(rest); ok && !isFlow(rest) {
			p.pos--
			p.lines[p.pos] = line{
				indent: itemIndent, text: k + ": " + v, number: l.number, raw: l.raw,
			}
			m, err := p.parseMapping(itemIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
			continue
		}

		// "- [a, b]" / "- scalar"
		if isFlow(rest) {
			v, err := parseFlow(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", l.number, err)
			}
			out = append(out, v)
			continue
		}
		out = append(out, scalar(rest))
	}
	return out, nil
}

func (p *parser) parseMapping(indent int) (Node, error) {
	out := map[string]any{}
	for {
		l := p.peek()
		if l == nil || l.indent < indent {
			break
		}
		if l.indent > indent {
			// Unexpected deeper indentation; skip defensively.
			p.pos++
			continue
		}
		if strings.HasPrefix(l.text, "- ") {
			break
		}
		k, v, ok := splitKV(l.text)
		if !ok {
			p.pos++
			continue
		}
		key := unquote(k)
		p.pos++

		if v != "" {
			if v == "|" || v == "|-" || v == ">" || v == ">-" || v == "|+" {
				block := p.collectBlockScalar(indent, v)
				out[key] = block
				continue
			}
			if isFlow(v) {
				parsed, err := parseFlow(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", l.number, err)
				}
				out[key] = parsed
				continue
			}
			// An opening bracket with no closer is a structural error, not a
			// string that happens to start with "[".
			if strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") {
				return nil, fmt.Errorf(
					"line %d: unterminated flow collection %q", l.number, v)
			}
			out[key] = scalar(v)
			continue
		}

		// Value is a nested block (or empty).
		child, err := p.parseBlock(indent + 1)
		if err != nil {
			return nil, err
		}
		if child == nil {
			out[key] = nil
		} else {
			out[key] = child
		}
	}
	return out, nil
}

func (p *parser) collectBlockScalar(parentIndent int, style string) string {
	var parts []string
	blockIndent := -1
	for {
		l := p.peek()
		if l == nil || l.indent <= parentIndent {
			break
		}
		if blockIndent < 0 {
			blockIndent = l.indent
		}
		// A later line may be less indented than the first line of the block
		// while still being deeper than the parent (common in hand-written
		// YAML). Clamp instead of panicking on a negative repeat count.
		extra := l.indent - blockIndent
		if extra < 0 {
			extra = 0
		}
		parts = append(parts, strings.Repeat(" ", extra)+l.text)
		p.pos++
	}
	var sep string
	switch {
	case strings.HasPrefix(style, ">"):
		sep = " "
	default:
		sep = "\n"
	}
	s := strings.Join(parts, sep)
	if !strings.HasSuffix(style, "-") {
		s += "\n"
	}
	return s
}

func isFlow(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) ||
		(strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"))
}

// splitKV splits "key: value" honouring quotes and flow collections.
func splitKV(s string) (string, string, bool) {
	inSingle, inDouble := false, false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '[', '{':
			if !inSingle && !inDouble {
				depth++
			}
		case ']', '}':
			if !inSingle && !inDouble {
				depth--
			}
		case ':':
			if inSingle || inDouble || depth > 0 {
				continue
			}
			if i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' {
				// "a:b" is a scalar, not a mapping
				if i+1 < len(s) {
					continue
				}
			}
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			inner := s[1 : len(s)-1]
			if s[0] == '"' {
				if u, err := strconv.Unquote(`"` + inner + `"`); err == nil {
					return u
				}
			}
			return strings.ReplaceAll(inner, "''", "'")
		}
	}
	return s
}

func scalar(s string) any {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return unquote(s)
		}
	}
	switch strings.ToLower(s) {
	case "", "null", "~":
		return nil
	case "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// parseFlow parses an inline [..] or {...} collection.
func parseFlow(s string) (Node, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if !strings.HasSuffix(s, "]") {
			return nil, fmt.Errorf("unterminated flow sequence %q", s)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}, nil
		}
		items, err := splitFlow(inner)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(items))
		for _, it := range items {
			if isFlow(it) {
				v, err := parseFlow(it)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
				continue
			}
			if k, v, ok := splitKV(it); ok {
				// [a: b] style map entry
				out = append(out, map[string]any{unquote(k): scalar(v)})
				continue
			}
			out = append(out, scalar(it))
		}
		return out, nil
	}
	if strings.HasPrefix(s, "{") {
		if !strings.HasSuffix(s, "}") {
			return nil, fmt.Errorf("unterminated flow mapping %q", s)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		out := map[string]any{}
		if inner == "" {
			return out, nil
		}
		items, err := splitFlow(inner)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			k, v, ok := splitKV(it)
			if !ok {
				out[unquote(it)] = nil
				continue
			}
			if isFlow(v) {
				parsed, err := parseFlow(v)
				if err != nil {
					return nil, err
				}
				out[unquote(k)] = parsed
				continue
			}
			out[unquote(k)] = scalar(v)
		}
		return out, nil
	}
	return scalar(s), nil
}

func splitFlow(s string) ([]string, error) {
	var out []string
	inSingle, inDouble := false, false
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '[', '{':
			if !inSingle && !inDouble {
				depth++
			}
		case ']', '}':
			if !inSingle && !inDouble {
				depth--
			}
		case ',':
			if !inSingle && !inDouble && depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in %q", s)
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out, nil
}

// Get walks a parsed document by key path.
func Get(node Node, path ...string) (Node, bool) {
	cur := node
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// GetString returns a string at a key path.
func GetString(node Node, path ...string) (string, bool) {
	v, ok := Get(node, path...)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
