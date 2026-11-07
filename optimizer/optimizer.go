package optimizer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxIndent    = 8  // leading indentation is capped at this many spaces
	minDedupeLen = 20 // shorter lines are never de-duplicated
)

// Result describes the outcome of optimizing one piece of text.
type Result struct {
	Text         string
	Changed      bool
	Redactions   map[string]int // kind -> number of items sanitized
	TokensBefore int            // estimated
	TokensAfter  int            // estimated
}

// TokensSaved is the net estimated token reduction. It can be negative for
// very short prompts because redaction placeholders may be longer than the
// value they replace.
func (r Result) TokensSaved() int { return r.TokensBefore - r.TokensAfter }

// RedactionTotal is the total number of sanitized items.
func (r Result) RedactionTotal() int {
	total := 0
	for _, n := range r.Redactions {
		total += n
	}
	return total
}

// Add merges another result's counters into r (used to combine the messages of
// one request). Text is intentionally not merged.
func (r *Result) Add(o Result) {
	r.TokensBefore += o.TokensBefore
	r.TokensAfter += o.TokensAfter
	r.Changed = r.Changed || o.Changed
	if r.Redactions == nil {
		r.Redactions = make(map[string]int, len(o.Redactions))
	}
	for k, v := range o.Redactions {
		r.Redactions[k] += v
	}
}

// Optimizer applies a Config to text. It is safe for concurrent use because it
// holds no mutable state.
type Optimizer struct {
	cfg   Config
	roles map[string]struct{}
}

// New creates an Optimizer for the given configuration.
func New(cfg Config) *Optimizer {
	roles := make(map[string]struct{}, len(cfg.Roles))
	for _, r := range cfg.Roles {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			roles[r] = struct{}{}
		}
	}
	return &Optimizer{cfg: cfg, roles: roles}
}

// Config returns the configuration in use.
func (o *Optimizer) Config() Config { return o.cfg }

// AppliesToRole reports whether messages with this role should be optimized.
func (o *Optimizer) AppliesToRole(role string) bool {
	if !o.cfg.Enabled {
		return false
	}
	_, ok := o.roles[strings.ToLower(strings.TrimSpace(role))]
	return ok
}

// Optimize sanitizes and compresses text. Fenced code blocks (``` ... ```) are
// never reformatted, because whitespace is meaningful in code; they are still
// scanned for secrets.
func (o *Optimizer) Optimize(text string) Result {
	res := Result{
		Text:         text,
		Redactions:   map[string]int{},
		TokensBefore: EstimateTokens(text),
	}
	res.TokensAfter = res.TokensBefore
	if !o.cfg.Enabled || text == "" {
		return res
	}

	out := strings.ReplaceAll(text, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")

	if o.cfg.StripInvisible {
		var removed int
		out, removed = stripInvisible(out)
		if removed > 0 {
			res.Redactions[KindHiddenChars] = removed
		}
	}

	out = o.cfg.sanitize(out, res.Redactions)

	if o.cfg.CollapseWhitespace || o.cfg.DedupeLines || o.cfg.CompressPhrases {
		out = o.compress(out)
	}

	res.Text = out
	res.Changed = out != text
	res.TokensAfter = EstimateTokens(out)
	return res
}

// compress walks the text line by line, leaving fenced code untouched and
// compressing everything else.
func (o *Optimizer) compress(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	prose := make([]string, 0, len(lines))
	inFence := false

	flush := func() {
		if len(prose) > 0 {
			out = append(out, o.compressProse(prose)...)
			prose = prose[:0]
		}
	}

	for _, line := range lines {
		fence := isFenceLine(line)
		switch {
		case inFence:
			out = append(out, line)
			if fence {
				inFence = false
			}
		case fence:
			flush()
			out = append(out, line)
			inFence = true
		default:
			prose = append(prose, line)
		}
	}
	flush()

	res := strings.Join(out, "\n")
	if o.cfg.CollapseWhitespace {
		res = strings.TrimLeft(res, "\n")
		res = strings.TrimRight(res, " \t\n")
	}
	return res
}

func isFenceLine(line string) bool {
	t := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

func (o *Optimizer) compressProse(lines []string) []string {
	out := make([]string, 0, len(lines))
	blanks := 0
	prev := ""

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			prev = ""
			if o.cfg.CollapseWhitespace {
				blanks++
				if blanks > o.cfg.MaxBlankLines {
					continue
				}
				out = append(out, "")
			} else {
				out = append(out, line)
			}
			continue
		}
		blanks = 0

		if o.cfg.CompressPhrases {
			line = compressPhrases(line)
		}
		if o.cfg.CollapseWhitespace {
			line = collapseLine(line)
		}
		if o.cfg.DedupeLines && utf8.RuneCountInString(strings.TrimSpace(line)) >= minDedupeLen && line == prev {
			continue
		}
		out = append(out, line)
		prev = line
	}
	return out
}

// collapseLine squeezes internal whitespace to single spaces, trims trailing
// whitespace and caps leading indentation so nested lists keep their shape.
func collapseLine(line string) string {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	indent := line[:len(line)-len(trimmed)]
	width := 0
	for _, r := range indent {
		if r == '\t' {
			width += 2
		} else {
			width++
		}
	}
	if width > maxIndent {
		width = maxIndent
	}
	return strings.Repeat(" ", width) + strings.Join(strings.Fields(trimmed), " ")
}

type phraseRule struct {
	re *regexp.Regexp
	to string
}

var phraseRules = buildPhraseRules([][2]string{
	{"in order to", "to"},
	{"due to the fact that", "because"},
	{"in spite of the fact that", "although"},
	{"despite the fact that", "although"},
	{"in the event that", "if"},
	{"at this point in time", "now"},
	{"at the present time", "now"},
	{"in the near future", "soon"},
	{"a large number of", "many"},
	{"a majority of", "most"},
	{"for the purpose of", "for"},
	{"with regard to", "about"},
	{"with respect to", "about"},
	{"is able to", "can"},
	{"are able to", "can"},
	{"has the ability to", "can"},
	{"have the ability to", "can"},
	{"each and every", "every"},
	{"first and foremost", "first"},
	{"whether or not", "whether"},
	{"make use of", "use"},
	{"prior to", "before"},
})

func buildPhraseRules(pairs [][2]string) []phraseRule {
	rules := make([]phraseRule, 0, len(pairs))
	for _, p := range pairs {
		rules = append(rules, phraseRule{
			re: regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(p[0]) + `\b`),
			to: p[1],
		})
	}
	return rules
}

func compressPhrases(line string) string {
	if len(line) < 6 {
		return line
	}
	for _, rule := range phraseRules {
		line = rule.re.ReplaceAllStringFunc(line, func(m string) string {
			return matchCase(m, rule.to)
		})
	}
	return line
}

// matchCase capitalizes the replacement when the matched phrase started with a
// capital letter, so "In order to" becomes "To" and not "to".
func matchCase(match, replacement string) string {
	first, _ := utf8.DecodeRuneInString(match)
	if unicode.IsUpper(first) && replacement != "" {
		r, size := utf8.DecodeRuneInString(replacement)
		return string(unicode.ToUpper(r)) + replacement[size:]
	}
	return replacement
}
