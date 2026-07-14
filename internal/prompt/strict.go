package prompt

import (
	"regexp"
	"strings"
)

// checkUnknownVariables enforces strict variable semantics (SPEC §5.4). The
// osteele/liquid engine renders undefined variables as empty, so we walk the
// template's variable references ourselves and fail when a top-level variable
// root is outside the known binding context (plus loop/assign-introduced
// locals and Liquid builtins).
func checkUnknownVariables(template string, bindings map[string]any) error {
	known := map[string]bool{
		"forloop":      true,
		"tablerowloop": true,
	}
	for k := range bindings {
		known[k] = true
	}

	// First pass: collect locals introduced by for/assign/capture/tablerow so
	// they are treated as known regardless of position in the template.
	for _, m := range forVarRe.FindAllStringSubmatch(template, -1) {
		known[m[1]] = true
	}
	for _, m := range assignVarRe.FindAllStringSubmatch(template, -1) {
		known[m[1]] = true
	}
	for _, m := range captureVarRe.FindAllStringSubmatch(template, -1) {
		known[m[1]] = true
	}
	for _, m := range tablerowVarRe.FindAllStringSubmatch(template, -1) {
		known[m[1]] = true
	}

	// Second pass: examine every expression block and validate variable roots.
	for _, block := range expressionBlocks(template) {
		for _, root := range variableRoots(block) {
			if !known[root] {
				return &Error{
					Code: CodeTemplateRenderError,
					Msg:  "unknown variable: " + root,
				}
			}
		}
	}
	return nil
}

var (
	forVarRe      = regexp.MustCompile(`\{%-?\s*for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\b`)
	assignVarRe   = regexp.MustCompile(`\{%-?\s*assign\s+([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	captureVarRe  = regexp.MustCompile(`\{%-?\s*capture\s+([A-Za-z_][A-Za-z0-9_]*)\s*-?%\}`)
	tablerowVarRe = regexp.MustCompile(`\{%-?\s*tablerow\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\b`)

	outputRe = regexp.MustCompile(`\{\{-?(.*?)-?\}\}`)
	tagRe    = regexp.MustCompile(`\{%-?(.*?)-?%\}`)

	identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// liquidKeywords are tag names, operators, and literals that must not be
// treated as variable references.
var liquidKeywords = map[string]bool{
	"if": true, "elsif": true, "else": true, "endif": true,
	"unless": true, "endunless": true,
	"for": true, "endfor": true, "in": true, "reversed": true,
	"limit": true, "offset": true, "range": true,
	"assign": true, "capture": true, "endcapture": true,
	"case": true, "when": true, "endcase": true,
	"tablerow": true, "endtablerow": true, "cols": true,
	"cycle": true, "increment": true, "decrement": true,
	"break": true, "continue": true,
	"comment": true, "endcomment": true, "raw": true, "endraw": true,
	"include": true, "render": true, "with": true, "as": true,
	"and": true, "or": true, "not": true, "contains": true,
	"true": true, "false": true, "nil": true, "null": true,
	"empty": true, "blank": true, "by": true,
}

// expressionBlocks returns the inner expression text of every output ({{ }})
// and control tag ({% %}) in the template.
func expressionBlocks(template string) []string {
	var blocks []string
	for _, m := range outputRe.FindAllStringSubmatch(template, -1) {
		blocks = append(blocks, m[1])
	}
	for _, m := range tagRe.FindAllStringSubmatch(template, -1) {
		expr := strings.TrimSpace(m[1])
		// Skip tags whose bodies are not variable expressions.
		if isNonExprTag(expr) {
			continue
		}
		blocks = append(blocks, expr)
	}
	return blocks
}

func isNonExprTag(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "endif", "endfor", "endunless", "endcase", "endcapture",
		"endtablerow", "comment", "endcomment", "raw", "endraw",
		"else", "break", "continue", "increment", "decrement":
		return true
	}
	return false
}

// variableRoots extracts the root identifiers that appear in variable position
// within an expression, skipping string literals, numbers, keywords, filter
// names, named filter arguments, and property accessors.
func variableRoots(expr string) []string {
	expr = stripStringLiterals(expr)

	// Drop the leading tag keyword (for/if/assign/etc.) so it isn't scanned.
	trimmed := strings.TrimSpace(expr)
	if fields := strings.Fields(trimmed); len(fields) > 0 && liquidKeywords[fields[0]] {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		// For `assign x = ...`, drop the target and `=`.
		if fields[0] == "assign" {
			if eq := strings.Index(trimmed, "="); eq >= 0 {
				trimmed = trimmed[eq+1:]
			}
		}
		// For `for x in ...` / `tablerow x in ...`, drop the loop var and `in`.
		if fields[0] == "for" || fields[0] == "tablerow" {
			if idx := regexp.MustCompile(`\bin\b`).FindStringIndex(trimmed); idx != nil {
				trimmed = trimmed[idx[1]:]
			}
		}
		if fields[0] == "capture" {
			// capture body isn't an expression here.
			return nil
		}
	}

	var roots []string
	// Split off filter pipeline; the segment before the first '|' is the value,
	// subsequent segments are `filter: args`.
	segments := strings.Split(trimmed, "|")
	// Value segment: collect leading identifiers.
	roots = append(roots, rootsInValue(segments[0])...)
	// Filter segments: skip the filter name, scan argument identifiers.
	for _, seg := range segments[1:] {
		seg = strings.TrimSpace(seg)
		if colon := strings.Index(seg, ":"); colon >= 0 {
			args := seg[colon+1:]
			roots = append(roots, rootsInValue(args)...)
		}
	}
	return roots
}

// rootsInValue returns root identifiers used as values in a fragment, ignoring
// property accessors (the part after a dot) and numeric literals.
func rootsInValue(fragment string) []string {
	var roots []string
	locs := identRe.FindAllStringIndex(fragment, -1)
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		ident := fragment[start:end]
		if liquidKeywords[ident] {
			continue
		}
		// Skip property accessors: preceded by '.'.
		if start > 0 {
			prev := lastNonSpace(fragment[:start])
			if prev == '.' {
				continue
			}
		}
		// Skip named-argument keys: `key:` in filter args.
		if next := firstNonSpace(fragment[end:]); next == ':' {
			continue
		}
		roots = append(roots, ident)
	}
	return roots
}

func lastNonSpace(s string) byte {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != ' ' && s[i] != '\t' {
			return s[i]
		}
	}
	return 0
}

func firstNonSpace(s string) byte {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return s[i]
		}
	}
	return 0
}

// stripStringLiterals replaces single/double quoted string contents with spaces
// so identifiers inside literals are not mistaken for variables.
func stripStringLiterals(s string) string {
	var b strings.Builder
	var quote byte
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == quote {
				inStr = false
			}
			b.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' {
			inStr = true
			quote = c
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
