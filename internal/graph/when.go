package graph

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Eval evaluates a when expression against a scope. Missing paths compare
// as nil. An expression that matches nothing is false, not an error.
func Eval(expr string, scope map[string]any) (bool, error) {
	p := &parser{s: expr}
	n, err := p.parseOr()
	if err != nil {
		return false, err
	}
	p.skip()
	if p.i < len(p.s) {
		return false, fmt.Errorf("unexpected %q", p.s[p.i:])
	}
	v, err := n.eval(scope)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expression did not return a boolean")
	}
	return b, nil
}

// Parse checks when-expression syntax.
func Parse(expr string) error {
	_, err := Eval(expr, map[string]any{})
	if err != nil && strings.HasPrefix(err.Error(), "expression did not return") {
		return nil
	}
	// Eval of a comparison against empty scope returns false, which is fine.
	// Syntax errors still surface. Re-parse to avoid treating type errors
	// from empty data as syntax errors: comparisons against nil are false.
	return parseOnly(expr)
}

func parseOnly(expr string) error {
	p := &parser{s: expr}
	if _, err := p.parseOr(); err != nil {
		return err
	}
	p.skip()
	if p.i < len(p.s) {
		return fmt.Errorf("unexpected %q", p.s[p.i:])
	}
	return nil
}

type node interface {
	eval(map[string]any) (any, error)
}

type parser struct {
	s string
	i int
}

func (p *parser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n') {
		p.i++
	}
}

func (p *parser) peek(s string) bool {
	p.skip()
	return strings.HasPrefix(p.s[p.i:], s)
}

func (p *parser) take(s string) bool {
	if p.peek(s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.take("||") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &logicNode{op: "||", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for p.take("&&") {
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		left = &logicNode{op: "&&", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseCmp() (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	p.skip()
	ops := []string{"==", "!=", "<=", ">=", "<", ">"}
	for _, op := range ops {
		if p.take(op) {
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			return &cmpNode{op: op, left: left, right: right}, nil
		}
	}
	return left, nil
}

func (p *parser) parseUnary() (node, error) {
	if p.take("!") {
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &notNode{inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	p.skip()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	if p.take("(") {
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.take(")") {
			return nil, fmt.Errorf("missing )")
		}
		return n, nil
	}
	if p.s[p.i] == '"' {
		s, err := p.parseString()
		if err != nil {
			return nil, err
		}
		return litNode{v: s}, nil
	}
	if p.s[p.i] == '-' || unicode.IsDigit(rune(p.s[p.i])) {
		return p.parseNumber()
	}
	if isIdentStart(p.s[p.i]) {
		ident := p.parseIdent()
		switch ident {
		case "true":
			return litNode{v: true}, nil
		case "false":
			return litNode{v: false}, nil
		case "null":
			return litNode{v: nil}, nil
		}
		return pathNode{path: ident}, nil
	}
	return nil, fmt.Errorf("unexpected %q", p.s[p.i:])
}

func (p *parser) parseString() (string, error) {
	p.i++ // opening quote
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		if c == '"' {
			return b.String(), nil
		}
		if c == '\\' {
			if p.i >= len(p.s) {
				return "", fmt.Errorf("unterminated string escape")
			}
			esc := p.s[p.i]
			p.i++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteByte(esc)
			default:
				return "", fmt.Errorf("unknown string escape %q", esc)
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", fmt.Errorf("unterminated string")
}

func (p *parser) parseNumber() (node, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) || !unicode.IsDigit(rune(p.s[p.i])) {
		return nil, fmt.Errorf("invalid number")
	}
	for p.i < len(p.s) && unicode.IsDigit(rune(p.s[p.i])) {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if p.i >= len(p.s) || !unicode.IsDigit(rune(p.s[p.i])) {
			return nil, fmt.Errorf("invalid number")
		}
		for p.i < len(p.s) && unicode.IsDigit(rune(p.s[p.i])) {
			p.i++
		}
	}
	n, err := strconv.ParseFloat(p.s[start:p.i], 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q", p.s[start:p.i])
	}
	return litNode{v: n}, nil
}

func (p *parser) parseIdent() string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if isIdentStart(c) || unicode.IsDigit(rune(c)) || c == '-' {
			p.i++
			continue
		}
		if c == '.' {
			// dotted path; keep going if the next char starts a segment
			if p.i+1 < len(p.s) && (isIdentStart(p.s[p.i+1]) || unicode.IsDigit(rune(p.s[p.i+1]))) {
				p.i++
				continue
			}
		}
		break
	}
	return p.s[start:p.i]
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

type litNode struct{ v any }

func (n litNode) eval(map[string]any) (any, error) { return n.v, nil }

type pathNode struct{ path string }

func (n pathNode) eval(scope map[string]any) (any, error) {
	v, _ := lookup(scope, n.path)
	return v, nil
}

type notNode struct{ inner node }

func (n *notNode) eval(scope map[string]any) (any, error) {
	v, err := n.inner.eval(scope)
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("! expects a boolean")
	}
	return !b, nil
}

type logicNode struct {
	op          string
	left, right node
}

func (n *logicNode) eval(scope map[string]any) (any, error) {
	lv, err := n.left.eval(scope)
	if err != nil {
		return nil, err
	}
	lb, ok := lv.(bool)
	if !ok {
		return nil, fmt.Errorf("%s expects booleans", n.op)
	}
	if n.op == "&&" && !lb {
		return false, nil
	}
	if n.op == "||" && lb {
		return true, nil
	}
	rv, err := n.right.eval(scope)
	if err != nil {
		return nil, err
	}
	rb, ok := rv.(bool)
	if !ok {
		return nil, fmt.Errorf("%s expects booleans", n.op)
	}
	return rb, nil
}

type cmpNode struct {
	op          string
	left, right node
}

func (n *cmpNode) eval(scope map[string]any) (any, error) {
	lv, err := n.left.eval(scope)
	if err != nil {
		return nil, err
	}
	rv, err := n.right.eval(scope)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "==":
		return equal(lv, rv), nil
	case "!=":
		return !equal(lv, rv), nil
	case "<", "<=", ">", ">=":
		ln, lok := asFloat(lv)
		rn, rok := asFloat(rv)
		if !lok || !rok {
			return false, nil
		}
		switch n.op {
		case "<":
			return ln < rn, nil
		case "<=":
			return ln <= rn, nil
		case ">":
			return ln > rn, nil
		default:
			return ln >= rn, nil
		}
	default:
		return nil, fmt.Errorf("unknown operator %s", n.op)
	}
}

func equal(a, b any) bool {
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			return af == bf
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b) && sameKind(a, b)
}

func sameKind(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if _, ok := asFloat(a); ok {
		_, okb := asFloat(b)
		return okb
	}
	switch a.(type) {
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	default:
		return fmt.Sprintf("%T", a) == fmt.Sprintf("%T", b)
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

func lookup(scope map[string]any, path string) (any, bool) {
	cur := any(scope)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
