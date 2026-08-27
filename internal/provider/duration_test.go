package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func TestParseDurationFriendlyForm(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"30 s", 30 * time.Second},
		{"30 seconds", 30 * time.Second},
		{"30 secs", 30 * time.Second},
		{"1m", time.Minute},
		{"1 minute", time.Minute},
		{"90 minutes", 90 * time.Minute},
		{"1h", time.Hour},
		{"5 hours", 5 * time.Hour},
		{"1day", 24 * time.Hour},
		{"7days", 7 * 24 * time.Hour},
		{"1 week", 7 * 24 * time.Hour},
		{"50ms", 50 * time.Millisecond},
		{"100us", 100 * time.Microsecond},
		{"100µs", 100 * time.Microsecond},
		{"5ns", 5 * time.Nanosecond},
		// Compound values, which the friendly format accepts in several separator styles.
		{"1h30m", 90 * time.Minute},
		{"1h 30m", 90 * time.Minute},
		{"1 hour 30 minutes", 90 * time.Minute},
		{"1h, 30m", 90 * time.Minute},
		{"1d 12h", 36 * time.Hour},
		// Fractions.
		{"1.5h", 90 * time.Minute},
		{"0.5 days", 12 * time.Hour},
		// Case is not significant.
		{"1H", time.Hour},
		{"5 HOURS", 5 * time.Hour},
		// Surrounding whitespace.
		{"  1h  ", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseDurationISOForm(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"PT30S", 30 * time.Second},
		{"PT1M", time.Minute},
		{"PT1H", time.Hour},
		{"PT1H30M", 90 * time.Minute},
		{"P1D", 24 * time.Hour},
		{"P7D", 7 * 24 * time.Hour},
		{"P1W", 7 * 24 * time.Hour},
		{"P1DT12H", 36 * time.Hour},
		{"PT0.5H", 30 * time.Minute},
		{"PT0,5H", 30 * time.Minute},
		// Lowercase is accepted by the parser; the API's own tolerance is its business.
		{"pt1h", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A calendar duration's length depends on which month or year it falls in, so reducing it to a fixed
// number would let the provider declare two genuinely different durations equal.
func TestParseDurationRejectsCalendarComponents(t *testing.T) {
	for _, in := range []string{"P1M", "P1Y", "P1Y2M", "P1M15D"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseDuration(in); err == nil {
				t.Fatalf("ParseDuration(%q) was accepted; a calendar component has no fixed length", in)
			}
		})
	}
}

func TestParseDurationRejectsUnusableValues(t *testing.T) {
	for _, in := range []string{
		"", "   ",
		"soon",
		"30",            // no unit
		"h",             // no number
		"30 fortnights", // unrecognised unit
		"P",             // no components
		"PT",            // no components
		"30s extra",     // trailing garbage
	} {
		t.Run(in, func(t *testing.T) {
			if got, err := ParseDuration(in); err == nil {
				t.Fatalf("ParseDuration(%q) = %v, want an error", in, got)
			}
		})
	}
}

// This is the property the whole type exists for: the server may echo a duration back in a different
// spelling than it was configured with, and that must not be a permanent diff.
func TestDurationSemanticEqualityAcrossSpellings(t *testing.T) {
	equivalent := [][2]string{
		{"1h", "PT1H"},
		{"1h", "60m"},
		{"1h", "3600s"},
		{"1day", "P1D"},
		{"1day", "24h"},
		{"7days", "P7D"},
		{"7days", "1 week"},
		{"90 minutes", "PT1H30M"},
		{"1 hour", "1h"},
		{"50ms", "0.05s"},
	}
	for _, pair := range equivalent {
		t.Run(pair[0]+" == "+pair[1], func(t *testing.T) {
			assertSemanticEquality(t, pair[0], pair[1], true)
		})
	}
}

func TestDurationSemanticEqualityDistinguishesDifferentLengths(t *testing.T) {
	different := [][2]string{
		{"1h", "PT2H"},
		{"1day", "P2D"},
		{"1h", "1m"},
		{"59m", "1h1s"},
	}
	for _, pair := range different {
		t.Run(pair[0]+" != "+pair[1], func(t *testing.T) {
			assertSemanticEquality(t, pair[0], pair[1], false)
		})
	}
}

// When a value cannot be parsed, exact equality is the fallback. A visible diff the user can resolve
// beats a wrong claim that two values match.
func TestDurationSemanticEqualityFallsBackToExactComparison(t *testing.T) {
	assertSemanticEquality(t, "P1M", "P1M", true)
	assertSemanticEquality(t, "P1M", "P30D", false)
	assertSemanticEquality(t, "whatever", "whatever", true)
	assertSemanticEquality(t, "whatever", "something else", false)
}

func TestDurationSemanticEqualityHandlesNullAndUnknown(t *testing.T) {
	ctx := context.Background()
	null := durationValue{StringValue: basetypes.NewStringNull()}
	unknown := durationValue{StringValue: basetypes.NewStringUnknown()}
	known := durationValueOf("1h")

	for _, tt := range []struct {
		name  string
		left  durationValue
		right durationValue
		want  bool
	}{
		{"null and null", null, null, true},
		{"null and known", null, known, false},
		{"unknown and unknown", unknown, unknown, true},
		{"unknown and known", unknown, known, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, diags := tt.left.StringSemanticEquals(ctx, tt.right)
			if diags.HasError() {
				t.Fatalf("diagnostics: %v", diags)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A comparison against a value of some other type must not claim equality.
func TestDurationSemanticEqualityRejectsAForeignType(t *testing.T) {
	got, diags := durationValueOf("1h").StringSemanticEquals(context.Background(), basetypes.NewStringValue("1h"))
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
	if got {
		t.Error("a plain string value was treated as semantically equal to a duration value")
	}
}

func TestDurationValueOfKeepsAnEmptyStringNull(t *testing.T) {
	if !durationValueOf("").IsNull() {
		t.Error("an empty duration became a value rather than staying null")
	}
	if durationValueOf("1h").IsNull() {
		t.Error("a configured duration became null")
	}
}

func TestDurationTypeIdentity(t *testing.T) {
	if !(durationType{}).Equal(durationType{}) {
		t.Error("durationType is not equal to itself")
	}
	if (durationType{}).Equal(basetypes.StringType{}) {
		t.Error("durationType claims equality with a plain string type")
	}
	if got := (durationValue{}).Type(context.Background()); !got.Equal(durationType{}) {
		t.Errorf("durationValue reports type %v", got)
	}
}

func assertSemanticEquality(t *testing.T, left, right string, want bool) {
	t.Helper()
	got, diags := durationValueOf(left).StringSemanticEquals(context.Background(), durationValueOf(right))
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
	if got != want {
		t.Errorf("%q vs %q: got %v, want %v", left, right, got, want)
	}
}
