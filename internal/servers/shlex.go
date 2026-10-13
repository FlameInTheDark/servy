package servers

import "strings"

// splitCommand is a quote-aware shell-like tokenizer (equivalent to Python.s
// shlex.split). Supports "..." '...' and
// backslash escapes; empty results are dropped.
func splitCommand(s string) []string {
	var parts []string
	var cur strings.Builder
	inSingle, inDouble, hasToken := false, false, false
	flush := func() {
		if hasToken || cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			hasToken = false
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else if r == '\\' && i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
			hasToken = true
		case r == '"':
			inDouble = true
			hasToken = true
		case r == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			hasToken = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	flush()
	return parts
}
