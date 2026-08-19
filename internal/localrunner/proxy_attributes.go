package localrunner

import (
	"encoding/json"
	"net/http"
	"strings"
)

type RequestStartAttributes struct {
	Method          string
	Path            string
	Query           string
	Headers         map[string][]string
	ContentLength   int64
	DeadlineEpochMs int64
}

type ResponseStartAttributes struct {
	Status  int
	Headers map[string][]string
}

func DecodeRequestStartAttributes(attributes map[string]json.RawMessage) (*RequestStartAttributes, error) {
	keys := []string{"method", "path", "query", "headers", "contentLength", "deadlineEpochMs"}
	if !hasExactKeys(attributes, keys) {
		return nil, ErrTunnelProtocol
	}
	var result RequestStartAttributes
	if json.Unmarshal(attributes["method"], &result.Method) != nil || json.Unmarshal(attributes["path"], &result.Path) != nil || json.Unmarshal(attributes["query"], &result.Query) != nil || json.Unmarshal(attributes["contentLength"], &result.ContentLength) != nil || json.Unmarshal(attributes["deadlineEpochMs"], &result.DeadlineEpochMs) != nil {
		return nil, ErrTunnelProtocol
	}
	if strings.TrimSpace(result.Method) == "" || !strings.HasPrefix(result.Path, "/") || strings.HasPrefix(result.Query, "?") || result.ContentLength < 0 || result.DeadlineEpochMs <= 0 {
		return nil, ErrTunnelProtocol
	}
	headers, err := decodeFrozenHeaders(attributes["headers"])
	if err != nil {
		return nil, err
	}
	result.Headers = filterTunnelHeaders(headers)
	return &result, nil
}

func EncodeResponseStartAttributes(status int, header http.Header) (map[string]json.RawMessage, error) {
	if status < 100 || status > 599 {
		return nil, ErrTunnelProtocol
	}
	filtered := make(map[string][]string)
	for name, values := range header {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || prohibitedTunnelHeader(lower) {
			continue
		}
		filtered[lower] = append([]string(nil), values...)
	}
	statusJSON, _ := json.Marshal(status)
	headerJSON, err := json.Marshal(filtered)
	if err != nil {
		return nil, ErrTunnelProtocol
	}
	return map[string]json.RawMessage{
		"status":  statusJSON,
		"headers": headerJSON,
	}, nil
}

func DecodeResponseStartAttributes(attributes map[string]json.RawMessage) (*ResponseStartAttributes, error) {
	if !hasExactKeys(attributes, []string{"status", "headers"}) {
		return nil, ErrTunnelProtocol
	}
	var status int
	if json.Unmarshal(attributes["status"], &status) != nil || status < 100 || status > 599 {
		return nil, ErrTunnelProtocol
	}
	headers, err := decodeFrozenHeaders(attributes["headers"])
	if err != nil {
		return nil, err
	}
	return &ResponseStartAttributes{Status: status, Headers: filterTunnelHeaders(headers)}, nil
}

func decodeFrozenHeaders(raw json.RawMessage) (map[string][]string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrTunnelProtocol
	}
	result := make(map[string][]string, len(fields))
	for name, value := range fields {
		if name == "" || name != strings.ToLower(name) {
			return nil, ErrTunnelProtocol
		}
		var values []string
		if json.Unmarshal(value, &values) != nil || values == nil {
			return nil, ErrTunnelProtocol
		}
		result[name] = append([]string(nil), values...)
	}
	return result, nil
}

func filterTunnelHeaders(headers map[string][]string) map[string][]string {
	filtered := make(map[string][]string)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if prohibitedTunnelHeader(lower) {
			continue
		}
		filtered[lower] = append([]string(nil), values...)
	}
	return filtered
}

func prohibitedTunnelHeader(name string) bool {
	if strings.HasPrefix(name, "x-forwarded-") {
		return true
	}
	switch name {
	case "authorization", "cookie", "set-cookie", "host", "connection", "upgrade", "proxy-authorization", "proxy-authenticate", "te", "trailer", "transfer-encoding":
		return true
	default:
		return false
	}
}
