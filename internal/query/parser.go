package query

import (
	"fmt"
	"strings"
)

// Node is a parsed filter expression.
type Node interface {
	// String formats the node back into filter syntax.
	String() string
}

// And matches when both sides match.
type And struct{ L, R Node }

// Or matches when either side matches.
type Or struct{ L, R Node }

// Not matches when its operand does not.
type Not struct{ X Node }

// Compare is field op value, such as age > 180d.
type Compare struct {
	Field string
	Op    string
	Value Value
	Pos   int
}

// In is field in [values].
type In struct {
	Field  string
	Values []Value
	Pos    int
}

// Exists is field exists.
type Exists struct {
	Field string
	Pos   int
}

// Flag is a field used on its own as a condition, such as has:sbom.
type Flag struct {
	Field string
	Pos   int
}

// Call is a function such as ancestor("reg.io/base/go:1.21").
type Call struct {
	Name string
	Args []Value
	Pos  int
}

// Value is a literal.
type Value struct {
	// Quoted is true for string literals.
	Quoted bool
	Text   string
	Pos    int
}

// String formats an And.
func (n And) String() string { return "(" + n.L.String() + " and " + n.R.String() + ")" }

// String formats an Or.
func (n Or) String() string { return "(" + n.L.String() + " or " + n.R.String() + ")" }

// String formats a Not.
func (n Not) String() string { return "not " + n.X.String() }

// String formats a Compare.
func (n Compare) String() string { return n.Field + " " + n.Op + " " + n.Value.String() }

// String formats an In.
func (n In) String() string {
	parts := make([]string, len(n.Values))
	for i, v := range n.Values {
		parts[i] = v.String()
	}
	return n.Field + " in [" + strings.Join(parts, ", ") + "]"
}

// String formats an Exists.
func (n Exists) String() string { return n.Field + " exists" }

// String formats a Flag.
func (n Flag) String() string { return n.Field }

// String formats a Call.
func (n Call) String() string {
	parts := make([]string, len(n.Args))
	for i, v := range n.Args {
		parts[i] = v.String()
	}
	return n.Name + "(" + strings.Join(parts, ", ") + ")"
}

// String formats a Value, quoting strings with the only escapes the lexer
// reads, \" and \\, so printing and re-parsing gives the same value.
func (v Value) String() string {
	if v.Quoted {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v.Text) + `"`
	}
	return v.Text
}

// parser is a recursive-descent parser over tokens.
type parser struct {
	toks []token
	i    int
}

// Parse parses a filter. An empty filter parses to nil, which matches
// every image.
func Parse(src string) (Node, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	if toks[0].kind == tokEOF {
		return nil, nil
	}
	p := &parser{toks: toks}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, &SyntaxError{t.pos, fmt.Sprintf("unexpected %s, expected \"and\", \"or\" or the end", t)}
	}
	return n, nil
}

// peek returns the current token.
func (p *parser) peek() token { return p.toks[p.i] }

// next consumes and returns the current token.
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

// keyword reports whether the current token is the given keyword.
func (p *parser) keyword(k string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.text, k)
}

// or parses and-expressions joined by "or".
func (p *parser) or() (Node, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("or") {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = Or{l, r}
	}
	return l, nil
}

// and parses unary expressions joined by "and".
func (p *parser) and() (Node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.keyword("and") {
		p.next()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = And{l, r}
	}
	return l, nil
}

// unary parses "not" and parenthesized expressions.
func (p *parser) unary() (Node, error) {
	if p.keyword("not") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return Not{x}, nil
	}
	if p.peek().kind == tokLParen {
		p.next()
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if t := p.next(); t.kind != tokRParen {
			return nil, &SyntaxError{t.pos, fmt.Sprintf("expected \")\", got %s", t)}
		}
		return n, nil
	}
	return p.predicate()
}

// predicate parses a comparison, membership, exists, call or bare flag.
func (p *parser) predicate() (Node, error) {
	t := p.next()
	if t.kind != tokIdent {
		return nil, &SyntaxError{t.pos, fmt.Sprintf("expected a field, got %s", t)}
	}
	switch strings.ToLower(t.text) {
	case "and", "or", "in", "exists":
		return nil, &SyntaxError{t.pos, fmt.Sprintf("expected a field, got keyword %s", t)}
	}
	if p.peek().kind == tokLParen {
		return p.call(t)
	}
	switch {
	case p.peek().kind == tokOp:
		op := p.next()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		return Compare{Field: t.text, Op: op.text, Value: v, Pos: t.pos}, nil
	case p.keyword("in"):
		p.next()
		vals, err := p.list()
		if err != nil {
			return nil, err
		}
		return In{Field: t.text, Values: vals, Pos: t.pos}, nil
	case p.keyword("exists"):
		p.next()
		return Exists{Field: t.text, Pos: t.pos}, nil
	}
	return Flag{Field: t.text, Pos: t.pos}, nil
}

// call parses name(arg, ...).
func (p *parser) call(name token) (Node, error) {
	p.next()
	var args []Value
	for p.peek().kind != tokRParen {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		args = append(args, v)
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		if p.peek().kind != tokRParen {
			t := p.peek()
			return nil, &SyntaxError{t.pos, fmt.Sprintf("expected \",\" or \")\", got %s", t)}
		}
	}
	p.next()
	return Call{Name: name.text, Args: args, Pos: name.pos}, nil
}

// list parses [value, ...].
func (p *parser) list() ([]Value, error) {
	if t := p.next(); t.kind != tokLBracket {
		return nil, &SyntaxError{t.pos, fmt.Sprintf("expected \"[\", got %s", t)}
	}
	var vals []Value
	for p.peek().kind != tokRBracket {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		if p.peek().kind != tokRBracket {
			t := p.peek()
			return nil, &SyntaxError{t.pos, fmt.Sprintf("expected \",\" or \"]\", got %s", t)}
		}
	}
	p.next()
	if len(vals) == 0 {
		return nil, &SyntaxError{p.peek().pos, "empty list"}
	}
	return vals, nil
}

// value parses a literal: a string, number, or bare word such as true.
func (p *parser) value() (Value, error) {
	t := p.next()
	switch t.kind {
	case tokString:
		return Value{Quoted: true, Text: t.text, Pos: t.pos}, nil
	case tokNumber, tokIdent:
		return Value{Text: t.text, Pos: t.pos}, nil
	}
	return Value{}, &SyntaxError{t.pos, fmt.Sprintf("expected a value, got %s", t)}
}
