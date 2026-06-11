package ivr

import (
	"encoding/json"
	"strconv"
	"strings"
)

// IntFromConfig reads an int node-config value (JSON numbers decode as float64),
// falling back to def.
func IntFromConfig(c map[string]interface{}, key string, def int) int {
	if v, ok := c[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

// StringFromConfig reads a string node-config value ("" if absent/wrong type).
func StringFromConfig(c map[string]interface{}, key string) string {
	if v, ok := c[key].(string); ok {
		return v
	}
	return ""
}

// BoolFromConfig reads a bool node-config value (false if absent/wrong type).
func BoolFromConfig(c map[string]interface{}, key string) bool {
	if v, ok := c[key].(bool); ok {
		return v
	}
	return false
}

// MapFromConfig reads a nested object node-config value (nil if absent).
func MapFromConfig(c map[string]interface{}, key string) map[string]interface{} {
	if v, ok := c[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

// MapStringFromConfig reads a JSON object config field as map[string]string,
// dropping non-string values (used for http_callback headers).
func MapStringFromConfig(c map[string]interface{}, key string) map[string]string {
	out := map[string]string{}
	if m, ok := c[key].(map[string]interface{}); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// SubstituteVars replaces {{key}} tokens in a template with vars[key]. Unknown
// tokens are left as-is (visible in logs so the author can spot a typo).
func SubstituteVars(tmpl string, vars map[string]string) string {
	if !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", v)
	}
	return tmpl
}

// StringifyJSON renders a JSON value as the plain string stored in a flow/channel
// var (so {"priority":true} → "true", {"team":"sales"} → "sales").
func StringifyJSON(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
