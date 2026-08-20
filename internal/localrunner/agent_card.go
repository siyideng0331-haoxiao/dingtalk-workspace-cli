package localrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
)

var ErrInvalidAgentCard = errors.New("invalid_agent_card")

type AgentCardSnapshot struct {
	JSON   []byte
	SHA256 string
	ETag   string
}

func RewriteAgentCard(raw json.RawMessage, endpointID, publicBaseURL string) (*AgentCardSnapshot, error) {
	endpointID = strings.TrimSpace(endpointID)
	publicBase, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || endpointID == "" || !publicBase.IsAbs() || publicBase.Scheme != "https" || publicBase.Host == "" || publicBase.User != nil || publicBase.Fragment != "" {
		return nil, ErrInvalidAgentCard
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var card map[string]any
	if err := decoder.Decode(&card); err != nil || card == nil {
		return nil, ErrInvalidAgentCard
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidAgentCard
	}
	if !validAgentCardCore(card) {
		return nil, ErrInvalidAgentCard
	}
	publicRPCURL := strings.TrimRight(publicBase.String(), "/") + "/v1/a2a/local-runners/" + url.PathEscape(endpointID) + "/rpc"
	callableURLs := 0
	if value, ok := card["url"]; ok {
		localURL, ok := value.(string)
		if !ok || !validLoopbackHTTPURL(localURL) {
			return nil, ErrInvalidAgentCard
		}
		card["url"] = publicRPCURL
		callableURLs++
	}
	for _, field := range []string{"supportedInterfaces", "additionalInterfaces"} {
		count, err := rewriteAgentCardInterfaces(card, field, publicRPCURL)
		if err != nil {
			return nil, err
		}
		callableURLs += count
	}
	if callableURLs == 0 {
		return nil, ErrInvalidAgentCard
	}
	delete(card, "authentication")
	card["securitySchemes"] = map[string]any{
		"localRunnerBearer": map[string]any{
			"type":   "http",
			"scheme": "bearer",
		},
	}
	card["security"] = []any{
		map[string]any{"localRunnerBearer": []any{}},
	}
	if containsLoopbackHTTPURL(card) {
		return nil, ErrInvalidAgentCard
	}

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(card); err != nil {
		return nil, ErrInvalidAgentCard
	}
	canonical := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	canonical = append([]byte(nil), canonical...)
	digest := sha256.Sum256(canonical)
	hexDigest := hex.EncodeToString(digest[:])
	return &AgentCardSnapshot{
		JSON:   canonical,
		SHA256: hexDigest,
		ETag:   `"sha256:` + hexDigest + `"`,
	}, nil
}

func validAgentCardCore(card map[string]any) bool {
	name, ok := card["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return false
	}
	protocol, ok := card["protocolVersion"].(string)
	if !ok || (protocol != "0.3.0" && protocol != "1.0") {
		return false
	}
	capabilities, ok := card["capabilities"].(map[string]any)
	if !ok || capabilities == nil {
		return false
	}
	skills, ok := card["skills"].([]any)
	if !ok {
		return false
	}
	for _, skill := range skills {
		if object, ok := skill.(map[string]any); !ok || object == nil {
			return false
		}
	}
	return true
}

func rewriteAgentCardInterfaces(card map[string]any, field, publicRPCURL string) (int, error) {
	value, exists := card[field]
	if !exists {
		return 0, nil
	}
	interfaces, ok := value.([]any)
	if !ok {
		return 0, ErrInvalidAgentCard
	}
	count := 0
	for _, value := range interfaces {
		object, ok := value.(map[string]any)
		if !ok || object == nil {
			return 0, ErrInvalidAgentCard
		}
		localURL, ok := object["url"].(string)
		if !ok || !validLoopbackHTTPURL(localURL) {
			return 0, ErrInvalidAgentCard
		}
		object["url"] = publicRPCURL
		count++
	}
	return count, nil
}

func validLoopbackHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ValidateLoopbackHTTPURL(raw string) bool {
	return validLoopbackHTTPURL(raw)
}

func LocalAgentCardTarget(raw json.RawMessage) (string, error) {
	if _, err := RewriteAgentCard(raw, "validation-endpoint", "https://localrunner.invalid"); err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var card map[string]any
	if err := decoder.Decode(&card); err != nil {
		return "", ErrInvalidAgentCard
	}
	if target, ok := card["url"].(string); ok && validLoopbackHTTPURL(target) {
		return target, nil
	}
	for _, field := range []string{"supportedInterfaces", "additionalInterfaces"} {
		interfaces, _ := card[field].([]any)
		for _, value := range interfaces {
			object, _ := value.(map[string]any)
			target, _ := object["url"].(string)
			if validLoopbackHTTPURL(target) {
				return target, nil
			}
		}
	}
	return "", ErrInvalidAgentCard
}

func containsLoopbackHTTPURL(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if containsLoopbackHTTPURL(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsLoopbackHTTPURL(child) {
				return true
			}
		}
	case string:
		return isLexicalLoopbackHTTPURL(typed)
	}
	return false
}

func isLexicalLoopbackHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
