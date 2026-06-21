package antispam

import (
	"fmt"
	"strconv"
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
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			n, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, err
			}
			toks = append(toks, saTok{kind: tokNum, num: n})
			i = j
		case isNameByte(c):
			j := i
			for j < len(s) && isNameByte(s[j]) {
				j++
			}
			toks = append(toks, saTok{kind: tokName, name: s[i:j]})
			i = j
		case c == '(' || c == ')':
			toks = append(toks, saTok{kind: tokOp, op: string(c)})
			i++
		case c == '+' || c == '-' || c == '*' || c == '/':
			toks = append(toks, saTok{kind: tokOp, op: string(c)})
			i++
		case c == '&':
			if i+1 < len(s) && s[i+1] == '&' {
				toks = append(toks, saTok{kind: tokOp, op: "&&"})
				i += 2
			} else {
				return nil, fmt.Errorf("lone &")
			}
		case c == '|':
			if i+1 < len(s) && s[i+1] == '|' {
				toks = append(toks, saTok{kind: tokOp, op: "||"})
				i += 2
			} else {
				return nil, fmt.Errorf("lone |")
			}
		case c == '!':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, saTok{kind: tokOp, op: "!="})
				i += 2
			} else {
				toks = append(toks, saTok{kind: tokOp, op: "!"})
				i++
			}
		case c == '=':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, saTok{kind: tokOp, op: "=="})
				i += 2
			} else {
				return nil, fmt.Errorf("lone =")
			}
		case c == '>':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, saTok{kind: tokOp, op: ">="})
				i += 2
			} else {
				toks = append(toks, saTok{kind: tokOp, op: ">"})
				i++
			}
		case c == '<':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, saTok{kind: tokOp, op: "<="})
				i += 2
			} else {
				toks = append(toks, saTok{kind: tokOp, op: "<"})
				i++
			}
		default:
			return nil, fmt.Errorf("bad character %q", c)
		}
	}
	return toks, nil
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
	for _, t := range toks {
		switch {
		case t.kind == tokNum || t.kind == tokName:
			out = append(out, t)
		case t.op == "(":
			ops = append(ops, t)
		case t.op == ")":
			for len(ops) > 0 && ops[len(ops)-1].op != "(" {
				out = append(out, ops[len(ops)-1])
				ops = ops[:len(ops)-1]
			}
			if len(ops) == 0 {
				return nil, fmt.Errorf("unbalanced )")
			}
			ops = ops[:len(ops)-1]
		default: // an operator
			p, known := precedence[t.op]
			if !known {
				return nil, fmt.Errorf("bad operator %q", t.op)
			}
			rightAssoc := t.op == "!"
			for len(ops) > 0 {
				top := ops[len(ops)-1]
				if top.op == "(" {
					break
				}
				tp := precedence[top.op]
				if tp > p || (tp == p && !rightAssoc) {
					out = append(out, top)
					ops = ops[:len(ops)-1]
				} else {
					break
				}
			}
			ops = append(ops, t)
		}
	}
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
	var st []float64
	pop := func() (float64, error) {
		if len(st) == 0 {
			return 0, fmt.Errorf("stack underflow")
		}
		v := st[len(st)-1]
		st = st[:len(st)-1]
		return v, nil
	}
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	for _, t := range rpn {
		switch t.kind {
		case tokNum:
			st = append(st, t.num)
		case tokName:
			st = append(st, value(t.name))
		case tokOp:
			if t.op == "!" {
				a, err := pop()
				if err != nil {
					return 0, err
				}
				st = append(st, b2f(a == 0))
				continue
			}
			b, err := pop()
			if err != nil {
				return 0, err
			}
			a, err := pop()
			if err != nil {
				return 0, err
			}
			switch t.op {
			case "&&":
				st = append(st, b2f(a != 0 && b != 0))
			case "||":
				st = append(st, b2f(a != 0 || b != 0))
			case "==":
				st = append(st, b2f(a == b))
			case "!=":
				st = append(st, b2f(a != b))
			case "<":
				st = append(st, b2f(a < b))
			case ">":
				st = append(st, b2f(a > b))
			case "<=":
				st = append(st, b2f(a <= b))
			case ">=":
				st = append(st, b2f(a >= b))
			case "+":
				st = append(st, a+b)
			case "-":
				st = append(st, a-b)
			case "*":
				st = append(st, a*b)
			case "/":
				if b == 0 {
					st = append(st, 0)
				} else {
					st = append(st, a/b)
				}
			default:
				return 0, fmt.Errorf("bad operator %q", t.op)
			}
		}
	}
	if len(st) != 1 {
		return 0, fmt.Errorf("malformed expression")
	}
	return st[0], nil
}
