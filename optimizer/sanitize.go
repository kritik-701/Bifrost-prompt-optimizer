package optimizer

import (
	"regexp"
	"strings"
)

// Redaction kinds, used as keys in Result.Redactions.
const (
	KindEmail       = "email"
	KindPhone       = "phone"
	KindCreditCard  = "credit_card"
	KindSSN         = "ssn"
	KindAadhaar     = "aadhaar"
	KindSecret      = "secret"
	KindHiddenChars = "hidden_chars"
)

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	reSSN   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)

	// Candidates are validated with a checksum before being redacted, which
	// avoids wiping out order numbers and other harmless digit strings.
	reCardCandidate    = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	reAadhaarCandidate = regexp.MustCompile(`\b[2-9]\d{3}[ -]?\d{4}[ -]?\d{4}\b`)

	rePhoneIntl = regexp.MustCompile(`\+\d{1,3}[ .-]?\(?\d{1,5}\)?(?:[ .-]?\d{2,5}){1,4}`)
	rePhoneUS   = regexp.MustCompile(`(?:\(\d{3}\)|\b\d{3})[ .-]?\d{3}[ .-]\d{4}\b`)
)

type secretPattern struct {
	re          *regexp.Regexp
	placeholder string
}

var secretPatterns = []secretPattern{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`), "[REDACTED_GITHUB_TOKEN]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED_SLACK_TOKEN]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), "[REDACTED_GOOGLE_KEY]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`), "Bearer [REDACTED_TOKEN]"},
}

// sanitize redacts sensitive values and records how many were replaced.
// Order matters: secrets first, then structured identifiers, phones last.
func (c Config) sanitize(text string, counts map[string]int) string {
	if c.RedactSecrets {
		for _, p := range secretPatterns {
			text = replaceMatches(p.re, text, p.placeholder, KindSecret, counts, nil)
		}
	}
	if c.RedactEmails {
		text = replaceMatches(reEmail, text, "[REDACTED_EMAIL]", KindEmail, counts, nil)
	}
	if c.RedactCards {
		text = replaceMatches(reCardCandidate, text, "[REDACTED_CARD]", KindCreditCard, counts, validCard)
	}
	if c.RedactSSN {
		text = replaceMatches(reSSN, text, "[REDACTED_SSN]", KindSSN, counts, nil)
	}
	if c.RedactAadhaar {
		text = replaceMatches(reAadhaarCandidate, text, "[REDACTED_AADHAAR]", KindAadhaar, counts, validAadhaar)
	}
	if c.RedactPhones {
		text = replaceMatches(rePhoneIntl, text, "[REDACTED_PHONE]", KindPhone, counts, validPhone)
		text = replaceMatches(rePhoneUS, text, "[REDACTED_PHONE]", KindPhone, counts, validPhone)
	}
	return text
}

func replaceMatches(re *regexp.Regexp, text, placeholder, kind string, counts map[string]int, valid func(string) bool) string {
	return re.ReplaceAllStringFunc(text, func(m string) string {
		if valid != nil && !valid(m) {
			return m
		}
		counts[kind]++
		return placeholder
	})
}

// stripInvisible removes characters that are invisible to humans but still
// cost tokens or can be used to smuggle instructions into a prompt. It returns
// the cleaned string and how many characters were removed.
func stripInvisible(s string) (string, int) {
	var b strings.Builder
	b.Grow(len(s))
	removed := 0
	for _, r := range s {
		if isInvisible(r) {
			removed++
			continue
		}
		b.WriteRune(r)
	}
	if removed == 0 {
		return s, 0
	}
	return b.String(), removed
}

func isInvisible(r rune) bool {
	switch {
	case r == '\n' || r == '\t' || r == '\r':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f: // C0/C1 controls
		return true
	case r == 0x200B, r == 0x2060, r == 0xFEFF: // zero-width space, word joiner, BOM
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	}
	return false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validPhone(m string) bool {
	n := len(digitsOnly(m))
	return n >= 8 && n <= 15
}

func validCard(m string) bool {
	d := digitsOnly(m)
	return len(d) >= 13 && len(d) <= 19 && luhnValid(d)
}

func validAadhaar(m string) bool {
	d := digitsOnly(m)
	return len(d) == 12 && verhoeffValid(d)
}

func luhnValid(digits string) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

var verhoeffD = [10][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	{1, 2, 3, 4, 0, 6, 7, 8, 9, 5},
	{2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
	{3, 4, 0, 1, 2, 8, 9, 5, 6, 7},
	{4, 0, 1, 2, 3, 9, 5, 6, 7, 8},
	{5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
	{6, 5, 9, 8, 7, 1, 0, 4, 3, 2},
	{7, 6, 5, 9, 8, 2, 1, 0, 4, 3},
	{8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
	{9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
}

var verhoeffP = [8][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	{1, 5, 7, 6, 2, 8, 3, 0, 9, 4},
	{5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
	{8, 9, 1, 6, 0, 4, 3, 5, 2, 7},
	{9, 4, 5, 3, 1, 2, 6, 8, 7, 0},
	{4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
	{2, 7, 9, 3, 8, 0, 6, 4, 1, 5},
	{7, 0, 4, 6, 9, 1, 3, 2, 5, 8},
}

// verhoeffValid reports whether a digit string passes the Verhoeff checksum,
// the scheme used by Aadhaar numbers.
func verhoeffValid(digits string) bool {
	c := 0
	for i := 0; i < len(digits); i++ {
		n := int(digits[len(digits)-1-i] - '0')
		c = verhoeffD[c][verhoeffP[i%8][n]]
	}
	return c == 0
}
