package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The Admin API accepts a duration in either the ISO-8601 form (`PT1H`) or the friendly form the
// jiff crate uses (`1 hour`, `1h`, `90 minutes`), and it does not promise which form it echoes back.
// Comparing those as plain strings would produce a permanent diff whenever the server normalized a
// value differently from how it was written.
//
// durationType therefore compares durations by what they mean, not by how they are spelled, using
// the framework's semantic-equality hook. State keeps whatever the server returned, and a plan is
// empty as long as the configured and remote values denote the same length of time.
//
// Where a value cannot be parsed -- notably a calendar duration such as `P1M`, whose length depends
// on which month it is -- the comparison falls back to exact string equality. That is the
// conservative choice: reporting a diff the user can see and resolve beats silently treating two
// different durations as the same.

// durationType is the attribute type for a Restate duration.
type durationType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = durationType{}

func (t durationType) Equal(other attr.Type) bool {
	_, ok := other.(durationType)
	return ok
}

func (t durationType) String() string { return "provider.durationType" }

func (t durationType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return durationValue{StringValue: in}, nil
}

func (t durationType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	value, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringValue, ok := value.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", value)
	}
	return durationValue{StringValue: stringValue}, nil
}

func (t durationType) ValueType(context.Context) attr.Value {
	return durationValue{}
}

// durationValue is a duration attribute value that compares semantically.
type durationValue struct {
	basetypes.StringValue
}

var _ basetypes.StringValuableWithSemanticEquals = durationValue{}

func (v durationValue) Type(context.Context) attr.Type { return durationType{} }

func (v durationValue) Equal(other attr.Value) bool {
	otherDuration, ok := other.(durationValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(otherDuration.StringValue)
}

// StringSemanticEquals reports whether two spellings denote the same duration.
func (v durationValue) StringSemanticEquals(_ context.Context, other basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	otherDuration, ok := other.(durationValue)
	if !ok {
		return false, diags
	}
	if v.IsNull() || v.IsUnknown() || otherDuration.IsNull() || otherDuration.IsUnknown() {
		// Null and unknown are the framework's business, not this comparison's.
		return v.StringValue.Equal(otherDuration.StringValue), diags
	}

	mine, myErr := ParseDuration(v.ValueString())
	theirs, theirErr := ParseDuration(otherDuration.ValueString())
	if myErr != nil || theirErr != nil {
		// One of them is a form this parser does not understand -- a calendar duration, say. Exact
		// equality is the honest fallback: a visible diff beats a wrong "these match".
		return v.ValueString() == otherDuration.ValueString(), diags
	}
	return mine == theirs, diags
}

// durationValueOf builds a duration value from a string, keeping an empty string null so an
// unconfigured optional attribute does not diff against "".
func durationValueOf(s string) durationValue {
	if s == "" {
		return durationValue{StringValue: basetypes.NewStringNull()}
	}
	return durationValue{StringValue: basetypes.NewStringValue(s)}
}

// isoDurationPattern matches the ISO-8601 durations that have a fixed length. Years and months are
// deliberately excluded: their length depends on the calendar, so they cannot be reduced to a
// time.Duration and are left to exact string comparison.
var isoDurationPattern = regexp.MustCompile(
	`^(?i)P(?:(\d+(?:[.,]\d+)?)W)?(?:(\d+(?:[.,]\d+)?)D)?` +
		`(?:T(?:(\d+(?:[.,]\d+)?)H)?(?:(\d+(?:[.,]\d+)?)M)?(?:(\d+(?:[.,]\d+)?)S)?)?$`)

// friendlyUnits maps every unit spelling the friendly format accepts onto its length. The plural and
// abbreviated forms are all listed rather than stemmed, so an unrecognised unit fails loudly instead
// of being coerced into a plausible-looking wrong answer.
var friendlyUnits = map[string]time.Duration{
	"ns": time.Nanosecond, "nsec": time.Nanosecond, "nsecs": time.Nanosecond,
	"nanosecond": time.Nanosecond, "nanoseconds": time.Nanosecond,

	"us": time.Microsecond, "µs": time.Microsecond, "usec": time.Microsecond, "usecs": time.Microsecond,
	"microsecond": time.Microsecond, "microseconds": time.Microsecond,

	"ms": time.Millisecond, "msec": time.Millisecond, "msecs": time.Millisecond,
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,

	"s": time.Second, "sec": time.Second, "secs": time.Second,
	"second": time.Second, "seconds": time.Second,

	"m": time.Minute, "min": time.Minute, "mins": time.Minute,
	"minute": time.Minute, "minutes": time.Minute,

	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour,
	"hour": time.Hour, "hours": time.Hour,

	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,

	"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
}

// friendlyComponentPattern matches one number-and-unit pair, with optional whitespace between them.
var friendlyComponentPattern = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*([a-zµ]+)`)

// ParseDuration parses the duration forms the Restate Admin API accepts and reduces them to a fixed
// length.
//
// It handles the ISO-8601 form with weeks, days, hours, minutes and seconds, and the friendly form
// as a sequence of number-and-unit components. It deliberately rejects ISO calendar components
// (years and months), because their length is not fixed and pretending otherwise would let the
// provider declare two genuinely different durations equal.
func ParseDuration(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("a duration cannot be empty")
	}

	if strings.HasPrefix(strings.ToUpper(trimmed), "P") {
		return parseISODuration(trimmed)
	}
	return parseFriendlyDuration(trimmed)
}

func parseISODuration(raw string) (time.Duration, error) {
	upper := strings.ToUpper(raw)
	// Reject calendar components explicitly, so the caller learns why rather than getting a
	// misleading parse. A trailing M inside the time part is minutes, not months, so only look
	// before the T.
	datePart := upper
	if index := strings.Index(upper, "T"); index >= 0 {
		datePart = upper[:index]
	}
	if strings.ContainsAny(datePart[1:], "Y") || strings.Contains(datePart[1:], "M") {
		return 0, fmt.Errorf("duration %q uses a calendar component (years or months), whose length is not fixed and cannot be compared numerically", raw)
	}

	match := isoDurationPattern.FindStringSubmatch(raw)
	if match == nil {
		return 0, fmt.Errorf("duration %q is not a recognised ISO-8601 duration", raw)
	}

	units := []time.Duration{7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second}
	var total time.Duration
	matched := false
	for i, unit := range units {
		field := match[i+1]
		if field == "" {
			continue
		}
		matched = true
		value, err := strconv.ParseFloat(strings.Replace(field, ",", ".", 1), 64)
		if err != nil {
			return 0, fmt.Errorf("duration %q has an unparseable component %q: %w", raw, field, err)
		}
		total += time.Duration(value * float64(unit))
	}
	if !matched {
		return 0, fmt.Errorf("duration %q has no components", raw)
	}
	return total, nil
}

func parseFriendlyDuration(raw string) (time.Duration, error) {
	remaining := raw
	var total time.Duration
	matched := false

	for {
		remaining = strings.TrimLeft(remaining, " \t,")
		if remaining == "" {
			break
		}

		match := friendlyComponentPattern.FindStringSubmatch(remaining)
		if match == nil {
			return 0, fmt.Errorf("duration %q could not be parsed at %q; expected a number followed by a unit such as `30s`, `5 minutes`, or `1 day`", raw, remaining)
		}

		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, fmt.Errorf("duration %q has an unparseable number %q: %w", raw, match[1], err)
		}
		unit, ok := friendlyUnits[strings.ToLower(match[2])]
		if !ok {
			return 0, fmt.Errorf("duration %q uses an unrecognised unit %q", raw, match[2])
		}

		total += time.Duration(value * float64(unit))
		matched = true
		remaining = remaining[len(match[0]):]
	}

	if !matched {
		return 0, fmt.Errorf("duration %q has no components", raw)
	}
	return total, nil
}
