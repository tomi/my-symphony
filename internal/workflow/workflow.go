package workflow

import (
	"bufio"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is a parsed WORKFLOW.md payload (SPEC §4.1.2).
type Definition struct {
	// Config is the YAML front-matter root object, NOT nested under a "config"
	// key (SPEC §5.2).
	Config map[string]any
	// PromptTemplate is the trimmed Markdown body after the front matter (SPEC §5.2).
	PromptTemplate string
}

// Load reads and parses a WORKFLOW.md file (SPEC §5.2).
func Load(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Code: CodeMissingWorkflowFile, Msg: path, Wrapped: err}
	}
	return Parse(string(data))
}

// Parse splits front matter from body and decodes the front matter (SPEC §5.2).
func Parse(content string) (*Definition, error) {
	front, body, hasFront := splitFrontMatter(content)

	def := &Definition{Config: map[string]any{}}
	if hasFront {
		var raw any
		if err := yaml.Unmarshal([]byte(front), &raw); err != nil {
			return nil, &Error{Code: CodeWorkflowParseError, Msg: err.Error(), Wrapped: err}
		}
		if raw != nil {
			m, ok := normalizeMap(raw)
			if !ok {
				return nil, &Error{Code: CodeFrontMatterNotAMap, Msg: "front matter did not decode to a map"}
			}
			def.Config = m
		}
	}
	def.PromptTemplate = strings.TrimSpace(body)
	return def, nil
}

// splitFrontMatter detects a leading `---` fence and scans to the next `---`
// line. Returns (frontMatter, body, hasFrontMatter) (SPEC §5.2).
func splitFrontMatter(content string) (string, string, bool) {
	// Strip a leading UTF-8 BOM if present so the fence check still matches.
	content = strings.TrimPrefix(content, "\ufeff")

	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	if !sc.Scan() {
		return "", content, false
	}
	first := sc.Text()
	if strings.TrimRight(first, " \t\r") != "---" {
		return "", content, false
	}

	var front strings.Builder
	var body strings.Builder
	inFront := true
	for sc.Scan() {
		line := sc.Text()
		if inFront && strings.TrimRight(line, " \t\r") == "---" {
			inFront = false
			continue
		}
		if inFront {
			front.WriteString(line)
			front.WriteByte('\n')
		} else {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if inFront {
		// Opening fence with no closing fence: treat whole remainder as front
		// matter body-less; there is no prompt body.
		return front.String(), "", true
	}
	return front.String(), body.String(), true
}

// normalizeMap coerces a decoded YAML value into a map[string]any, recursively
// converting nested maps so downstream typed access is uniform.
func normalizeMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	return normalizeValue(m).(map[string]any), true
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeValue(val)
		}
		return out
	default:
		return v
	}
}
