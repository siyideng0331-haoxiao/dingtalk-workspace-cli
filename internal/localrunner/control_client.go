package localrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxControlResponseBytes = 1 << 20

const (
	controlTenantHeader = "X-Dingtalk-Corp-Id"
	controlUserHeader   = "X-Dingtalk-User-Id"
)

var ErrControlClientInvalid = errors.New("control_client_invalid")

type OAuthAccessTokenProvider interface {
	AccessToken(context.Context) (string, error)
	RefreshRejectedAccessToken(context.Context, string) (string, error)
}

type ControlOwnerIdentityProvider interface {
	OwnerIdentity(context.Context) (string, string, error)
}

type ControlOwnerIdentityProviderFunc func(context.Context) (string, string, error)

func (f ControlOwnerIdentityProviderFunc) OwnerIdentity(ctx context.Context) (string, string, error) {
	return f(ctx)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type ControlClient interface {
	CreateRunner(context.Context, CreateRunnerRequest, EndpointBearerSink) (*CreatedRunner, error)
	GetRunner(context.Context, string) (*RunnerStatusData, error)
	UpdateAgentCard(context.Context, string, UpdateAgentCardRequest) (*RunnerStatusData, error)
	RevokeRunner(context.Context, string) (*RevokeRunnerData, error)
	GetConnection(context.Context, string) (*ConnectionStatusData, error)
	Disconnect(context.Context, string) (*DisconnectData, error)
	OpenConnection(context.Context, RunnerEndpointIdentity, time.Time) (*OpenConnectionData, error)
}

type CreatedRunner struct {
	RunnerID     string       `json:"runnerId"`
	EndpointID   string       `json:"endpointId"`
	AgentCardURL string       `json:"agentCardUrl"`
	Status       RunnerStatus `json:"status"`
}

type HTTPControlClient struct {
	baseURL      string
	httpClient   HTTPDoer
	tokenProvider OAuthAccessTokenProvider
	ownerProvider ControlOwnerIdentityProvider
}

func NewHTTPControlClient(baseURL string, httpClient HTTPDoer, tokenProvider OAuthAccessTokenProvider,
	ownerProvider ControlOwnerIdentityProvider) (*HTTPControlClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !validOpenAPIBase(baseURL) || httpClient == nil || tokenProvider == nil || ownerProvider == nil {
		return nil, ErrControlClientInvalid
	}
	return &HTTPControlClient{
		baseURL:       strings.TrimRight(parsed.String(), "/"),
		httpClient:    httpClient,
		tokenProvider: tokenProvider,
		ownerProvider: ownerProvider,
	}, nil
}

func (c *HTTPControlClient) CreateRunner(ctx context.Context, request CreateRunnerRequest, sink EndpointBearerSink) (*CreatedRunner, error) {
	if err := request.Validate(); err != nil || sink == nil {
		return nil, ErrControlClientInvalid
	}
	raw, _ := json.Marshal(request)
	status, body, err := c.request(ctx, http.MethodPost, "/v1/assistant/local-runners", raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, decodeControlFailure(status, body, createFailureCodes)
	}
	response, err := DecodeCreateRunnerSuccess(body)
	if err != nil {
		return nil, err
	}
	data := response.Data
	if err := data.EndpointBearer.Store(ctx, sink, data.RunnerID, data.EndpointID); err != nil {
		return nil, ErrEndpointBearerStoreFailed
	}
	return &CreatedRunner{
		RunnerID:     data.RunnerID,
		EndpointID:   data.EndpointID,
		AgentCardURL: data.AgentCardURL,
		Status:       data.Status,
	}, nil
}

func (c *HTTPControlClient) GetRunner(ctx context.Context, runnerID string) (*RunnerStatusData, error) {
	status, body, err := c.runnerRequest(ctx, http.MethodGet, runnerID, "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, runnerFailureCodes)
	}
	response, err := DecodeRunnerStatusSuccess(body)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) UpdateAgentCard(ctx context.Context, runnerID string, request UpdateAgentCardRequest) (*RunnerStatusData, error) {
	if err := request.Validate(); err != nil {
		return nil, ErrControlClientInvalid
	}
	raw, _ := json.Marshal(request)
	status, body, err := c.runnerRequest(ctx, http.MethodPut, runnerID, "/agent-card", raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, updateCardFailureCodes)
	}
	response, err := DecodeRunnerStatusSuccess(body)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) RevokeRunner(ctx context.Context, runnerID string) (*RevokeRunnerData, error) {
	status, body, err := c.runnerRequest(ctx, http.MethodDelete, runnerID, "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, runnerFailureCodes)
	}
	response, err := DecodeRevokeRunnerSuccess(body)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) GetConnection(ctx context.Context, runnerID string) (*ConnectionStatusData, error) {
	status, body, err := c.runnerRequest(ctx, http.MethodGet, runnerID, "/connection", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, runnerFailureCodes)
	}
	response, err := DecodeConnectionStatusSuccess(body)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) Disconnect(ctx context.Context, runnerID string) (*DisconnectData, error) {
	status, body, err := c.runnerRequest(ctx, http.MethodDelete, runnerID, "/connection", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, runnerFailureCodes)
	}
	response, err := DecodeDisconnectSuccess(body)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) OpenConnection(ctx context.Context, identity RunnerEndpointIdentity, now time.Time) (*OpenConnectionData, error) {
	normalized, err := identity.normalized()
	if err != nil {
		return nil, err
	}
	request := OpenConnectionRequest{EndpointID: normalized.EndpointID}
	raw, _ := json.Marshal(request)
	status, body, err := c.runnerRequest(ctx, http.MethodPost, normalized.RunnerID, "/connections/open", raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decodeControlFailure(status, body, openConnectionControlFailureCodes)
	}
	response, err := DecodeOpenConnectionSuccess(body)
	if err != nil {
		return nil, err
	}
	if err := response.Data.ValidateFor(normalized, now); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *HTTPControlClient) runnerRequest(ctx context.Context, method, runnerID, suffix string, body []byte) (int, []byte, error) {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return 0, nil, ErrControlClientInvalid
	}
	path := "/v1/assistant/local-runners/" + url.PathEscape(runnerID) + suffix
	return c.request(ctx, method, path, body)
}

func (c *HTTPControlClient) request(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	token, err := c.tokenProvider.AccessToken(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return 0, nil, ErrControlClientInvalid
	}
	tenantID, userID, err := c.ownerProvider.OwnerIdentity(ctx)
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if err != nil || tenantID == "" || userID == "" {
		return 0, nil, ErrControlClientInvalid
	}
	status, response, err := c.requestAttempt(ctx, method, path, body, token, tenantID, userID)
	if err != nil || status != http.StatusUnauthorized {
		return status, response, err
	}
	fresh, refreshErr := c.tokenProvider.RefreshRejectedAccessToken(ctx, token)
	if refreshErr != nil || strings.TrimSpace(fresh) == "" {
		return 0, nil, ErrControlClientInvalid
	}
	return c.requestAttempt(ctx, method, path, body, fresh, tenantID, userID)
}

func (c *HTTPControlClient) requestAttempt(ctx context.Context, method, path string, body []byte,
	token, tenantID, userID string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, ErrControlClientInvalid
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(controlTenantHeader, tenantID)
	req.Header.Set(controlUserHeader, userID)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxControlResponseBytes+1))
	if err != nil || len(raw) > maxControlResponseBytes {
		return response.StatusCode, nil, ErrLifecycleResponseMalformed
	}
	return response.StatusCode, raw, nil
}

var createFailureCodes = map[int]map[string]bool{
	400: {"invalidParameter": true, "invalidAgentCard": true},
	401: {"unauthorized": true},
	404: {"localRunnerNotFound": true},
	410: {"localRunnerRevoked": true},
	409: {"localRunnerConflict": true},
	429: {"rateLimitExceeded": true},
	550: {"internalError": true},
}

var runnerFailureCodes = map[int]map[string]bool{
	400: {"invalidParameter": true},
	401: {"unauthorized": true},
	404: {"localRunnerNotFound": true},
	410: {"localRunnerRevoked": true},
	429: {"rateLimitExceeded": true},
	550: {"internalError": true},
}

var updateCardFailureCodes = map[int]map[string]bool{
	400: {"invalidParameter": true, "invalidAgentCard": true},
	401: {"unauthorized": true},
	404: {"localRunnerNotFound": true},
	410: {"localRunnerRevoked": true},
	429: {"rateLimitExceeded": true},
	550: {"internalError": true},
}

var openConnectionControlFailureCodes = map[int]map[string]bool{
	400: {"invalidParameter": true},
	401: {"unauthorized": true},
	404: {"localRunnerNotFound": true},
	410: {"localRunnerRevoked": true},
	429: {"rateLimitExceeded": true},
	550: {"internalError": true},
}

func decodeControlFailure(status int, data []byte, allowed map[int]map[string]bool) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || !hasExactKeys(envelope, []string{"error"}) {
		return ErrLifecycleResponseMalformed
	}
	var errorObject map[string]json.RawMessage
	if err := json.Unmarshal(envelope["error"], &errorObject); err != nil || !hasExactKeys(errorObject, []string{"code", "message"}) {
		return ErrLifecycleResponseMalformed
	}
	var controlError ControlError
	if err := json.Unmarshal(envelope["error"], &controlError); err != nil || strings.TrimSpace(controlError.Code) == "" || strings.TrimSpace(controlError.Message) == "" {
		return ErrLifecycleResponseMalformed
	}
	codes, ok := allowed[status]
	if !ok || !codes[controlError.Code] {
		return ErrLifecycleResponseMalformed
	}
	return &ControlFailure{StatusCode: status, Detail: &controlError}
}
