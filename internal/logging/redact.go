// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	freeTextBearerPattern = regexp.MustCompile(`(?i)\bbearer[[:space:]]+[A-Za-z0-9._~+/=-]+`)
	freeTextSensitivePairPattern = regexp.MustCompile(`(?i)["']?(password|passwd|pwd|token|secret|credential|cookie|api[_-]?key|authorization|context([_-]?id)?|session([_-]?id)?|prompt)["']?[[:space:]]*[:=][[:space:]]*("[^"\r\n]*"|'[^'\r\n]*'|[^[:space:],;&]+)`)
	freeTextSensitiveFlagPattern = regexp.MustCompile(`(?i)--(password|passwd|pwd|token|secret|credential|cookie|api[_-]?key|authorization|context([_-]?id)?|session([_-]?id)?|prompt)[[:space:]]+("[^"\r\n]*"|'[^'\r\n]*'|[^[:space:],;&]+)`)
	freeTextUUIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	freeTextSensitiveMarkerPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|secret|credential|cookie|authorization|bearer|context|session|prompt)\b|api[_-]?key`)
)

// sensitiveKeys are header/field names whose values must be redacted in logs.
var sensitiveKeys = map[string]bool{
	"authorization":       true,
	"x-user-access-token": true,
	"client_secret":       true,
	"client-secret":       true,
	"token":               true,
	"secret":              true,
	"password":            true,
	"cookie":              true,
	"api_key":             true,
	"api-key":             true,
	"access_token":        true,
	"credential":          true,
	"configstring":        true,
	"config_string":       true,
	"config-string":       true,
	"envs":                true,
	"headers":             true,
	"fileurl":             true,
	"file_url":            true,
	"file-url":            true,
	"uploadurl":           true,
	"upload_url":          true,
	"upload-url":          true,
}

// sensitiveSubstrings are substrings that mark a key as sensitive.
var sensitiveSubstrings = []string{
	"password", "secret", "token", "credential",
}

// IsSensitiveKey returns true if the key (case-insensitive) refers to a
// credential or secret that must not appear in log files.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if sensitiveKeys[lower] {
		return true
	}
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// RedactValue replaces a sensitive value with a safe placeholder.
// It preserves the first 4 characters for identification if the value is
// long enough, otherwise fully redacts.
func RedactValue(value string) string {
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***"
}

// TruncateBody returns the body truncated to maxBytes with a UTF-8 safe
// boundary. If truncated, appends a marker showing the original size.
func TruncateBody(body []byte, maxBytes int) string {
	if len(body) <= maxBytes {
		return string(body)
	}
	safe := body[:maxBytes]
	// Walk back to a valid UTF-8 boundary.
	for len(safe) > 0 && !utf8.Valid(safe) {
		safe = safe[:len(safe)-1]
	}
	return fmt.Sprintf("%s...(truncated, total=%d bytes)", string(safe), len(body))
}

// SanitizeArguments returns a JSON string of the arguments map with
// sensitive-looking values replaced by "***". Truncates to maxBytes.
func SanitizeArguments(args map[string]any, maxBytes int) string {
	if len(args) == 0 {
		return "{}"
	}
	sanitized, _ := sanitizeArgumentValue(args).(map[string]any)
	data, err := json.Marshal(sanitized)
	if err != nil {
		return "{}"
	}
	return TruncateBody(data, maxBytes)
}

// SanitizeFreeText keeps a bounded diagnostic summary while removing values
// that must not enter logs. exactSecrets is intended for request-scoped values
// such as the current prompt and A2A context ID. If a sensitive marker remains
// after sanitization, the function fails closed with an empty result.
func SanitizeFreeText(text string, exactSecrets []string, maxRunes int) string {
	return sanitizeFreeText(text, exactSecrets, maxRunes, true)
}

// SanitizeMessageText applies the same fail-closed secret redaction as
// SanitizeFreeText while preserving ordinary whitespace in delivered message
// text, including the leading space of an appended streaming delta.
func SanitizeMessageText(text string, exactSecrets []string, maxRunes int) string {
	return sanitizeFreeText(text, exactSecrets, maxRunes, false)
}

func sanitizeFreeText(text string, exactSecrets []string, maxRunes int, collapseWhitespace bool) string {
	if maxRunes <= 0 {
		return ""
	}
	sanitized := text
	secrets := append([]string(nil), exactSecrets...)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			sanitized = strings.ReplaceAll(sanitized, secret, "[redacted]")
		}
	}
	sanitized = freeTextBearerPattern.ReplaceAllString(sanitized, "[redacted]")
	sanitized = freeTextSensitivePairPattern.ReplaceAllString(sanitized, "[redacted]")
	sanitized = freeTextSensitiveFlagPattern.ReplaceAllString(sanitized, "[redacted]")
	sanitized = freeTextUUIDPattern.ReplaceAllString(sanitized, "[redacted]")
	sanitized = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			if !collapseWhitespace && (r == '\n' || r == '\t') {
				return r
			}
			return ' '
		}
		return r
	}, sanitized)
	if collapseWhitespace {
		sanitized = strings.Join(strings.Fields(sanitized), " ")
	}
	if strings.TrimSpace(sanitized) == "" || freeTextSensitiveMarkerPattern.MatchString(sanitized) {
		return ""
	}
	runes := []rune(sanitized)
	if len(runes) > maxRunes {
		if maxRunes == 1 {
			return "…"
		}
		sanitized = string(runes[:maxRunes-1]) + "…"
	}
	return sanitized
}

// redactMapValues replaces values of sensitive keys with "***" in-place.
func redactMapValues(m map[string]any) {
	sanitized, _ := sanitizeArgumentValue(m).(map[string]any)
	for key := range m {
		delete(m, key)
	}
	for key, value := range sanitized {
		m[key] = value
	}
}

func sanitizeArgumentValue(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return value
		}
		result := make(map[string]any, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if IsSensitiveKey(key) {
				result[key] = "***"
				continue
			}
			result[key] = sanitizeArgumentValue(iterator.Value().Interface())
		}
		return result
	case reflect.Slice, reflect.Array:
		result := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			result[index] = sanitizeArgumentValue(reflected.Index(index).Interface())
		}
		return result
	default:
		return value
	}
}

// RedactHeaders returns slog attributes for HTTP headers with sensitive
// values redacted.
func RedactHeaders(headers http.Header) []slog.Attr {
	if len(headers) == 0 {
		return nil
	}
	attrs := make([]slog.Attr, 0, len(headers))
	for key := range headers {
		value := headers.Get(key)
		if IsSensitiveKey(key) {
			value = RedactValue(value)
		}
		attrs = append(attrs, slog.String("header."+strings.ToLower(key), value))
	}
	return attrs
}
