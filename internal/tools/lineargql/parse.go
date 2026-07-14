package lineargql

// countOperations counts top-level GraphQL operation definitions (query,
// mutation, subscription, or anonymous `{ ... }`), excluding fragment
// definitions. It enforces the single-operation rule (SPEC §10.5).
func countOperations(doc string) int {
	doc = stripGraphQL(doc)

	ops := 0
	depth := 0
	atDefStart := true
	i, n := 0, len(doc)
	for i < n {
		c := doc[i]
		switch {
		case c == '{':
			if depth == 0 && atDefStart {
				// Anonymous operation.
				ops++
				atDefStart = false
			}
			depth++
			i++
		case c == '}':
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				atDefStart = true
			}
			i++
		case depth == 0 && atDefStart && isLetter(c):
			word, adv := readWord(doc[i:])
			switch word {
			case "query", "mutation", "subscription":
				ops++
				atDefStart = false
			case "fragment":
				atDefStart = false
			}
			i += adv
		default:
			i++
		}
	}
	return ops
}

// stripGraphQL removes comments (# to EOL) and string literals so they don't
// interfere with operation counting.
func stripGraphQL(s string) string {
	var b []byte
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '#':
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '"':
			// Handle block strings (""") and normal strings.
			if i+2 < n && s[i+1] == '"' && s[i+2] == '"' {
				i += 3
				for i+2 < n && !(s[i] == '"' && s[i+1] == '"' && s[i+2] == '"') {
					i++
				}
				i += 3
			} else {
				i++
				for i < n && s[i] != '"' {
					if s[i] == '\\' && i+1 < n {
						i++
					}
					i++
				}
				i++
			}
			b = append(b, ' ')
		default:
			b = append(b, c)
			i++
		}
	}
	return string(b)
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func readWord(s string) (string, int) {
	i := 0
	for i < len(s) && (isLetter(s[i]) || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	return s[:i], i
}
