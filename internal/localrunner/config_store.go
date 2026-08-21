package localrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/config"
)

var (
	ErrRunnerConfigInvalid  = errors.New("local_runner_config_invalid")
	ErrRunnerConfigNotFound = errors.New("local_runner_config_not_found")
)

type StoredRunnerConfig struct {
	RunnerID        string `json:"runnerId"`
	EndpointID      string `json:"endpointId"`
	LocalAgentID    string `json:"localAgentId"`
	DisplayName     string `json:"displayName"`
	AgentCardURL    string `json:"agentCardUrl"`
	LoopbackBaseURL string `json:"loopbackBaseUrl"`
	OpenAPIBase     string `json:"openApiBase"`
	AgentCardSHA256 string `json:"agentCardSha256"`
	AgentKind       string `json:"agentKind,omitempty"`
	WorkDir         string `json:"workDir,omitempty"`
}

func (c StoredRunnerConfig) Validate() error {
	if strings.TrimSpace(c.RunnerID) == "" || strings.TrimSpace(c.EndpointID) == "" || strings.TrimSpace(c.LocalAgentID) == "" || strings.TrimSpace(c.DisplayName) == "" {
		return ErrRunnerConfigInvalid
	}
	if !validLoopbackHTTPURL(c.AgentCardURL) || !validLoopbackOrigin(c.LoopbackBaseURL) || !validOpenAPIBase(c.OpenAPIBase) {
		return ErrRunnerConfigInvalid
	}
	if c.AgentKind == "" && c.WorkDir == "" {
		// Legacy external/test-echo bindings predate local process metadata.
	} else if !validLocalAgentKind(c.AgentKind) || strings.TrimSpace(c.WorkDir) != c.WorkDir || !filepath.IsAbs(c.WorkDir) || filepath.Clean(c.WorkDir) != c.WorkDir {
		return ErrRunnerConfigInvalid
	}
	const agentCardSHA256Prefix = "sha256:"
	if !strings.HasPrefix(c.AgentCardSHA256, agentCardSHA256Prefix) {
		return ErrRunnerConfigInvalid
	}
	hexDigest := strings.TrimPrefix(c.AgentCardSHA256, agentCardSHA256Prefix)
	if len(hexDigest) != sha256.Size*2 {
		return ErrRunnerConfigInvalid
	}
	decoded, err := hex.DecodeString(hexDigest)
	if err != nil || len(decoded) != sha256.Size || hexDigest != strings.ToLower(hexDigest) {
		return ErrRunnerConfigInvalid
	}
	return nil
}

func validLocalAgentKind(kind string) bool {
	if kind == "" || strings.TrimSpace(kind) != kind {
		return false
	}
	for _, char := range kind {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

type RunnerConfigStore struct {
	root string
}

func NewRunnerConfigStore(root string) *RunnerConfigStore {
	return &RunnerConfigStore{root: filepath.Clean(strings.TrimSpace(root))}
}

func (s *RunnerConfigStore) Save(value StoredRunnerConfig) error {
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." || value.Validate() != nil {
		return ErrRunnerConfigInvalid
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ErrRunnerConfigInvalid
	}
	if err := helpers.AtomicWrite(s.path(value.RunnerID), raw, config.FilePerm); err != nil {
		return fmt.Errorf("%w: save", ErrRunnerConfigInvalid)
	}
	return nil
}

func (s *RunnerConfigStore) Load(runnerID string) (*StoredRunnerConfig, error) {
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." || strings.TrimSpace(runnerID) == "" {
		return nil, ErrRunnerConfigInvalid
	}
	raw, err := os.ReadFile(s.path(runnerID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrRunnerConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load", ErrRunnerConfigInvalid)
	}
	value, err := decodeStoredRunnerConfig(raw)
	if err != nil || value.RunnerID != strings.TrimSpace(runnerID) {
		return nil, ErrRunnerConfigInvalid
	}
	return value, nil
}

func (s *RunnerConfigStore) FindByLocalAgentID(localAgentID string) (*StoredRunnerConfig, error) {
	localAgentID = strings.TrimSpace(localAgentID)
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." || localAgentID == "" {
		return nil, ErrRunnerConfigInvalid
	}
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrRunnerConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: find", ErrRunnerConfigInvalid)
	}
	var found *StoredRunnerConfig
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: find", ErrRunnerConfigInvalid)
		}
		value, err := decodeStoredRunnerConfig(raw)
		if err != nil || filepath.Base(s.path(value.RunnerID)) != entry.Name() {
			return nil, ErrRunnerConfigInvalid
		}
		if strings.TrimSpace(value.LocalAgentID) != localAgentID {
			continue
		}
		if value.LocalAgentID != localAgentID || found != nil {
			return nil, ErrRunnerConfigInvalid
		}
		found = value
	}
	if found == nil {
		return nil, ErrRunnerConfigNotFound
	}
	return found, nil
}

func (s *RunnerConfigStore) Delete(runnerID string) error {
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." || strings.TrimSpace(runnerID) == "" {
		return ErrRunnerConfigInvalid
	}
	err := os.Remove(s.path(runnerID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: delete", ErrRunnerConfigInvalid)
	}
	return nil
}

func (s *RunnerConfigStore) path(runnerID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(runnerID)))
	return filepath.Join(s.root, fmt.Sprintf("%x.json", digest))
}

func decodeStoredRunnerConfig(raw []byte) (*StoredRunnerConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value StoredRunnerConfig
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Validate() != nil {
		return nil, ErrRunnerConfigInvalid
	}
	return &value, nil
}

func validLoopbackOrigin(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !validLoopbackHTTPURL(raw) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

func validOpenAPIBase(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && validLoopbackHTTPURL(raw)
}
