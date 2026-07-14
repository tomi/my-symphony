package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// subMap returns the nested map for a top-level key, or an empty map.
func subMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if sm, ok := v.(map[string]any); ok {
			return sm
		}
	}
	return map[string]any{}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// asStringList coerces a YAML list into []string, trimming empty entries kept as-is.
func asStringList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func defaultStringList(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}

// asIntDefault coerces a numeric value (YAML int/float or numeric string),
// returning def when absent or uncoercible.
func asIntDefault(v any, def int) int {
	if n, ok := asIntStrict(v, def); ok {
		return n
	}
	return def
}

// asIntStrict returns (value, ok). ok is false when v is present but not a
// coercible integer; absent values return (def, true).
func asIntStrict(v any, def int) (int, bool) {
	switch t := v.(type) {
	case nil:
		return def, true
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
		return 0, false
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return def, true
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func asBoolDefault(v any, def bool) bool {
	switch t := v.(type) {
	case nil:
		return def
	case bool:
		return t
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return def
		}
		return b
	default:
		return def
	}
}

// normalizeStateMap lowercases keys and keeps only positive integer values
// (SPEC §5.3.5).
func normalizeStateMap(v any) map[string]int {
	out := map[string]int{}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for k, val := range m {
		n, ok := asIntStrict(val, 0)
		if !ok || n <= 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = n
	}
	return out
}

// resolveEnv resolves a $VAR_NAME reference via the environment. A value that
// is not a single $VAR reference is returned verbatim. An empty resolution is
// treated as missing by callers (SPEC §5.3.1, §6.1).
func resolveEnv(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "$") && len(v) > 1 && isEnvName(v[1:]) {
		return os.Getenv(v[1:])
	}
	return v
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// expandPath applies ~ home expansion and $VAR expansion for filesystem-path
// values only (SPEC §6.1). It must never be applied to URIs or shell commands.
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	} else if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	// Expand $VAR and ${VAR} references from the environment.
	p = os.Expand(p, func(name string) string { return os.Getenv(name) })
	return p
}
