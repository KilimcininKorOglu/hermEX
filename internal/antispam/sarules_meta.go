package antispam

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// sarules_meta.go evaluates SpamAssassin meta-rule expressions: boolean and
// arithmetic combinations of other rules, e.g. "(A && !B) || C > 1". A rule name
// evaluates to whether it fired (1/0); the result is truthy when non-zero. The
// expression is tokenized, converted to RPN by the shunting-yard algorithm at
// parse time (so a malformed expression is rejected once, not per message), and
// run on a small stack machine at evaluation time.

// saTokKind distinguishes the three token shapes.
type saTokKind uint8

const (
	tokNum saTokKind = iota
	tokName
	tokOp
)

type saTok struct {
	kind saTokKind
	num  float64
	name string
	op   string
}

// parseMeta tokenizes and compiles a meta expression. ok is false when the
// expression uses an unsupported construct or is malformed, in which case the
// caller drops the meta.
func parseMeta(name, expr string, score float64) (*saMeta, bool) {
	toks, err := tokenizeMeta(expr)
	if err != nil {
		return nil, false
	}
	rpn, err := shuntingYard(toks)
	if err != nil {
		return nil, false
	}
	// Validate the RPN's stack discipline once, at parse: the result depends only
	// on the token structure, not the values, so a dry run with every name zero
	// rejects a malformed expression (e.g. an operator missing an operand) here
	// rather than letting it silently never fire at evaluation time.
	if _, err := evalRPN(rpn, func(string) float64 { return 0 }); err != nil {
		return nil, false
	}
	var refs []string
	for _, t := range toks {
		if t.kind == tokName {
			refs = append(refs, t.name)
		}
	}
	return &saMeta{name: name, rpn: rpn, refs: refs, score: score}, true
}

func tokenizeMeta(s string) ([]saTok, error) {
	var toks []saTok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case isNumberByte(c):
			tok, next, err := scanMetaNumber(s, i)
			if err != nil {
				return nil, err
			}
			toks, i = append(toks, tok), next
		case isNameByte(c):
			tok, next := scanMetaName(s, i)
			toks, i = append(toks, tok), next
		default:
			tok, next, err := scanMetaOperator(s, i)
			if err != nil {
				return nil, err
			}
			toks, i = append(toks, tok), next
		}
	}
	return toks, nil
}

// isNumberByte reports whether a byte can open or continue a numeric literal.
func isNumberByte(b byte) bool { return b >= '0' && b <= '9' || b == '.' }

// scanMetaNumber reads the numeric literal starting at i.
func scanMetaNumber(s string, i int) (saTok, int, error) {
	j := i
	for j < len(s) && isNumberByte(s[j]) {
		j++
	}
	n, err := strconv.ParseFloat(s[i:j], 64)
	if err != nil {
		return saTok{}, 0, err
	}
	return saTok{kind: tokNum, num: n}, j, nil
}

// scanMetaName reads the rule/meta name starting at i.
func scanMetaName(s string, i int) (saTok, int) {
	j := i
	for j < len(s) && isNameByte(s[j]) {
		j++
	}
	return saTok{kind: tokName, name: s[i:j]}, j
}

// metaOperators2 are the two-byte operators, matched before the one-byte ones so
// "&&" never reads as two "&". A "&", "|" or "=" that does not pair is rejected,
// because it is not an operator this grammar has on its own.
var metaOperators2 = []string{"&&", "||", "!=", "==", ">=", "<="}

// metaOperators1 are the one-byte operators, including the grouping parentheses.
const metaOperators1 = "()+-*/!<>"

// scanMetaOperator reads the operator starting at i.
func scanMetaOperator(s string, i int) (saTok, int, error) {
	if i+1 < len(s) {
		if two := s[i : i+2]; slices.Contains(metaOperators2, two) {
			return saTok{kind: tokOp, op: two}, i + 2, nil
		}
	}
	c := s[i]
	if strings.IndexByte(metaOperators1, c) < 0 {
		return saTok{}, 0, fmt.Errorf("bad character %q", c)
	}
	return saTok{kind: tokOp, op: string(c)}, i + 1, nil
}

func isNameByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

var precedence = map[string]int{
	"||": 1, "&&": 2,
	"==": 3, "!=": 3, "<": 3, ">": 3, "<=": 3, ">=": 3,
	"+": 4, "-": 4, "*": 5, "/": 5,
	"!": 6,
}

// shuntingYard converts the infix token stream to RPN. The unary "!" is the only
// right-associative, prefix operator.
func shuntingYard(toks []saTok) ([]saTok, error) {
	var out, ops []saTok
	var err error
	for _, t := range toks {
		switch {
		case t.kind == tokNum || t.kind == tokName:
			out = append(out, t)
		case t.op == "(":
			ops = append(ops, t)
		case t.op == ")":
			if out, ops, err = closeGroup(out, ops); err != nil {
				return nil, err
			}
		default: // an operator
			if out, ops, err = pushOperator(out, ops, t); err != nil {
				return nil, err
			}
		}
	}
	return drainOperators(out, ops)
}

// closeGroup pops operators to the output until the matching "(", which it
// discards.
func closeGroup(out, ops []saTok) ([]saTok, []saTok, error) {
	for len(ops) > 0 && ops[len(ops)-1].op != "(" {
		out = append(out, ops[len(ops)-1])
		ops = ops[:len(ops)-1]
	}
	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("unbalanced )")
	}
	return out, ops[:len(ops)-1], nil
}

// pushOperator pops every operator that binds at least as tightly as t before
// stacking it. The unary "!" is right-associative, so an equal precedence does
// not pop.
func pushOperator(out, ops []saTok, t saTok) ([]saTok, []saTok, error) {
	p, known := precedence[t.op]
	if !known {
		return nil, nil, fmt.Errorf("bad operator %q", t.op)
	}
	rightAssoc := t.op == "!"
	for len(ops) > 0 {
		top := ops[len(ops)-1]
		tp, isOp := precedence[top.op]
		if !isOp || tp < p || (tp == p && rightAssoc) {
			break
		}
		out = append(out, top)
		ops = ops[:len(ops)-1]
	}
	return out, append(ops, t), nil
}

// drainOperators appends what is left on the operator stack, refusing a group
// that was never closed.
func drainOperators(out, ops []saTok) ([]saTok, error) {
	for len(ops) > 0 {
		if ops[len(ops)-1].op == "(" {
			return nil, fmt.Errorf("unbalanced (")
		}
		out = append(out, ops[len(ops)-1])
		ops = ops[:len(ops)-1]
	}
	return out, nil
}

// evalRPN runs the RPN on a stack machine. value resolves a rule/meta name to its
// numeric value (1 fired, 0 not). A structurally bad expression returns an error,
// which the caller treats as the meta not firing.
func evalRPN(rpn []saTok, value func(name string) float64) (float64, error) {
	var st metaStack
	for _, t := range rpn {
		if err := applyMetaToken(&st, t, value); err != nil {
			return 0, err
		}
	}
	if len(st.v) != 1 {
		return 0, fmt.Errorf("malformed expression")
	}
	return st.v[0], nil
}

// metaStack is the operand stack the RPN machine runs on.
type metaStack struct{ v []float64 }

// push places one operand on the stack.
func (s *metaStack) push(x float64) { s.v = append(s.v, x) }

// pop takes the top operand, reporting an expression that asked for more
// operands than it supplied.
func (s *metaStack) pop() (float64, error) {
	if len(s.v) == 0 {
		return 0, fmt.Errorf("stack underflow")
	}
	x := s.v[len(s.v)-1]
	s.v = s.v[:len(s.v)-1]
	return x, nil
}

// binaryMetaOps are the two-operand operators a meta expression may use. Division
// by zero yields zero rather than an infinity, so one bad rule cannot swamp a
// score.
var binaryMetaOps = map[string]func(a, b float64) float64{
	"&&": func(a, b float64) float64 { return boolScore(a != 0 && b != 0) },
	"||": func(a, b float64) float64 { return boolScore(a != 0 || b != 0) },
	"==": func(a, b float64) float64 { return boolScore(a == b) },
	"!=": func(a, b float64) float64 { return boolScore(a != b) },
	"<":  func(a, b float64) float64 { return boolScore(a < b) },
	">":  func(a, b float64) float64 { return boolScore(a > b) },
	"<=": func(a, b float64) float64 { return boolScore(a <= b) },
	">=": func(a, b float64) float64 { return boolScore(a >= b) },
	"+":  func(a, b float64) float64 { return a + b },
	"-":  func(a, b float64) float64 { return a - b },
	"*":  func(a, b float64) float64 { return a * b },
	"/": func(a, b float64) float64 {
		if b == 0 {
			return 0
		}
		return a / b
	},
}

// applyMetaToken runs one RPN token against the stack: a literal and a name push
// their value, the unary "!" negates the top, and every other operator consumes
// two operands.
func applyMetaToken(st *metaStack, t saTok, value func(name string) float64) error {
	switch t.kind {
	case tokNum:
		st.push(t.num)
		return nil
	case tokName:
		st.push(value(t.name))
		return nil
	}
	if t.op == "!" {
		a, err := st.pop()
		if err != nil {
			return err
		}
		st.push(boolScore(a == 0))
		return nil
	}
	apply, known := binaryMetaOps[t.op]
	if !known {
		return fmt.Errorf("bad operator %q", t.op)
	}
	b, err := st.pop()
	if err != nil {
		return err
	}
	a, err := st.pop()
	if err != nil {
		return err
	}
	st.push(apply(a, b))
	return nil
}
