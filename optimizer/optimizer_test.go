package optimizer

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newDefault() *Optimizer { return New(DefaultConfig()) }

// Test 1: the core job. Whitespace is squeezed and tokens go down.
func TestCollapsesWhitespaceAndReducesTokens(t *testing.T) {
	in := "Hello    world.   \n\n\n\n\nThis   is   a   test.\t\t\n   \n"
	want := "Hello world.\n\nThis is a test."

	res := newDefault().Optimize(in)

	if res.Text != want {
		t.Fatalf("text mismatch\n got: %q\nwant: %q", res.Text, want)
	}
	if !res.Changed {
		t.Error("Changed should be true")
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Errorf("expected fewer tokens: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}
	if res.TokensSaved() != res.TokensBefore-res.TokensAfter {
		t.Error("TokensSaved must equal before-after")
	}
}

// Test 2: whitespace inside fenced code is meaningful and must survive.
func TestPreservesFencedCode(t *testing.T) {
	in := "Please  fix   this:\n```go\nfunc  main() {\n\t\tx   :=  1\n}\n```\n\n\n\nThanks   a lot.\n"
	want := "Please fix this:\n```go\nfunc  main() {\n\t\tx   :=  1\n}\n```\n\nThanks a lot."

	got := newDefault().Optimize(in).Text
	if got != want {
		t.Fatalf("code block was modified\n got: %q\nwant: %q", got, want)
	}
}

// Secrets inside code fences must still be redacted even though the
// formatting is left alone.
func TestRedactsSecretsInsideCodeFence(t *testing.T) {
	in := "```\nAPI_KEY=AKIAIOSFODNN7EXAMPLE\n```"
	res := newDefault().Optimize(in)
	if strings.Contains(res.Text, "AKIA") {
		t.Fatalf("secret leaked: %q", res.Text)
	}
	if res.Redactions[KindSecret] != 1 {
		t.Errorf("want 1 secret redaction, got %v", res.Redactions)
	}
}

// Test 3: sanitization of sensitive text.
func TestRedactsSensitiveData(t *testing.T) {
	in := strings.Join([]string{
		"Contact jane.doe@example.com or call +91 98765 43210 / (555) 123-4567.",
		"Card 4111 1111 1111 1111 and SSN 123-45-6789.",
		"Key sk-proj-abcdefghijklmnopqrstuvwxyz123456 and AKIAIOSFODNN7EXAMPLE.",
		"Token ghp_abcdefghijklmnopqrstuvwxyz0123456789 end.",
		"Header: Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789",
	}, "\n")

	res := newDefault().Optimize(in)

	for _, leak := range []string{
		"jane.doe@example.com", "98765", "123-4567", "4111", "123-45-6789",
		"sk-proj", "AKIAIOSFODNN7EXAMPLE", "ghp_", "abcdefghijklmnopqrstuvwxyz0123456789",
	} {
		if strings.Contains(res.Text, leak) {
			t.Errorf("%q was not redacted:\n%s", leak, res.Text)
		}
	}
	want := map[string]int{
		KindEmail: 1, KindPhone: 2, KindCreditCard: 1, KindSSN: 1, KindSecret: 4,
	}
	for kind, n := range want {
		if res.Redactions[kind] != n {
			t.Errorf("redactions[%s] = %d, want %d (all: %v)", kind, res.Redactions[kind], n, res.Redactions)
		}
	}
	if res.RedactionTotal() != 9 {
		t.Errorf("RedactionTotal = %d, want 9", res.RedactionTotal())
	}
}

// Harmless digit strings must survive: checksums prevent false positives.
func TestDoesNotRedactHarmlessNumbers(t *testing.T) {
	in := "Order 4111 1111 1111 1112 shipped on 2026-11-07, invoice 1234567890, version 1.2.3."
	res := newDefault().Optimize(in)
	if res.Text != in {
		t.Fatalf("harmless text modified:\n got: %q\nwant: %q", res.Text, in)
	}
	if res.RedactionTotal() != 0 {
		t.Errorf("unexpected redactions: %v", res.Redactions)
	}
}

func TestAadhaarUsesVerhoeffChecksum(t *testing.T) {
	// Build a valid 12-digit number by brute-forcing the final check digit.
	base := "23456789012"
	valid := ""
	for d := 0; d < 10; d++ {
		if c := fmt.Sprintf("%s%d", base, d); verhoeffValid(c) {
			valid = c
			break
		}
	}
	if valid == "" {
		t.Fatal("no valid check digit found")
	}
	spaced := valid[0:4] + " " + valid[4:8] + " " + valid[8:12]

	res := newDefault().Optimize("My Aadhaar is " + spaced + ".")
	if strings.Contains(res.Text, spaced) || res.Redactions[KindAadhaar] != 1 {
		t.Errorf("valid Aadhaar not redacted: %q %v", res.Text, res.Redactions)
	}

	// Change the last digit so the checksum fails.
	bad := valid[:11] + fmt.Sprintf("%d", (int(valid[11]-'0')+1)%10)
	badSpaced := bad[0:4] + " " + bad[4:8] + " " + bad[8:12]
	res = newDefault().Optimize("Ticket " + badSpaced + ".")
	if res.Redactions[KindAadhaar] != 0 {
		t.Errorf("invalid Aadhaar should be kept: %q", res.Text)
	}
}

func TestVerhoeffKnownVector(t *testing.T) {
	// 236 + check digit 3 is the classic Verhoeff example.
	if !verhoeffValid("2363") || verhoeffValid("2364") {
		t.Fatal("Verhoeff tables are wrong")
	}
	if !luhnValid("4111111111111111") || luhnValid("4111111111111112") {
		t.Fatal("Luhn implementation is wrong")
	}
}

// Test 4: edge cases.
func TestEdgeCases(t *testing.T) {
	o := newDefault()

	t.Run("empty", func(t *testing.T) {
		res := o.Optimize("")
		if res.Text != "" || res.Changed || res.TokensBefore != 0 {
			t.Errorf("unexpected result for empty input: %+v", res)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		if got := o.Optimize("   \n\n\t  \n").Text; got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("already optimal is unchanged", func(t *testing.T) {
		in := "Summarize the report in three bullet points."
		res := o.Optimize(in)
		if res.Changed || res.Text != in {
			t.Errorf("clean prompt was modified: %+v", res)
		}
	})

	t.Run("windows line endings", func(t *testing.T) {
		if got := o.Optimize("a\r\nb\r\n").Text; got != "a\nb" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("unclosed fence treats the rest as code", func(t *testing.T) {
		in := "Intro\n```\nx   =   1\n\n\n\ny = 2"
		if got := o.Optimize(in).Text; got != in {
			t.Errorf("unclosed fence content changed: %q", got)
		}
	})

	t.Run("nested list indentation is kept but capped", func(t *testing.T) {
		in := "- item\n    - nested\n" + strings.Repeat(" ", 40) + "- very deep"
		want := "- item\n    - nested\n" + strings.Repeat(" ", maxIndent) + "- very deep"
		if got := o.Optimize(in).Text; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("disabled config is a no-op", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Enabled = false
		in := "  messy    text  with a@b.com  "
		res := New(cfg).Optimize(in)
		if res.Text != in || res.Changed {
			t.Errorf("disabled optimizer modified text: %+v", res)
		}
	})

	t.Run("large input finishes quickly", func(t *testing.T) {
		in := strings.Repeat("word    word  \n\n\n", 50000)
		start := time.Now()
		res := o.Optimize(in)
		if time.Since(start) > 30*time.Second {
			t.Errorf("took too long: %v", time.Since(start))
		}
		if res.TokensAfter >= res.TokensBefore {
			t.Error("expected savings on large input")
		}
	})
}

func TestStripInvisibleKeepsLanguageJoiners(t *testing.T) {
	o := newDefault()

	if got := o.Optimize("a​b‮c\u0000d⁦e").Text; got != "abcde" {
		t.Errorf("hidden characters survived: %q", got)
	}

	persian := "می‌خواهم" // ZWNJ is required for correct Persian
	hindi := "क्‍ष"       // ZWJ is used in Indic conjuncts
	emoji := "👨‍👩‍👧"      // ZWJ builds family emoji
	for _, s := range []string{persian, hindi, emoji} {
		if got := o.Optimize(s).Text; got != s {
			t.Errorf("joiner text corrupted: %q -> %q", s, got)
		}
	}

	res := o.Optimize("x​y")
	if res.Redactions[KindHiddenChars] != 1 {
		t.Errorf("hidden char count = %d, want 1", res.Redactions[KindHiddenChars])
	}
}

func TestPhraseCompressionKeepsCapitalization(t *testing.T) {
	in := "In order to win, we must act at this point in time because of a large number of reasons."
	want := "To win, we must act now because of many reasons."
	if got := newDefault().Optimize(in).Text; got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

func TestDedupeOnlyConsecutiveLongLines(t *testing.T) {
	long := "Always answer in formal English please."
	in := strings.Join([]string{long, long, long, "---", "---", long}, "\n")
	want := strings.Join([]string{long, "---", "---", long}, "\n")
	if got := newDefault().Optimize(in).Text; got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

func TestOptimizeIsIdempotent(t *testing.T) {
	o := newDefault()
	in := "  Hi   there ​!\n\n\n\nIn order to help,   email a@b.com\n```\n  keep   this\n```\n" +
		strings.Repeat("A repeated instruction line here.\n", 3)
	once := o.Optimize(in).Text
	twice := o.Optimize(once).Text
	if once != twice {
		t.Errorf("not idempotent\n once: %q\ntwice: %q", once, twice)
	}
}

func TestConfigFlagsAreIndependent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RedactEmails = false
	cfg.CollapseWhitespace = false
	cfg.CompressPhrases = false
	cfg.DedupeLines = false
	in := "mail   a@b.com"
	if got := New(cfg).Optimize(in).Text; got != in {
		t.Errorf("all features off should leave text alone, got %q", got)
	}
}

func TestRoleSelection(t *testing.T) {
	o := newDefault()
	for role, want := range map[string]bool{
		"user": true, "SYSTEM": true, " user ": true,
		"assistant": false, "tool": false, "": false,
	} {
		if got := o.AppliesToRole(role); got != want {
			t.Errorf("AppliesToRole(%q) = %v, want %v", role, got, want)
		}
	}
	cfg := DefaultConfig()
	cfg.Enabled = false
	if New(cfg).AppliesToRole("user") {
		t.Error("disabled optimizer must not apply to any role")
	}
}

func TestResultAdd(t *testing.T) {
	var total Result
	total.Add(Result{TokensBefore: 10, TokensAfter: 6, Changed: true, Redactions: map[string]int{KindEmail: 1}})
	total.Add(Result{TokensBefore: 5, TokensAfter: 5, Redactions: map[string]int{KindEmail: 2, KindSecret: 1}})
	if total.TokensBefore != 15 || total.TokensAfter != 11 || !total.Changed {
		t.Errorf("bad totals: %+v", total)
	}
	if total.Redactions[KindEmail] != 3 || total.Redactions[KindSecret] != 1 {
		t.Errorf("bad redaction totals: %v", total.Redactions)
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Error("empty string should be 0 tokens")
	}
	if EstimateTokens("abcd") != 1 || EstimateTokens("abcde") != 2 {
		t.Error("ASCII estimate should be ceil(chars/4)")
	}
	if EstimateTokens("नमस्ते") <= 0 {
		t.Error("non-ASCII text should cost tokens")
	}
	if EstimateTokens("a b") >= EstimateTokens("a      b") {
		t.Error("more whitespace must not estimate fewer tokens")
	}
}

func TestConfigFromMap(t *testing.T) {
	t.Run("nil gives defaults", func(t *testing.T) {
		cfg, err := ConfigFromMap(nil)
		if err != nil || !cfg.Enabled || cfg.MaxBlankLines != 1 {
			t.Fatalf("bad defaults: %+v %v", cfg, err)
		}
	})

	t.Run("valid overrides", func(t *testing.T) {
		cfg, err := ConfigFromMap(map[string]any{
			"max_blank_lines": float64(0),
			"roles":           []any{"user"},
			"redact_emails":   false,
			"stats_addr":      "127.0.0.1:9999", // plugin-level key must be ignored
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxBlankLines != 0 || cfg.RedactEmails || len(cfg.Roles) != 1 || cfg.Roles[0] != "user" {
			t.Errorf("overrides not applied: %+v", cfg)
		}
	})

	bad := []map[string]any{
		{"enabled": "yes"},
		{"max_blank_lines": -1.0},
		{"max_blank_lines": 1.5},
		{"max_blank_lines": "2"},
		{"roles": "user"},
		{"roles": []any{"user", 5}},
	}
	for i, m := range bad {
		if _, err := ConfigFromMap(m); err == nil {
			t.Errorf("case %d (%v): expected an error", i, m)
		}
	}
}

// Fuzzing: the optimizer must never panic and must keep valid UTF-8 valid.
// `go test` runs the seed corpus; `go test -fuzz=FuzzOptimize` explores more.
func FuzzOptimize(f *testing.F) {
	for _, s := range []string{
		"", " ", "\n\n\n", "a@b.co", "```", "```\ncode\n```", "4111 1111 1111 1111",
		"+91 98765 43210", "​‮", "In order to", "\t\t- x\n\t\t\t- y",
	} {
		f.Add(s)
	}
	o := newDefault()
	f.Fuzz(func(t *testing.T, s string) {
		res := o.Optimize(s)
		if utf8.ValidString(s) && !utf8.ValidString(res.Text) {
			t.Fatalf("produced invalid UTF-8 from %q", s)
		}
		if res.TokensBefore < 0 || res.TokensAfter < 0 {
			t.Fatal("negative token estimate")
		}
	})
}
