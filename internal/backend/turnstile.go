package backend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type turnstileFunc func(args ...any)

type turnstileOrderedMap struct {
	keys   []string
	values map[string]any
}

func (m *turnstileOrderedMap) add(key string, value any) {
	if m.values == nil {
		m.values = map[string]any{}
	}
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func solveTurnstileToken(dx, p string) string {
	decoded, err := base64.StdEncoding.DecodeString(dx)
	if err != nil {
		return ""
	}
	var tokenList [][]any
	if err := json.Unmarshal([]byte(xorTurnstileString(string(decoded), p)), &tokenList); err != nil {
		return ""
	}

	process := map[int]any{}
	start := time.Now()
	result := ""
	get := func(value any) any {
		return process[turnstileKey(value)]
	}
	set := func(key any, value any) {
		process[turnstileKey(key)] = value
	}
	call := func(value any, args ...any) {
		if fn, ok := value.(turnstileFunc); ok {
			fn(args...)
		}
	}

	process[1] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		set(args[0], xorTurnstileString(turnstileToString(get(args[0])), turnstileToString(get(args[1]))))
	})
	process[2] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		set(args[0], args[1])
	})
	process[3] = turnstileFunc(func(args ...any) {
		if len(args) == 0 {
			return
		}
		result = base64.StdEncoding.EncodeToString([]byte(turnstileToString(args[0])))
	})
	process[5] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		current := get(args[0])
		incoming := get(args[1])
		if list, ok := current.([]any); ok {
			set(args[0], append(list, incoming))
			return
		}
		if _, ok := current.(string); ok {
			set(args[0], turnstileToString(current)+turnstileToString(incoming))
			return
		}
		if _, ok := current.(float64); ok {
			set(args[0], turnstileToString(current)+turnstileToString(incoming))
			return
		}
		if _, ok := incoming.(string); ok {
			set(args[0], turnstileToString(current)+turnstileToString(incoming))
			return
		}
		if _, ok := incoming.(float64); ok {
			set(args[0], turnstileToString(current)+turnstileToString(incoming))
			return
		}
		set(args[0], "NaN")
	})
	process[6] = turnstileFunc(func(args ...any) {
		if len(args) < 3 {
			return
		}
		left, leftOK := get(args[1]).(string)
		right, rightOK := get(args[2]).(string)
		if !leftOK || !rightOK {
			return
		}
		value := left + "." + right
		if value == "window.document.location" {
			value = "https://chatgpt.com/"
		}
		set(args[0], value)
	})
	process[7] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		target := get(args[0])
		values := make([]any, 0, len(args)-1)
		for _, arg := range args[1:] {
			values = append(values, get(arg))
		}
		if target == "window.Reflect.set" && len(values) >= 3 {
			if obj, ok := values[0].(*turnstileOrderedMap); ok {
				obj.add(turnstileToString(values[1]), values[2])
			}
			return
		}
		call(target, values...)
	})
	process[8] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		set(args[0], get(args[1]))
	})
	process[9] = tokenList
	process[10] = "window"
	process[14] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		var value any
		if json.Unmarshal([]byte(turnstileToString(get(args[1]))), &value) == nil {
			set(args[0], value)
		}
	})
	process[15] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		data, err := json.Marshal(get(args[1]))
		if err == nil {
			set(args[0], string(data))
		}
	})
	process[16] = p
	process[17] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		callArgs := make([]any, 0, len(args)-2)
		for _, arg := range args[2:] {
			callArgs = append(callArgs, get(arg))
		}
		switch get(args[1]) {
		case "window.performance.now":
			elapsed := float64(time.Since(start).Nanoseconds()) + rand.Float64()
			set(args[0], elapsed/1e6)
		case "window.Object.create":
			set(args[0], &turnstileOrderedMap{})
		case "window.Object.keys":
			if len(callArgs) > 0 && callArgs[0] == "window.localStorage" {
				set(args[0], []string{
					"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4",
					"STATSIG_LOCAL_STORAGE_STABLE_ID",
					"client-correlated-secret",
					"oai/apps/capExpiresAt",
					"oai-did",
					"STATSIG_LOCAL_STORAGE_LOGGING_REQUEST",
					"UiState.isNavigationCollapsed.1",
				})
			}
		case "window.Math.random":
			set(args[0], rand.Float64())
		default:
			call(get(args[1]), callArgs...)
		}
	})
	process[18] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		data, err := base64.StdEncoding.DecodeString(turnstileToString(get(args[0])))
		if err == nil {
			set(args[0], string(data))
		}
	})
	process[19] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		set(args[0], base64.StdEncoding.EncodeToString([]byte(turnstileToString(get(args[0])))))
	})
	process[20] = turnstileFunc(func(args ...any) {
		if len(args) < 3 || !reflect.DeepEqual(get(args[0]), get(args[1])) {
			return
		}
		callArgs := make([]any, 0, len(args)-3)
		for _, arg := range args[3:] {
			callArgs = append(callArgs, get(arg))
		}
		call(get(args[2]), callArgs...)
	})
	process[21] = turnstileFunc(func(args ...any) {})
	process[23] = turnstileFunc(func(args ...any) {
		if len(args) < 2 || get(args[0]) == nil {
			return
		}
		call(get(args[1]), args[2:]...)
	})
	process[24] = turnstileFunc(func(args ...any) {
		if len(args) < 3 {
			return
		}
		left, leftOK := get(args[1]).(string)
		right, rightOK := get(args[2]).(string)
		if leftOK && rightOK {
			set(args[0], left+"."+right)
		}
	})

	for _, token := range tokenList {
		if len(token) == 0 {
			continue
		}
		call(process[turnstileKey(token[0])], token[1:]...)
	}
	return result
}

func turnstileKey(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	default:
		i, _ := strconv.Atoi(turnstileToString(value))
		return i
	}
}

func turnstileToString(value any) string {
	switch v := value.(type) {
	case nil:
		return "undefined"
	case float64:
		if math.Trunc(v) == v {
			return strconv.FormatFloat(v, 'f', 1, 64)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		switch v {
		case "window.Math":
			return "[object Math]"
		case "window.Reflect":
			return "[object Reflect]"
		case "window.performance":
			return "[object Performance]"
		case "window.localStorage":
			return "[object Storage]"
		case "window.Object":
			return "function Object() { [native code] }"
		case "window.Reflect.set":
			return "function set() { [native code] }"
		case "window.performance.now":
			return "function () { [native code] }"
		case "window.Object.create":
			return "function create() { [native code] }"
		case "window.Object.keys":
			return "function keys() { [native code] }"
		case "window.Math.random":
			return "function random() { [native code] }"
		default:
			return v
		}
	case []string:
		return strings.Join(v, ",")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return fmt.Sprint(value)
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(value)
	}
}

func xorTurnstileString(text, key string) string {
	if key == "" {
		return text
	}
	var out strings.Builder
	keyRunes := []rune(key)
	for index, ch := range text {
		out.WriteRune(ch ^ keyRunes[index%len(keyRunes)])
	}
	return out.String()
}
