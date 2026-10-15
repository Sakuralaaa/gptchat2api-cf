package backend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"chatgpt2api/internal/util"
)

const (
	defaultPOWScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

	// The conversation endpoints declare the client timezone; it has to agree
	// with the geolocation of the outbound proxy.
	defaultConversationTimezone          = "Asia/Shanghai"
	defaultConversationTimezoneOffsetMin = -480

	powMaxAttempts = 500000
)

var (
	scriptSrcRE = regexp.MustCompile(`(?is)<script[^>]+src=["']([^"']+)["']`)
	dataBuildRE = regexp.MustCompile(`c/[^/]*/_`)
	htmlBuildRE = regexp.MustCompile(`(?is)<html[^>]*data-build=["']([^"']*)["']`)
	difficultySanitizeRE = regexp.MustCompile(`[^0-9a-fA-F]`)
)

func parsePOWResources(html string) ([]string, string) {
	matches := scriptSrcRE.FindAllStringSubmatch(html, -1)
	sources := make([]string, 0, len(matches))
	dataBuild := ""
	for _, match := range matches {
		src := match[1]
		sources = append(sources, src)
		if dataBuild == "" {
			dataBuild = dataBuildRE.FindString(src)
		}
	}
	if len(sources) == 0 {
		sources = []string{defaultPOWScript}
	}
	if dataBuild == "" {
		if match := htmlBuildRE.FindStringSubmatch(html); len(match) > 1 {
			dataBuild = match[1]
		}
	}
	return sources, dataBuild
}

// powTimezone describes the timezone of the environment that solves the
// sentinel challenge. The challenge programs themselves do not read the clock,
// but the token payloads embed a Date.prototype.toString() stamp and OpenAI
// correlates it with the request's exit IP, so it has to match the solver page.
type powTimezone struct {
	Name      string
	OffsetMin int    // JS getTimezoneOffset(): minutes to add to local time to get UTC.
	LongName  string // e.g. "Mountain Standard Time".
}

func defaultPowTimezone() powTimezone {
	return powTimezone{
		Name:      defaultConversationTimezone,
		OffsetMin: defaultConversationTimezoneOffsetMin,
		LongName:  "China Standard Time",
	}
}

func (z powTimezone) fixedZone() *time.Location {
	name := z.LongName
	if name == "" {
		name = z.Name
	}
	if name == "" {
		name = "UTC"
	}
	return time.FixedZone(name, -z.OffsetMin*60)
}

// formatLocalTime renders the Date.prototype.toString() style stamp embedded in
// sentinel tokens, e.g. "Mon Oct 15 2026 12:34:56 GMT-0700 (Mountain Standard Time)".
func (z powTimezone) formatLocalTime(now time.Time) string {
	offsetSeconds := -z.OffsetMin * 60
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	minutes := offsetSeconds / 60
	location := z.fixedZone()
	return fmt.Sprintf("%s GMT%s%02d%02d (%s)",
		now.In(location).Format("Mon Jan 02 2006 15:04:05"), sign, minutes/60, minutes%60, location.String())
}

var (
	powScreenResolutions = [][]int{{1920, 1080}, {1440, 900}, {2560, 1440}, {3840, 2160}}
	powCores             = []int{8, 16, 24, 32}

	powNavigatorKeys = []string{
		"registerProtocolHandler−function registerProtocolHandler() { [native code] }",
		"storage−[object StorageManager]", "locks−[object LockManager]", "appCodeName−Mozilla",
		"permissions−[object Permissions]", "share−function share() { [native code] }", "webdriver−false",
		"managed−[object NavigatorManagedData]", "canShare−function canShare() { [native code] }",
		"vendor−Google Inc.", "mediaDevices−[object MediaDevices]", "vibrate−function vibrate() { [native code] }",
		"storageBuckets−[object StorageBucketManager]", "mediaCapabilities−[object MediaCapabilities]",
		"cookieEnabled−true", "virtualKeyboard−[object VirtualKeyboard]", "product−Gecko",
		"presentation−[object Presentation]", "onLine−true", "mimeTypes−[object MimeTypeArray]",
		"credentials−[object CredentialsContainer]", "serviceWorker−[object ServiceWorkerContainer]",
		"keyboard−[object Keyboard]", "gpu−[object GPU]", "doNotTrack", "serial−[object Serial]",
		"pdfViewerEnabled−true", "language−zh-CN", "geolocation−[object Geolocation]",
		"userAgentData−[object NavigatorUAData]", "getUserMedia−function getUserMedia() { [native code] }",
		"sendBeacon−function sendBeacon() { [native code] }", "hardwareConcurrency−32",
		"windowControlsOverlay−[object WindowControlsOverlay]",
	}
	powWindowKeys = []string{
		"0", "window", "self", "document", "name", "location", "customElements", "history", "navigation",
		"innerWidth", "innerHeight", "scrollX", "scrollY", "visualViewport", "screenX", "screenY", "outerWidth",
		"outerHeight", "devicePixelRatio", "screen", "chrome", "navigator", "onresize", "performance", "crypto",
		"indexedDB", "sessionStorage", "localStorage", "scheduler", "alert", "atob", "btoa", "fetch", "matchMedia",
		"postMessage", "queueMicrotask", "requestAnimationFrame", "setInterval", "setTimeout", "caches",
		"__NEXT_DATA__", "__BUILD_MANIFEST", "__NEXT_PRELOADREADY",
	}
	powDocumentKeys = []string{"_reactListeningo743lnnpvdg", "location"}
)

// buildPOWConfig builds the 25 field payload the current challenge program
// expects. Indices 3 and 9 are the proof-of-work nonce and are overwritten
// while solving; everything else is a fingerprint sample.
func buildPOWConfig(userAgent string, scriptSources []string, dataBuild string, tz powTimezone) []any {
	if len(scriptSources) == 0 {
		scriptSources = []string{defaultPOWScript}
	}
	perfNow := rand.Float64()*49000 + 1000
	screen := powScreenResolutions[rand.Intn(len(powScreenResolutions))]
	return []any{
		screen[0] + screen[1],
		tz.formatLocalTime(time.Now()),
		4294705152,
		1,
		userAgent,
		randomChoice(scriptSources),
		dataBuild,
		"en-US",
		"en-US,es-US,en,es",
		rand.Float64(),
		randomChoice(powNavigatorKeys),
		randomChoice(powDocumentKeys),
		randomChoice(powWindowKeys),
		perfNow,
		util.NewUUID(),
		"",
		randomChoiceInt(powCores),
		float64(time.Now().UnixNano())/1e6 - perfNow,
		0, 0, 0, 0, 0, 0, 0,
	}
}

func encodePOWPayload(config []any) string {
	data, _ := json.Marshal(config)
	return base64.StdEncoding.EncodeToString(data)
}

// buildRequirementsToken builds the "requirements token" submitted as `p` when
// opening the sentinel chat-requirements handshake. The challenge program is
// XOR encrypted with this value, so the solver must receive the same string.
func buildRequirementsToken(userAgent string, scriptSources []string, dataBuild string, tz powTimezone) string {
	return "gAAAAAC" + encodePOWPayload(buildPOWConfig(userAgent, scriptSources, dataBuild, tz))
}

// buildProofToken solves the sentinel proof of work. The current challenge
// program uses FNV-1a 32 bit plus an avalanche step and expects the "~S"
// suffix. The 25 field payload is reused from the requirements token that was
// submitted as `p`, so both carry the same fingerprint sample.
func buildProofToken(seed, difficulty, requirementsToken string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(requirementsToken, "gAAAAAC"))
	if err != nil {
		return "", fmt.Errorf("decode requirements token: %w", err)
	}
	var config []any
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", fmt.Errorf("parse requirements token: %w", err)
	}
	if len(config) != 25 {
		return "", fmt.Errorf("requirements token has %d fields, want 25", len(config))
	}
	difficulty = sanitizeDifficulty(difficulty)
	for i := 0; i < powMaxAttempts; i++ {
		config[3] = i
		config[9] = i >> 1
		payload := encodePOWPayload(config)
		if powSatisfies(fnv1a32(seed+payload), difficulty) {
			return "gAAAAAB" + payload + "~S", nil
		}
	}
	return "", fmt.Errorf("failed to solve proof token: difficulty=%s", difficulty)
}

// sanitizeDifficulty keeps only the hex digits the challenge program sent.
func sanitizeDifficulty(difficulty string) string {
	return difficultySanitizeRE.ReplaceAllString(difficulty, "")
}

// fnv1a32 is the current sentinel proof-of-work hash: FNV-1a 32 bit over the
// UTF-16 code units of the input, then a murmur3 style avalanche, rendered as
// eight hex digits.
func fnv1a32(text string) string {
	hash := uint32(2166136261)
	// The challenge program hashes UTF-16 code units, matching JS charCodeAt.
	for _, unit := range utf16.Encode([]rune(text)) {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	hash ^= hash >> 16
	hash *= 2246822507
	hash ^= hash >> 13
	hash *= 3266489909
	hash ^= hash >> 16
	return fmt.Sprintf("%08x", hash)
}

// powSatisfies reports whether a hash meets the difficulty, comparing the same
// number of leading hex digits the challenge program asked for.
func powSatisfies(hashHex, difficulty string) bool {
	difficulty = sanitizeDifficulty(difficulty)
	if difficulty == "" {
		return true
	}
	if len(hashHex) < len(difficulty) {
		return hashHex <= difficulty
	}
	return hashHex[:len(difficulty)] <= difficulty
}

func randomChoice(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[rand.Intn(len(items))]
}

func randomChoiceInt(items []int) int {
	if len(items) == 0 {
		return 0
	}
	return items[rand.Intn(len(items))]
}
