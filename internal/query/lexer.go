// Package query parses strata's filter language and compiles it to SQL, so
// every filter runs inside DuckDB rather than in application memory.
//
//	label.team = "payments" and age > 180d
//	ancestor("reg.io/base/go:1.21") and not has:sbom
//	stale in ["stale", "abandoned"] and label.env = "prod"
//	root ~ "reg.io/base/debian:*" and label.deprecated exists
package query

import (
	"fmt"
	"strings"
	"unicode"
)

// tokenKind classifies a token.
type tokenKind int

// Token kinds.
const (
	tokEOF tokenKind = iota
	tokIdent
	tokString
	tokNumber
	tokOp
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokComma
)

// token is one lexical unit with its byte offset in the input.
type token struct {
	kind tokenKind
	text string
	pos  int
}

// String formats a token for error messages.
func (t token) String() string {
	if t.kind == tokEOF {
		return "end of filter"
	}
	return fmt.Sprintf("%q", t.text)
}

// SyntaxError reports a problem at a position in the filter.
type SyntaxError struct {
	Pos int
	Msg string
}

// Error formats the error with the 1-based column.
func (e *SyntaxError) Error() string {
	return fmt.Sprintf("column %d: %s", e.Pos+1, e.Msg)
}

// isIdentStart reports whether r can begin an identifier.
func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

// isIdentPart reports whether r can continue an identifier. Dots, dashes,
// slashes and colons are allowed so label keys such as
// label.org.opencontainers.image.base.name and has:sbom read as one word.
func isIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_.-:/", r)
}

// lex splits a filter into tokens.
func lex(src string) ([]token, error) {
	var out []token
	rs := []rune(src)
	// offsets maps rune index to byte offset for error positions.
	offsets := make([]int, len(rs)+1)
	b := 0
	for i, r := range rs {
		offsets[i] = b
		b += len(string(r))
	}
	offsets[len(rs)] = b
	for i := 0; i < len(rs); {
		r := rs[i]
		pos := offsets[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '#':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '(':
			out = append(out, token{tokLParen, "(", pos})
			i++
		case r == ')':
			out = append(out, token{tokRParen, ")", pos})
			i++
		case r == '[':
			out = append(out, token{tokLBracket, "[", pos})
			i++
		case r == ']':
			out = append(out, token{tokRBracket, "]", pos})
			i++
		case r == ',':
			out = append(out, token{tokComma, ",", pos})
			i++
		case r == '"' || r == '\'':
			s, n, err := lexString(rs[i:], pos)
			if err != nil {
				return nil, err
			}
			out = append(out, token{tokString, s, pos})
			i += n
		case strings.ContainsRune("=!<>~", r):
			op := string(r)
			if i+1 < len(rs) && rs[i+1] == '=' && r != '=' && r != '~' {
				op += "="
			}
			if op == "!" {
				return nil, &SyntaxError{pos, `"!" must be followed by "=" (use "not" to negate)`}
			}
			out = append(out, token{tokOp, op, pos})
			i += len([]rune(op))
		case unicode.IsDigit(r) || (r == '-' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i + 1
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || unicode.IsLetter(rs[j]) || rs[j] == '-' || rs[j] == ':') {
				j++
			}
			out = append(out, token{tokNumber, string(rs[i:j]), pos})
			i = j
		case isIdentStart(r):
			j := i + 1
			for j < len(rs) && isIdentPart(rs[j]) {
				j++
			}
			out = append(out, token{tokIdent, string(rs[i:j]), pos})
			i = j
		default:
			return nil, &SyntaxError{pos, fmt.Sprintf("unexpected character %q", r)}
		}
	}
	return append(out, token{tokEOF, "", len(src)}), nil
}

// lexString reads a quoted string starting at rs[0], returning its value
// and the number of runes consumed. Backslash escapes the quote and
// itself.
func lexString(rs []rune, pos int) (string, int, error) {
	q := rs[0]
	var sb strings.Builder
	for i := 1; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			if i+1 < len(rs) {
				i++
				sb.WriteRune(rs[i])
				continue
			}
		case q:
			return sb.String(), i + 1, nil
		}
		sb.WriteRune(rs[i])
	}
	return "", 0, &SyntaxError{pos, "unterminated string"}
}
