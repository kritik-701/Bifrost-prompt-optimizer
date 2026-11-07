// Package optimizer contains the framework-independent prompt optimization
// logic: whitespace normalization, phrase compression, de-duplication and
// input sanitization. It has no dependency on Bifrost, which keeps it fast to
// test and easy to reuse.
package optimizer

import (
	"fmt"
	"math"
	"strings"
)

// Config controls what the optimizer does. Use DefaultConfig as the starting
// point; every feature is enabled by default except where noted.
type Config struct {
	// Enabled is the master switch. When false, Optimize returns its input.
	Enabled bool

	// Roles lists the chat message roles that get optimized
	// (case-insensitive). Tool and assistant messages are left alone by default
	// because tool output is often JSON or code where whitespace matters.
	Roles []string

	// CollapseWhitespace squeezes runs of spaces/tabs inside lines, caps
	// leading indentation, trims trailing whitespace and limits blank lines.
	CollapseWhitespace bool

	// MaxBlankLines is the largest number of consecutive blank lines kept when
	// CollapseWhitespace is on. 1 keeps paragraph breaks.
	MaxBlankLines int

	// DedupeLines drops a line that is identical to the line directly above
	// it. Only lines of at least 20 characters are considered, so separators
	// such as "---" are never removed.
	DedupeLines bool

	// CompressPhrases rewrites wordy phrases ("in order to" -> "to").
	CompressPhrases bool

	// StripInvisible removes control characters, zero-width spaces, BOMs and
	// bidirectional override characters. Zero-width joiners and non-joiners
	// are kept because Indic, Persian and emoji text depend on them.
	StripInvisible bool

	// Redaction toggles. Matches are replaced with placeholders such as
	// [REDACTED_EMAIL].
	RedactEmails  bool
	RedactPhones  bool
	RedactCards   bool // 13-19 digit numbers that pass the Luhn check
	RedactSSN     bool // US format 123-45-6789
	RedactAadhaar bool // 12-digit Indian Aadhaar numbers that pass the Verhoeff check
	RedactSecrets bool // API keys, tokens, JWTs, private key blocks
}

// DefaultConfig returns the recommended defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		Roles:              []string{"system", "user"},
		CollapseWhitespace: true,
		MaxBlankLines:      1,
		DedupeLines:        true,
		CompressPhrases:    true,
		StripInvisible:     true,
		RedactEmails:       true,
		RedactPhones:       true,
		RedactCards:        true,
		RedactSSN:          true,
		RedactAadhaar:      true,
		RedactSecrets:      true,
	}
}

// ConfigFromMap builds a Config from the JSON object found in Bifrost's
// config.json ("config" block of the plugin entry). Unknown keys are ignored so
// plugin-level keys (such as stats_addr) can live in the same object. Keys with
// the wrong type return an error instead of being silently ignored.
func ConfigFromMap(m map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if m == nil {
		return cfg, nil
	}

	boolKeys := []struct {
		key string
		dst *bool
	}{
		{"enabled", &cfg.Enabled},
		{"collapse_whitespace", &cfg.CollapseWhitespace},
		{"dedupe_lines", &cfg.DedupeLines},
		{"compress_phrases", &cfg.CompressPhrases},
		{"strip_invisible", &cfg.StripInvisible},
		{"redact_emails", &cfg.RedactEmails},
		{"redact_phones", &cfg.RedactPhones},
		{"redact_credit_cards", &cfg.RedactCards},
		{"redact_ssn", &cfg.RedactSSN},
		{"redact_aadhaar", &cfg.RedactAadhaar},
		{"redact_secrets", &cfg.RedactSecrets},
	}
	for _, k := range boolKeys {
		v, ok := m[k.key]
		if !ok {
			continue
		}
		b, ok := v.(bool)
		if !ok {
			return cfg, fmt.Errorf("config %q must be true or false, got %T", k.key, v)
		}
		*k.dst = b
	}

	if v, ok := m["max_blank_lines"]; ok {
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int:
			f = float64(n)
		default:
			return cfg, fmt.Errorf("config \"max_blank_lines\" must be a number, got %T", v)
		}
		if f < 0 || f != math.Trunc(f) || f > 1000 {
			return cfg, fmt.Errorf("config \"max_blank_lines\" must be a whole number between 0 and 1000, got %v", f)
		}
		cfg.MaxBlankLines = int(f)
	}

	if v, ok := m["roles"]; ok {
		roles, err := stringList(v)
		if err != nil {
			return cfg, fmt.Errorf("config \"roles\": %w", err)
		}
		cfg.Roles = roles
	}

	return cfg, nil
}

func stringList(v any) ([]string, error) {
	switch list := v.(type) {
	case []string:
		return append([]string(nil), list...), nil
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("must be a list of strings, found %T", item)
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be a list of strings, got %T", v)
	}
}
