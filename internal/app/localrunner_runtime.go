package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/localrunner"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/config"
	"github.com/gorilla/websocket"
)

const (
	defaultLocalRunnerOpenAPIBase = config.DefaultDEAPOpenAPIBaseURL
	maxLocalAgentCardBytes        = 1 << 20
)

var ErrLocalRunnerRuntimeInvalid = errors.New("local_runner_runtime_invalid")

type localRunnerStatusResult struct {
	Runner     *localrunner.RunnerStatusData     `json:"runner"`
	Connection *localrunner.ConnectionStatusData `json:"connection"`
}

type localRunnerCredentialStore interface {
	localrunner.EndpointBearerSink
	RemoveEndpointBearer(context.Context, string, string) error
}

type localRunnerRuntimeDependencies struct {
	ConfigDir         string
	ControlHTTPClient localrunner.HTTPDoer
	CardHTTPClient    localrunner.HTTPDoer
	ProxyHTTPClient   localrunner.HTTPDoer
	WSSDialer         *websocket.Dialer
	OAuth             localrunner.OAuthAccessTokenProvider
	Credentials       localRunnerCredentialStore
	OwnerIdentity     func(context.Context) (string, string, error)
	OpenAPIBaseURL    func() string
	ReconnectBackoff  time.Duration
}

type productionLocalRunnerCommandRuntime struct {
	configs           *localrunner.RunnerConfigStore
	controlHTTPClient localrunner.HTTPDoer
	cardHTTPClient    localrunner.HTTPDoer
	proxyHTTPClient   localrunner.HTTPDoer
	wssDialer         *websocket.Dialer
	oauth             localrunner.OAuthAccessTokenProvider
	credentials       localRunnerCredentialStore
	ownerIdentity     func(context.Context) (string, string, error)
	openAPIBaseURL    func() string
	reconnectBackoff  time.Duration
}

func newProductionLocalRunnerCommandRuntime(deps localRunnerRuntimeDependencies) *productionLocalRunnerCommandRuntime {
	configDir := strings.TrimSpace(deps.ConfigDir)
	if configDir == "" {
		configDir = defaultConfigDir()
	}
	if deps.ControlHTTPClient == nil {
		deps.ControlHTTPClient = &http.Client{Timeout: config.HTTPTimeout}
	}
	if deps.CardHTTPClient == nil {
		deps.CardHTTPClient = &http.Client{
			Timeout: config.HTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if deps.ProxyHTTPClient == nil {
		deps.ProxyHTTPClient = &http.Client{}
	}
	if deps.WSSDialer == nil {
		deps.WSSDialer = websocket.DefaultDialer
	}
	if deps.OAuth == nil {
		deps.OAuth = dwsLocalRunnerOAuth{configDir: configDir}
	}
	if deps.Credentials == nil {
		deps.Credentials = localrunner.NewSystemEndpointBearerKeyring()
	}
	if deps.OwnerIdentity == nil {
		deps.OwnerIdentity = dwsLocalRunnerOwnerIdentity(configDir)
	}
	if deps.OpenAPIBaseURL == nil {
		deps.OpenAPIBaseURL = config.GetDEAPOpenAPIBaseURL
	}
	if deps.ReconnectBackoff <= 0 {
		deps.ReconnectBackoff = time.Second
	}
	return &productionLocalRunnerCommandRuntime{
		configs:           localrunner.NewRunnerConfigStore(filepath.Join(configDir, "local-runners")),
		controlHTTPClient: deps.ControlHTTPClient,
		cardHTTPClient:    deps.CardHTTPClient,
		proxyHTTPClient:   deps.ProxyHTTPClient,
		wssDialer:         deps.WSSDialer,
		oauth:             deps.OAuth,
		credentials:       deps.Credentials,
		ownerIdentity:     deps.OwnerIdentity,
		openAPIBaseURL:    deps.OpenAPIBaseURL,
		reconnectBackoff:  deps.ReconnectBackoff,
	}
}

func (r *productionLocalRunnerCommandRuntime) Expose(ctx context.Context, options localRunnerExposeOptions) (*localrunner.CreatedRunner, error) {
	if r == nil || strings.TrimSpace(options.LocalAgentID) == "" || strings.TrimSpace(options.DisplayName) == "" || !localrunner.ValidateLoopbackHTTPURL(options.AgentCardURL) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	if strings.TrimSpace(options.OpenAPIBase) == "" {
		options.OpenAPIBase = r.openAPIBaseURL()
	}
	control, err := r.controlClient(options.OpenAPIBase)
	if err != nil {
		return nil, err
	}
	rawCard, err := r.readLocalAgentCard(ctx, options.AgentCardURL)
	if err != nil {
		return nil, err
	}
	return r.exposeLocalAgentCard(ctx, options, rawCard, control)
}

func (r *productionLocalRunnerCommandRuntime) StartLocal(ctx context.Context, options localRunnerStartLocalOptions) (*localRunnerStartLocalResult, error) {
	if r == nil || options.MaxConcurrent <= 0 {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	if strings.TrimSpace(options.OpenAPIBase) == "" {
		options.OpenAPIBase = r.openAPIBaseURL()
	}
	agentRef := strings.TrimSpace(options.AgentRef)
	agentCardURL := agentRef
	identitySeed := agentRef
	var stored *localrunner.StoredRunnerConfig
	var closeAgent func() error
	if helpers.IsLocalRunnerAgentChannel(agentRef) {
		workDir, err := normalizeLocalRunnerWorkDir(options.WorkDir)
		if err != nil {
			return nil, err
		}
		options.WorkDir = workDir
		options.Model = strings.TrimSpace(options.Model)
		identitySeed = agentRef + ":" + workDir
		storedLocalAgentID := strings.TrimSpace(options.LocalAgentID)
		if storedLocalAgentID == "" {
			storedLocalAgentID = localRunnerLocalAgentDefaultID(agentRef, workDir)
		}
		candidate, findErr := r.configs.FindByLocalAgentID(storedLocalAgentID)
		if findErr == nil {
			stored = candidate
		} else if !errors.Is(findErr, localrunner.ErrRunnerConfigNotFound) {
			return nil, findErr
		}
		if stored != nil && (stored.AgentKind != agentRef || stored.WorkDir != workDir) {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		var agent *localRunnerOpenCodeAgent
		if stored != nil {
			agent, err = localRunnerLocalAgentRestarter(ctx, stored.LoopbackBaseURL, agentRef, localRunnerLocalAgentOptions{
				WorkDir: workDir, Model: options.Model, Memory: options.Memory, Yolo: options.Yolo, Timeout: options.AgentTimeout,
			})
		} else {
			agent, err = localRunnerLocalAgentStarter(ctx, agentRef, localRunnerLocalAgentOptions{
				WorkDir: workDir, Model: options.Model, Memory: options.Memory, Yolo: options.Yolo, Timeout: options.AgentTimeout,
			})
		}
		if err != nil {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		agentCardURL = agent.CardURL()
		closeAgent = agent.Close
	} else if agentRef == localRunnerTestEchoRef {
		if strings.TrimSpace(options.WorkDir) != "" || strings.TrimSpace(options.Model) != "" {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		storedLocalAgentID := strings.TrimSpace(options.LocalAgentID)
		if storedLocalAgentID == "" {
			storedLocalAgentID = localRunnerTestEchoRef
		}
		candidate, findErr := r.configs.FindByLocalAgentID(storedLocalAgentID)
		if findErr == nil {
			stored = candidate
		} else if !errors.Is(findErr, localrunner.ErrRunnerConfigNotFound) {
			return nil, findErr
		}
		var agent *localRunnerBuiltInAgent
		var err error
		if stored != nil {
			agent, err = localRunnerTestEchoAgentRestarter(stored.LoopbackBaseURL)
		} else {
			agent, err = localRunnerTestEchoAgentStarter()
		}
		if err != nil {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		agentCardURL = agent.CardURL()
		closeAgent = agent.Close
	} else if strings.TrimSpace(options.WorkDir) != "" || strings.TrimSpace(options.Model) != "" || !localrunner.ValidateLoopbackHTTPURL(agentRef) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	keepAgent := false
	defer func() {
		if !keepAgent && closeAgent != nil {
			_ = closeAgent()
		}
	}()
	rawCard, err := r.readLocalAgentCard(ctx, agentCardURL)
	if err != nil {
		return nil, err
	}
	localAgentID, displayName, err := resolveLocalRunnerStartIdentity(rawCard, identitySeed, options.LocalAgentID, options.DisplayName)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		candidate, findErr := r.configs.FindByLocalAgentID(localAgentID)
		if findErr == nil {
			stored = candidate
		} else if !errors.Is(findErr, localrunner.ErrRunnerConfigNotFound) {
			return nil, findErr
		}
	}
	control, err := r.controlClient(options.OpenAPIBase)
	if err != nil {
		return nil, err
	}
	exposeOptions := localRunnerExposeOptions{
		LocalAgentID: localAgentID,
		DisplayName:  displayName,
		AgentCardURL: agentCardURL,
		OpenAPIBase:  options.OpenAPIBase,
	}
	if helpers.IsLocalRunnerAgentChannel(agentRef) {
		exposeOptions.AgentKind = agentRef
		exposeOptions.WorkDir = options.WorkDir
	}
	targetURL, err := localrunner.LocalAgentCardTarget(rawCard)
	if err != nil {
		return nil, localrunner.ErrInvalidAgentCard
	}
	var created *localrunner.CreatedRunner
	if stored != nil {
		created, err = r.recoverStoredLocalAgentCard(ctx, exposeOptions, rawCard, targetURL, stored, control)
	} else {
		created, err = r.exposeLocalAgentCard(ctx, exposeOptions, rawCard, control)
		if err == nil {
			stored, err = r.configs.Load(created.RunnerID)
		}
	}
	if err != nil {
		return nil, err
	}
	result := &localRunnerStartLocalResult{
		Summary: localRunnerA2AConfiguration{
			Type:         "A2A",
			AgentCardURL: created.AgentCardURL,
			LocalRunner: localRunnerA2ALocalRunner{
				RunnerID: created.RunnerID, EndpointID: created.EndpointID, Status: "CONNECTING",
			},
		},
		ConnectOptions: localRunnerConnectOptions{
			RunnerID: created.RunnerID, EndpointID: created.EndpointID, TargetURL: targetURL,
			AgentCardSHA256: stored.AgentCardSHA256, MaxConcurrent: options.MaxConcurrent, Streaming: options.Streaming,
		},
		Close: closeAgent,
	}
	keepAgent = true
	return result, nil
}

func (r *productionLocalRunnerCommandRuntime) recoverStoredLocalAgentCard(ctx context.Context, options localRunnerExposeOptions, rawCard json.RawMessage, targetURL string, stored *localrunner.StoredRunnerConfig, control *localrunner.HTTPControlClient) (*localrunner.CreatedRunner, error) {
	if r == nil || stored == nil || control == nil || stored.Validate() != nil || strings.TrimSpace(options.LocalAgentID) != stored.LocalAgentID || strings.TrimSpace(options.DisplayName) != stored.DisplayName || strings.TrimSpace(options.AgentCardURL) != stored.AgentCardURL || strings.TrimRight(strings.TrimSpace(options.OpenAPIBase), "/") != stored.OpenAPIBase || options.AgentKind != stored.AgentKind || options.WorkDir != stored.WorkDir || !sameLocalRunnerOrigin(stored.LoopbackBaseURL, targetURL) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	view, err := control.GetRunner(ctx, stored.RunnerID)
	if err != nil {
		return nil, err
	}
	if view.RunnerID != stored.RunnerID || view.EndpointID != stored.EndpointID || view.LocalAgentID != stored.LocalAgentID || view.DisplayName != stored.DisplayName || view.Status != localrunner.RunnerStatusActive || view.AgentCardSHA256 != stored.AgentCardSHA256 {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	publicCard, err := r.readPublicAgentCard(ctx, view.AgentCardURL)
	if err != nil || localRunnerAgentCardDigest(publicCard) != view.AgentCardSHA256 {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	publicBaseURL, err := localRunnerOrigin(view.AgentCardURL)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	snapshot, err := localrunner.RewriteAgentCard(rawCard, stored.EndpointID, publicBaseURL)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	if !localRunnerAgentCardsSemanticallyEqual(publicCard, snapshot.JSON) {
		updated, err := control.UpdateAgentCard(ctx, stored.RunnerID, localrunner.UpdateAgentCardRequest{AgentCard: rawCard})
		if err != nil {
			return nil, err
		}
		if updated.RunnerID != stored.RunnerID || updated.EndpointID != stored.EndpointID || updated.LocalAgentID != stored.LocalAgentID || updated.DisplayName != stored.DisplayName || updated.Status != localrunner.RunnerStatusActive || updated.AgentCardURL != view.AgentCardURL {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		updatedPublicCard, err := r.readPublicAgentCard(ctx, updated.AgentCardURL)
		if err != nil || localRunnerAgentCardDigest(updatedPublicCard) != updated.AgentCardSHA256 || !localRunnerAgentCardsSemanticallyEqual(updatedPublicCard, snapshot.JSON) {
			return nil, ErrLocalRunnerRuntimeInvalid
		}
		nextStored := *stored
		nextStored.AgentCardSHA256 = updated.AgentCardSHA256
		if err := r.configs.Save(nextStored); err != nil {
			return nil, err
		}
		*stored = nextStored
		view = updated
	}
	return &localrunner.CreatedRunner{
		RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
		AgentCardURL: view.AgentCardURL, Status: localrunner.RunnerStatusActive,
	}, nil
}

func (r *productionLocalRunnerCommandRuntime) exposeLocalAgentCard(ctx context.Context, options localRunnerExposeOptions, rawCard json.RawMessage, control *localrunner.HTTPControlClient) (*localrunner.CreatedRunner, error) {
	if r == nil || control == nil || strings.TrimSpace(options.LocalAgentID) == "" || strings.TrimSpace(options.DisplayName) == "" || !localrunner.ValidateLoopbackHTTPURL(options.AgentCardURL) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	localTarget, err := localrunner.LocalAgentCardTarget(rawCard)
	if err != nil {
		return nil, localrunner.ErrInvalidAgentCard
	}
	created, err := control.CreateRunner(ctx, localrunner.CreateRunnerRequest{
		LocalAgentID: strings.TrimSpace(options.LocalAgentID),
		DisplayName:  strings.TrimSpace(options.DisplayName),
		AgentCard:    rawCard,
	}, r.credentials)
	if err != nil {
		return nil, err
	}
	view, err := control.GetRunner(ctx, created.RunnerID)
	if err != nil || view.RunnerID != created.RunnerID || view.EndpointID != created.EndpointID || view.LocalAgentID != strings.TrimSpace(options.LocalAgentID) {
		_ = r.credentials.RemoveEndpointBearer(ctx, created.RunnerID, created.EndpointID)
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	origin, err := localRunnerOrigin(localTarget)
	if err != nil {
		_ = r.credentials.RemoveEndpointBearer(ctx, created.RunnerID, created.EndpointID)
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	stored := localrunner.StoredRunnerConfig{
		RunnerID:        created.RunnerID,
		EndpointID:      created.EndpointID,
		LocalAgentID:    strings.TrimSpace(options.LocalAgentID),
		DisplayName:     strings.TrimSpace(options.DisplayName),
		AgentCardURL:    strings.TrimSpace(options.AgentCardURL),
		LoopbackBaseURL: origin,
		OpenAPIBase:     strings.TrimRight(strings.TrimSpace(options.OpenAPIBase), "/"),
		AgentCardSHA256: view.AgentCardSHA256,
		AgentKind:       options.AgentKind,
		WorkDir:         options.WorkDir,
	}
	if err := r.configs.Save(stored); err != nil {
		_ = r.credentials.RemoveEndpointBearer(ctx, created.RunnerID, created.EndpointID)
		return nil, err
	}
	return created, nil
}

func resolveLocalRunnerStartIdentity(rawCard json.RawMessage, identitySeed, localAgentID, displayName string) (string, string, error) {
	identitySeed = strings.TrimSpace(identitySeed)
	localAgentRef := ""
	localAgentWorkDir := ""
	if ref, workDir, ok := strings.Cut(identitySeed, ":"); ok && helpers.IsLocalRunnerAgentChannel(ref) {
		localAgentRef = ref
		localAgentWorkDir = workDir
	}
	if identitySeed == "" || (identitySeed != localRunnerTestEchoRef && localAgentWorkDir == "" && !localrunner.ValidateLoopbackHTTPURL(identitySeed)) || (localAgentWorkDir != "" && (!filepath.IsAbs(localAgentWorkDir) || filepath.Clean(localAgentWorkDir) != localAgentWorkDir)) {
		return "", "", ErrLocalRunnerRuntimeInvalid
	}
	var card struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rawCard, &card); err != nil {
		return "", "", localrunner.ErrInvalidAgentCard
	}
	resolvedID := strings.TrimSpace(localAgentID)
	if resolvedID == "" {
		if identitySeed == localRunnerTestEchoRef {
			resolvedID = localRunnerTestEchoRef
		} else if localAgentWorkDir != "" {
			resolvedID = localRunnerLocalAgentDefaultID(localAgentRef, localAgentWorkDir)
		} else {
			digest := sha256.Sum256([]byte(identitySeed))
			resolvedID = fmt.Sprintf("local-%x", digest[:8])
		}
	}
	resolvedName := strings.TrimSpace(displayName)
	if resolvedName == "" {
		resolvedName = strings.TrimSpace(card.Name)
	}
	if resolvedID == "" || resolvedName == "" {
		return "", "", localrunner.ErrInvalidAgentCard
	}
	return resolvedID, resolvedName, nil
}

func localRunnerOpenCodeDefaultID(workDir string) string {
	return localRunnerLocalAgentDefaultID(localRunnerOpenCodeRef, workDir)
}

func localRunnerLocalAgentDefaultID(agentRef, workDir string) string {
	digest := sha256.Sum256([]byte(workDir))
	return fmt.Sprintf("%s-%x", strings.TrimSpace(agentRef), digest[:8])
}

func (r *productionLocalRunnerCommandRuntime) Status(ctx context.Context, runnerID string) (*localRunnerStatusResult, error) {
	stored, control, err := r.storedControl(runnerID)
	if err != nil {
		return nil, err
	}
	runner, err := control.GetRunner(ctx, stored.RunnerID)
	if err != nil {
		return nil, err
	}
	connection, err := control.GetConnection(ctx, stored.RunnerID)
	if err != nil {
		return nil, err
	}
	if runner.RunnerID != stored.RunnerID || runner.EndpointID != stored.EndpointID || connection.RunnerID != stored.RunnerID || connection.EndpointID != stored.EndpointID {
		return nil, localrunner.ErrTicketBindingMismatch
	}
	return &localRunnerStatusResult{Runner: runner, Connection: connection}, nil
}

func (r *productionLocalRunnerCommandRuntime) Revoke(ctx context.Context, runnerID string) (*localrunner.RevokeRunnerData, error) {
	stored, control, err := r.storedControl(runnerID)
	if err != nil {
		return nil, err
	}
	result, err := control.RevokeRunner(ctx, stored.RunnerID)
	if err != nil {
		return nil, err
	}
	if result.RunnerID != stored.RunnerID || result.EndpointID != stored.EndpointID {
		return nil, localrunner.ErrTicketBindingMismatch
	}
	if err := r.credentials.RemoveEndpointBearer(ctx, stored.RunnerID, stored.EndpointID); err != nil {
		return nil, err
	}
	if err := r.configs.Delete(stored.RunnerID); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *productionLocalRunnerCommandRuntime) Connect(ctx context.Context, options localRunnerConnectOptions) (*localrunner.ConnectionStateSnapshot, error) {
	stored, control, err := r.storedControl(options.RunnerID)
	if err != nil {
		return nil, err
	}
	if stored.EndpointID != strings.TrimSpace(options.EndpointID) || stored.AgentCardSHA256 != strings.TrimSpace(options.AgentCardSHA256) || options.MaxConcurrent <= 0 || !localrunner.ValidateLoopbackHTTPURL(options.TargetURL) || !sameLocalRunnerOrigin(stored.LoopbackBaseURL, options.TargetURL) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	tenantID, operatorUserID, err := r.ownerIdentity(ctx)
	if err != nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(operatorUserID) == "" {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	identity := localrunner.RunnerEndpointIdentity{
		TenantID: strings.TrimSpace(tenantID), OperatorUserID: strings.TrimSpace(operatorUserID),
		RunnerID: stored.RunnerID, EndpointID: stored.EndpointID,
	}
	configValue, err := localrunner.NewRunnerEndpointConfig(identity)
	if err != nil {
		return nil, err
	}
	state := localrunner.NewSingleEndpointConnectionStateMachine(configValue)
	proxy, err := localrunner.NewLocalA2AProxy(localrunner.LocalA2AProxyConfig{
		TargetURL: options.TargetURL, HTTPClient: r.proxyHTTPClient,
		MaxConcurrent: options.MaxConcurrent,
	})
	if err != nil {
		return nil, err
	}
	session := localrunner.NewTunnelSession(identity, state, localrunner.NewGorillaWSSDialAdapter(r.wssDialer), localrunner.NewTunnelCodec(localrunner.DefaultMaxFrameBytes))
	reconnect := &localrunner.ReconnectRunner{
		Identity: identity, Opener: control, Attempts: session,
		Hello: localrunner.HelloConfig{AgentCardSHA256: stored.AgentCardSHA256, MaxConcurrent: options.MaxConcurrent, Streaming: options.Streaming},
		ReconnectBackoff: r.reconnectBackoff,
	}
	err = reconnect.Run(ctx, proxy)
	if err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	snapshot := state.Snapshot()
	return &snapshot, nil
}

func (r *productionLocalRunnerCommandRuntime) storedControl(runnerID string) (*localrunner.StoredRunnerConfig, *localrunner.HTTPControlClient, error) {
	if r == nil {
		return nil, nil, ErrLocalRunnerRuntimeInvalid
	}
	stored, err := r.configs.Load(strings.TrimSpace(runnerID))
	if err != nil {
		return nil, nil, err
	}
	control, err := r.controlClient(stored.OpenAPIBase)
	if err != nil {
		return nil, nil, err
	}
	return stored, control, nil
}

func (r *productionLocalRunnerCommandRuntime) controlClient(baseURL string) (*localrunner.HTTPControlClient, error) {
	control, err := localrunner.NewHTTPControlClient(baseURL, r.controlHTTPClient, r.oauth,
		localrunner.ControlOwnerIdentityProviderFunc(r.ownerIdentity))
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	return control, nil
}

func (r *productionLocalRunnerCommandRuntime) readLocalAgentCard(ctx context.Context, rawURL string) (json.RawMessage, error) {
	if r == nil || r.cardHTTPClient == nil || !localrunner.ValidateLoopbackHTTPURL(rawURL) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	req.Header.Set("Accept", "application/json")
	response, err := r.cardHTTPClient.Do(req)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL == nil || !localrunner.ValidateLoopbackHTTPURL(response.Request.URL.String()) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxLocalAgentCardBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxLocalAgentCardBytes {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	return json.RawMessage(append([]byte(nil), raw...)), nil
}

func (r *productionLocalRunnerCommandRuntime) readPublicAgentCard(ctx context.Context, rawURL string) (json.RawMessage, error) {
	if r == nil || r.cardHTTPClient == nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	req.Header.Set("Accept", "application/json")
	response, err := r.cardHTTPClient.Do(req)
	if err != nil {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL == nil || response.Request.URL.String() != parsed.String() {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxLocalAgentCardBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxLocalAgentCardBytes || !localRunnerValidAgentCardJSON(raw) {
		return nil, ErrLocalRunnerRuntimeInvalid
	}
	return json.RawMessage(append([]byte(nil), raw...)), nil
}

func localRunnerAgentCardDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func localRunnerAgentCardsSemanticallyEqual(left, right []byte) bool {
	leftCanonical, leftOK := localRunnerCanonicalAgentCardJSON(left)
	rightCanonical, rightOK := localRunnerCanonicalAgentCardJSON(right)
	return leftOK && rightOK && bytes.Equal(leftCanonical, rightCanonical)
}

func localRunnerValidAgentCardJSON(raw []byte) bool {
	_, ok := localRunnerCanonicalAgentCardJSON(raw)
	return ok
}

func localRunnerCanonicalAgentCardJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	canonical, err := json.Marshal(value)
	return canonical, err == nil
}

func localRunnerOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrLocalRunnerRuntimeInvalid
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func sameLocalRunnerOrigin(origin, target string) bool {
	targetOrigin, err := localRunnerOrigin(target)
	return err == nil && strings.EqualFold(strings.TrimRight(origin, "/"), targetOrigin)
}

type dwsLocalRunnerOAuth struct {
	configDir string
}

func (o dwsLocalRunnerOAuth) AccessToken(ctx context.Context) (string, error) {
	return ResolveAuxiliaryAccessToken(ctx, o.configDir, "")
}

func (o dwsLocalRunnerOAuth) RefreshRejectedAccessToken(ctx context.Context, rejected string) (string, error) {
	return forceRefreshRejectedAccessToken(ctx, o.configDir, rejected)
}

func dwsLocalRunnerOwnerIdentity(configDir string) func(context.Context) (string, string, error) {
	return func(context.Context) (string, string, error) {
		data, err := authpkg.LoadTokenData(configDir)
		if err != nil || data == nil || strings.TrimSpace(data.CorpID) == "" || strings.TrimSpace(data.UserID) == "" {
			return "", "", fmt.Errorf("%w: owner identity", ErrLocalRunnerRuntimeInvalid)
		}
		return data.CorpID, data.UserID, nil
	}
}
