package backend

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The expected values come from the reference implementation of the current
// sentinel proof of work (FNV-1a 32 bit plus an avalanche step).
func TestFnv1a32MatchesReferenceVectors(t *testing.T) {
	cases := map[string]string{
		"":                                          "ab3e7c0b",
		"hello":                                     "888d766e",
		"0fffff":                                    "7552f184",
		"abc123":                                    "9d1f3ec1",
		"seedpayload":                               "769860aa",
		"gAAAAACeyJhIjoxfQ==":                       "20accb18",
		"The quick brown fox jumps over the lazy dog": "cc6bf03d",
	}
	for input, want := range cases {
		if got := fnv1a32(input); got != want {
			t.Fatalf("fnv1a32(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildPOWConfigHas25Fields(t *testing.T) {
	config := buildPOWConfig("Mozilla/5.0 (X11; Linux x86_64) Test", []string{defaultPOWScript}, "build-1", defaultPowTimezone())
	if len(config) != 25 {
		t.Fatalf("buildPOWConfig() has %d fields, want 25", len(config))
	}
	if got, ok := config[3].(int); !ok || got != 1 {
		t.Fatalf("config[3] = %#v, want 1", config[3])
	}
	if got, ok := config[4].(string); !ok || got != "Mozilla/5.0 (X11; Linux x86_64) Test" {
		t.Fatalf("config[4] = %#v, want the solver user agent", config[4])
	}
	if _, ok := config[9].(float64); !ok {
		t.Fatalf("config[9] = %#v, want a float", config[9])
	}
}

func TestBuildRequirementsTokenIsBase64JSON(t *testing.T) {
	token := buildRequirementsToken("ua-1", []string{defaultPOWScript}, "build-1", defaultPowTimezone())
	if !strings.HasPrefix(token, "gAAAAAC") {
		t.Fatalf("requirements token prefix = %q, want gAAAAAC", token[:min(7, len(token))])
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "gAAAAAC"))
	if err != nil {
		t.Fatalf("decode requirements token: %v", err)
	}
	var config []any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal requirements token: %v", err)
	}
	if len(config) != 25 {
		t.Fatalf("requirements token carries %d fields, want 25", len(config))
	}
}

// TestBuildProofTokenSatisfiesDifficulty checks the emitted proof token is
// self-consistent: it must carry the "~S" suffix and its hash must clear the
// difficulty the server asked for.
func TestBuildProofTokenSatisfiesDifficulty(t *testing.T) {
	const seed = "seed"
	const difficulty = "0fffff"
	requirementsToken := buildRequirementsToken("ua-1", []string{defaultPOWScript}, "build-1", defaultPowTimezone())
	token, err := buildProofToken(seed, difficulty, requirementsToken)
	if err != nil {
		t.Fatalf("buildProofToken() error = %v", err)
	}
	if !strings.HasPrefix(token, "gAAAAAB") {
		t.Fatalf("proof token prefix = %q, want gAAAAAB", token[:min(7, len(token))])
	}
	if !strings.HasSuffix(token, "~S") {
		t.Fatalf("proof token %q does not end with ~S", token)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(token, "gAAAAAB"), "~S")
	if !powSatisfies(fnv1a32(seed+payload), difficulty) {
		t.Fatalf("proof token payload does not satisfy difficulty %q", difficulty)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode proof payload: %v", err)
	}
	var config []any
	if err := json.Unmarshal(decoded, &config); err != nil {
		t.Fatalf("unmarshal proof payload: %v", err)
	}
	if len(config) != 25 {
		t.Fatalf("proof payload carries %d fields, want 25", len(config))
	}
	if nonce, ok := config[3].(float64); !ok || nonce < 0 {
		t.Fatalf("config[3] = %#v, want a nonce >= 0", config[3])
	}
}

func TestPowTimezoneFormatLocalTime(t *testing.T) {
	moment := time.Date(2026, 10, 15, 12, 34, 56, 0, time.UTC)
	cases := []struct {
		name   string
		tz     powTimezone
		suffix string
	}{
		{
			name:   "phoenix",
			tz:     powTimezone{Name: "America/Phoenix", OffsetMin: 420, LongName: "Mountain Standard Time"},
			suffix: "Oct 15 2026 05:34:56 GMT-0700 (Mountain Standard Time)",
		},
		{
			name:   "shanghai default",
			tz:     defaultPowTimezone(),
			suffix: "Oct 15 2026 20:34:56 GMT+0800 (China Standard Time)",
		},
	}
	for _, tc := range cases {
		got := tc.tz.formatLocalTime(moment)
		if !strings.HasSuffix(got, tc.suffix) {
			t.Fatalf("%s: formatLocalTime() = %q, want suffix %q", tc.name, got, tc.suffix)
		}
	}
}

func TestPowSatisfiesRejectsHighHash(t *testing.T) {
	if powSatisfies("ffffffff", "0fffff") {
		t.Fatal("powSatisfies() accepted a hash that does not clear the difficulty")
	}
	if !powSatisfies("0abcde", "0fffff") {
		t.Fatal("powSatisfies() rejected a hash that clears the difficulty")
	}
	if !powSatisfies("deadbeef", "") {
		t.Fatal("powSatisfies() rejected an empty difficulty")
	}
}
