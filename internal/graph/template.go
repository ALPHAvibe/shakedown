package graph

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// Render replaces {{ dotted.path }} placeholders. Shell quoting is applied
// to each substitution so values cannot break out of a command string.
// Prompt rendering leaves values unquoted.
func Render(s string, scope map[string]any, shell bool) (string, error) {
	var err error
	out := placeholder.ReplaceAllStringFunc(s, func(match string) string {
		if err != nil {
			return match
		}
		sub := placeholder.FindStringSubmatch(match)
		val, ok := lookup(scope, strings.TrimSpace(sub[1]))
		if !ok {
			err = fmt.Errorf("unknown placeholder %s", strings.TrimSpace(sub[1]))
			return match
		}
		text := stringify(val)
		if shell {
			return shellQuote(text)
		}
		return text
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case Value:
		return t.String()
	default:
		if f, ok := asFloat(t); ok {
			if f == float64(int64(f)) {
				return strconv.FormatInt(int64(f), 10)
			}
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return fmt.Sprint(t)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Scope builds the template and when-expression scope for one execution.
func Scope(inputs map[string]Value, result map[string]any) map[string]any {
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v.Any()
	}
	scope := map[string]any{"inputs": in}
	for k, v := range result {
		scope[k] = v
	}
	return scope
}
