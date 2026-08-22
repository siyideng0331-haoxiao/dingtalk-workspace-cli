package dwsauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxGrantResponseBytes = 1 << 20

var (
	ErrClientInvalid        = errors.New("dws_auth_client_invalid")
	ErrGrantResponseInvalid = errors.New("dws_auth_grant_response_invalid")
)

type OAuthAccessTokenProvider interface {
	AccessToken(context.Context) (string, error)
	RefreshRejectedAccessToken(context.Context, string) (string, error)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Grant struct {
	AssistantID      string
	CorpID           string
	UID              string
	AuthCode         string
	ExpiresInSeconds int
}

func (g Grant) String() string {
	return fmt.Sprintf("Grant{AssistantID:%q CorpID:%q UID:%q AuthCode:[REDACTED] ExpiresInSeconds:%d}",
		g.AssistantID, g.CorpID, g.UID, g.ExpiresInSeconds)
}

func (g Grant) GoString() string {
	return g.String()
}

func (g Grant) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AssistantID      string `json:"assistantId"`
		CorpID           string `json:"corpId"`
		UID              string `json:"uid"`
		AuthCode         string `json:"authCode"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
	}{
		AssistantID:      g.AssistantID,
		CorpID:           g.CorpID,
		UID:              g.UID,
		AuthCode:         "[REDACTED]",
		ExpiresInSeconds: g.ExpiresInSeconds,
	})
}

type RequestError struct {
	StatusCode int
	Code       string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("DWS authorization grant request failed: status=%d code=%s",
		e.StatusCode, e.Code)
}

type Client struct {
	baseURL       string
	httpClient    HTTPDoer
	tokenProvider OAuthAccessTokenProvider
}

func NewClient(baseURL string, httpClient HTTPDoer,
	tokenProvider OAuthAccessTokenProvider) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		httpClient == nil || tokenProvider == nil {
		return nil, ErrClientInvalid
	}
	return &Client{
		baseURL:       strings.TrimRight(parsed.String(), "/"),
		httpClient:    httpClient,
		tokenProvider: tokenProvider,
	}, nil
}

func (c *Client) Issue(ctx context.Context, assistantID string) (*Grant, error) {
	assistantID = strings.TrimSpace(assistantID)
	if assistantID == "" {
		return nil, ErrClientInvalid
	}
	accessToken, err := c.tokenProvider.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, ErrClientInvalid
	}
	status, body, err := c.request(ctx, assistantID, accessToken)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		refreshed, refreshErr := c.tokenProvider.RefreshRejectedAccessToken(ctx, accessToken)
		if refreshErr != nil {
			return nil, refreshErr
		}
		refreshed = strings.TrimSpace(refreshed)
		if refreshed == "" {
			return nil, ErrClientInvalid
		}
		status, body, err = c.request(ctx, assistantID, refreshed)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, decodeRequestError(status, body)
	}
	return decodeGrant(assistantID, body)
}

func (c *Client) request(ctx context.Context, assistantID, accessToken string) (int, []byte, error) {
	endpoint := c.baseURL + "/v1/assistant/digital-employees/" +
		url.PathEscape(assistantID) + "/dws-auth-grants"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, http.NoBody)
	if err != nil {
		return 0, nil, ErrClientInvalid
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGrantResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(body) > maxGrantResponseBytes {
		return 0, nil, ErrGrantResponseInvalid
	}
	return response.StatusCode, body, nil
}

func decodeGrant(assistantID string, body []byte) (*Grant, error) {
	type grantData struct {
		AssistantID      string `json:"assistantId"`
		CorpID           string `json:"corpId"`
		UID              string `json:"uid"`
		AuthCode         string `json:"authCode"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
	}
	type successEnvelope struct {
		Success *bool      `json:"success"`
		Data    *grantData `json:"data"`
	}
	var response successEnvelope
	if err := decodeSingleJSON(body, &response); err != nil || response.Success == nil ||
		!*response.Success || response.Data == nil {
		return nil, ErrGrantResponseInvalid
	}
	data := response.Data
	data.AssistantID = strings.TrimSpace(data.AssistantID)
	data.CorpID = strings.TrimSpace(data.CorpID)
	data.UID = strings.TrimSpace(data.UID)
	data.AuthCode = strings.TrimSpace(data.AuthCode)
	uid, uidErr := strconv.ParseInt(data.UID, 10, 64)
	if data.AssistantID != assistantID || data.CorpID == "" || uidErr != nil || uid <= 0 ||
		data.AuthCode == "" || data.ExpiresInSeconds <= 0 {
		return nil, ErrGrantResponseInvalid
	}
	return &Grant{
		AssistantID:      data.AssistantID,
		CorpID:           data.CorpID,
		UID:              data.UID,
		AuthCode:         data.AuthCode,
		ExpiresInSeconds: data.ExpiresInSeconds,
	}, nil
}

func decodeRequestError(status int, body []byte) error {
	type failureEnvelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	var response failureEnvelope
	code := "httpError"
	if decodeSingleJSON(body, &response) == nil && response.Error != nil {
		if value := strings.TrimSpace(response.Error.Code); value != "" {
			code = value
		}
	}
	return &RequestError{StatusCode: status, Code: code}
}

func decodeSingleJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrGrantResponseInvalid
	}
	return nil
}
