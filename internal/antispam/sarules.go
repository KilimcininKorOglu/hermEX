package antispam

import (
	"bufio"
	"html"
	"net/textproto"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"hermex/internal/mime"
)

// sarules.go implements a subset of the SpamAssassin .cf rule language: header,
// body, rawbody, uri, and meta rules with their scores. It parses a ruleset and
// evaluates it against a message; the matched rules' summed score is surfaced to
// the spam verdict as one bounded signal (the integration lives in antispam.go).
//
// The subset is deliberate. SpamAssassin regexes are Perl-compatible; this engine
// compiles them with Go's RE2, which is linear-time, so a hostile message body
// cannot trigger catastrophic backtracking (ReDoS). Rules whose regex RE2 cannot
// compile — backreferences, lookaround — are dropped, as are eval:/plugin rules
// (network, Bayesian, and SPF/DKIM checks this package performs natively) and
// rules flagged "net" (they need DNS). A meta that references any dropped rule is
// itself dropped, transitively, so a partial dependency never makes a meta
// misfire. SkippedRules and DroppedMetas report the coverage that was lost.

// saKind is the text surface a regex rule matches against.
type saKind uint8

const (
	saHeaderRule saKind = iota
	saBodyRule
	saRawbodyRule
	saURIRule
)

// saRule is one regex test.
type saRule struct {
	name   string
	kind   saKind
	header string         // header rules: the header to match ("ALL" = whole header block)
	negate bool           // header rules: "!~" — matches when the regex does NOT match
	exists bool           // header rules: "exists:" — matches when the header is present
	re     *regexp.Regexp // nil only for an exists test
	score  float64
}

// saMeta is a boolean/arithmetic combination of other rules, stored as the RPN
// of its expression for stack evaluation.
type saMeta struct {
	name  string
	rpn   []saTok
	refs  []string // rule/meta names the expression references
	score float64
}

// SARuleSet is a parsed, compiled SpamAssassin ruleset ready to evaluate.
type SARuleSet struct {
	rules      []*saRule
	metas      []*saMeta
	metaByName map[string]*saMeta

	// SkippedRules counts regex rules dropped at parse: uncompilable under RE2,
	// eval:/plugin, or flagged "net". DroppedMetas counts metas dropped because
	// they reference an unavailable rule.
	SkippedRules int
	DroppedMetas int
}

// RuleCount reports the number of live (evaluable) rules and metas.
func (rs *SARuleSet) RuleCount() (rules, metas int) {
	return len(rs.rules), len(rs.metas)
}

// ParseSARules parses SpamAssassin .cf text into a compiled ruleset. It is
// tolerant: unrecognized directives (describe, lang, if/ifplugin/endif,
// require_version, …) are ignored, and an unparseable rule is dropped and counted
// rather than failing the whole set.
func ParseSARules(text string) *SARuleSet {
	type def struct{ kind, name, rest string }
	var defs []def
	scores := map[string]float64{}
	netFlag := map[string]bool{}

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kw, rest, ok := cutField(line)
		if !ok {
			continue
		}
		switch kw {
		case "header", "body", "rawbody", "uri", "meta":
			name, body, ok := cutField(rest)
			if ok {
				defs = append(defs, def{kw, name, body})
			}
		case "score":
			if name, vals, ok := cutField(rest); ok {
				if f, ok := firstScore(vals); ok {
					scores[name] = f
				}
			}
		case "tflags":
			if name, flags, ok := cutField(rest); ok {
				if hasField(flags, "net") {
					netFlag[name] = true
				}
			}
		}
	}

	rs := &SARuleSet{}
	for _, d := range defs {
		if netFlag[d.name] {
			if d.kind == "meta" {
				rs.DroppedMetas++
			} else {
				rs.SkippedRules++
			}
			continue
		}
		if d.kind == "meta" {
			m, ok := parseMeta(d.name, d.rest, scores[d.name])
			if !ok {
				rs.DroppedMetas++
				continue
			}
			rs.metas = append(rs.metas, m)
			continue
		}
		r, ok := compileRule(d.kind, d.name, d.rest, scores[d.name])
		if !ok {
			rs.SkippedRules++
			continue
		}
		rs.rules = append(rs.rules, r)
	}
	rs.resolveMetas()
	return rs
}

// resolveMetas drops every meta that references an unavailable name (a rule that
// did not compile, a network rule, or an undefined symbol), transitively: a meta
// referencing a dropped meta is dropped too. It computes the surviving set by
// fixpoint, then records metaByName for evaluation.
func (rs *SARuleSet) resolveMetas() {
	available := make(map[string]bool, len(rs.rules))
	for _, r := range rs.rules {
		available[r.name] = true
	}
	surviving := make(map[string]bool, len(rs.metas))
	for {
		changed := false
		for _, m := range rs.metas {
			if surviving[m.name] {
				continue
			}
			ok := true
			for _, ref := range m.refs {
				if !available[ref] {
					ok = false
					break
				}
			}
			if ok {
				surviving[m.name] = true
				available[m.name] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	kept := rs.metas[:0]
	for _, m := range rs.metas {
		if surviving[m.name] {
			kept = append(kept, m)
		} else {
			rs.DroppedMetas++
		}
	}
	rs.metas = kept
	rs.metaByName = make(map[string]*saMeta, len(kept))
	for _, m := range kept {
		rs.metaByName[m.name] = m
	}
}

// Evaluate matches the ruleset against a raw RFC 5322 message and returns the
// summed score of the rules and metas that fired, plus their names. A negative
// total is possible: SpamAssassin "nice" rules carry negative scores (ham
// signals). The caller maps this score onto the spam verdict.
func (rs *SARuleSet) Evaluate(raw []byte) (score float64, fired []string) {
	msg := newSAMessage(raw)
	hits := make(map[string]bool, len(rs.rules)+len(rs.metas))

	for _, r := range rs.rules {
		if rs.matchRule(r, msg) {
			hits[r.name] = true
			if r.score != 0 {
				score += r.score
				fired = append(fired, r.name)
			}
		}
	}

	memo := make(map[string]float64, len(rs.metas))
	inProgress := make(map[string]bool, len(rs.metas))
	var eval func(name string) float64
	eval = func(name string) float64 {
		if v, ok := memo[name]; ok {
			return v
		}
		m := rs.metaByName[name]
		if m == nil { // a rule reference
			if hits[name] {
				return 1
			}
			return 0
		}
		if inProgress[name] { // a meta cycle: treat as not firing
			return 0
		}
		inProgress[name] = true
		v, err := evalRPN(m.rpn, eval)
		inProgress[name] = false
		res := 0.0
		if err == nil && v != 0 {
			res = 1
		}
		memo[name] = res
		return res
	}
	for _, m := range rs.metas {
		if eval(m.name) != 0 {
			hits[m.name] = true
			if m.score != 0 {
				score += m.score
				fired = append(fired, m.name)
			}
		}
	}
	return score, fired
}

// saMessage holds the text surfaces a ruleset matches against, parsed once.
type saMessage struct {
	header    textproto.MIMEHeader
	rawHeader string
	body      string // decoded text, HTML stripped and whitespace collapsed
	rawbody   string // decoded text, markup intact
	uris      []string
}

func newSAMessage(raw []byte) *saMessage {
	root := mime.ParseStructure(raw)
	m := &saMessage{header: root.Header(), rawHeader: string(root.RawHeader())}
	var rb strings.Builder
	collectText(root, &rb)
	m.rawbody = rb.String()
	m.body = collapseWS(html.UnescapeString(htmlTag.ReplaceAllString(m.rawbody, " ")))
	m.uris = uriPattern.FindAllString(m.rawbody, -1)
	return m
}

// matchRule reports whether one rule fires against a message.
func (rs *SARuleSet) matchRule(r *saRule, msg *saMessage) bool {
	switch r.kind {
	case saHeaderRule:
		if r.exists {
			return headerPresent(msg, r.header)
		}
		m := r.re.MatchString(headerText(msg, r.header))
		if r.negate {
			return !m
		}
		return m
	case saBodyRule:
		return r.re.MatchString(msg.body)
	case saRawbodyRule:
		return r.re.MatchString(msg.rawbody)
	case saURIRule:
		return slices.ContainsFunc(msg.uris, r.re.MatchString)
	}
	return false
}

// headerText returns the text a header rule matches against: the SpamAssassin
// pseudo-headers ALL (the whole header block) and ToCc (To + Cc joined), or every
// occurrence of a named header joined by newlines.
func headerText(msg *saMessage, name string) string {
	switch strings.ToUpper(name) {
	case "ALL":
		return msg.rawHeader
	case "TOCC":
		return strings.Join(append(msg.header.Values("To"), msg.header.Values("Cc")...), "\n")
	case "MESSAGEID":
		return strings.Join(msg.header.Values("Message-Id"), "\n")
	default:
		return strings.Join(msg.header.Values(name), "\n")
	}
}

func headerPresent(msg *saMessage, name string) bool {
	switch strings.ToUpper(name) {
	case "ALL":
		return msg.rawHeader != ""
	case "TOCC":
		return len(msg.header.Values("To"))+len(msg.header.Values("Cc")) > 0
	case "MESSAGEID":
		return len(msg.header.Values("Message-Id")) > 0
	default:
		return len(msg.header.Values(name)) > 0
	}
}

var (
	htmlTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	wsRun      = regexp.MustCompile(`\s+`)
	uriPattern = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s"'<>)\]]+|mailto:[^\s"'<>)\]]+`)
)

func collapseWS(s string) string { return strings.TrimSpace(wsRun.ReplaceAllString(s, " ")) }

// compileRule builds a regex rule, returning ok=false when it must be dropped:
// an eval:/plugin rule, an unsupported header form, or a regex RE2 cannot compile.
func compileRule(kind, name, rest string, score float64) (*saRule, bool) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "eval:") {
		return nil, false
	}
	if kind == "header" {
		return compileHeaderRule(name, rest, score)
	}
	re, ok := compileBodyRegex(rest)
	if !ok {
		return nil, false
	}
	k := saBodyRule
	switch kind {
	case "rawbody":
		k = saRawbodyRule
	case "uri":
		k = saURIRule
	}
	return &saRule{name: name, kind: k, re: re, score: score}, true
}

func compileHeaderRule(name, rest string, score float64) (*saRule, bool) {
	if after, ok := strings.CutPrefix(rest, "exists:"); ok {
		h := normHeaderName(strings.TrimSpace(after))
		return &saRule{name: name, kind: saHeaderRule, header: h, exists: true, score: score}, true
	}
	negate := false
	idx := strings.Index(rest, "=~")
	if i := strings.Index(rest, "!~"); i >= 0 {
		negate, idx = true, i
	}
	if idx < 0 {
		return nil, false // unsupported header form (e.g. an eval already filtered)
	}
	hdr := normHeaderName(strings.TrimSpace(rest[:idx]))
	re, ok := compileBodyRegex(strings.TrimSpace(rest[idx+2:]))
	if !ok {
		return nil, false
	}
	return &saRule{name: name, kind: saHeaderRule, header: hdr, negate: negate, re: re, score: score}, true
}

// compileBodyRegex extracts the /pattern/flags from a rule body and compiles it
// under RE2, translating the i/s/m flags. It fails on a non-slash delimiter, the
// free-spacing /x flag (RE2 has no equivalent), or any RE2-incompatible construct.
func compileBodyRegex(s string) (*regexp.Regexp, bool) {
	pat, mods, ok := extractRegex(s)
	if !ok || strings.ContainsRune(mods, 'x') {
		return nil, false
	}
	re, err := regexp.Compile(toGoRegex(pat, mods))
	if err != nil {
		return nil, false
	}
	return re, true
}

// normHeaderName strips a SpamAssassin header modifier suffix (:addr, :name,
// :raw, …) and returns the bare header name. The address/name extraction those
// modifiers request is approximated by matching the raw header value, which is
// adequate for the rule subset and never over-matches.
func normHeaderName(h string) string {
	name, _, _ := strings.Cut(h, ":")
	return name
}

// extractRegex pulls the /pattern/flags out of a rule body. For header rules the
// caller passes the part after =~/!~; for body rules the whole remainder.
func extractRegex(body string) (pat, mods string, ok bool) {
	start := strings.IndexByte(body, '/')
	if start < 0 {
		return "", "", false
	}
	for i := start + 1; i < len(body); i++ {
		if body[i] == '\\' {
			i++
			continue
		}
		if body[i] == '/' {
			pat = body[start+1 : i]
			j := i + 1
			for j < len(body) && isModByte(body[j]) {
				j++
			}
			return pat, body[i+1 : j], true
		}
	}
	return "", "", false
}

func isModByte(b byte) bool {
	switch b {
	case 'i', 's', 'm', 'x', 'g', 'o', 'a', 'A':
		return true
	}
	return false
}

// toGoRegex translates a Perl regex and its modifiers into an RE2 pattern,
// hoisting the supported i/s/m flags into a leading (?…) group.
func toGoRegex(pat, mods string) string {
	var flags string
	for _, m := range mods {
		switch m {
		case 'i':
			flags += "i"
		case 's':
			flags += "s"
		case 'm':
			flags += "m"
		}
	}
	if flags != "" {
		return "(?" + flags + ")" + pat
	}
	return pat
}

func firstScore(vals string) (float64, bool) {
	f := strings.Fields(vals)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// cutField splits off the first whitespace-delimited token and the trimmed rest.
func cutField(s string) (head, tail string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:]), true
	}
	return s, "", true
}

func hasField(s, want string) bool {
	return slices.Contains(strings.Fields(s), want)
}
