package localrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrConnectionResponseMalformed  = errors.New("connection_response_malformed")
	ErrTicketBindingMismatch        = errors.New("ticket_binding_mismatch")
	ErrTicketExpired                = errors.New("ticket_expired")
	ErrConnectionTicketAlreadyUsed  = errors.New("connection_ticket_already_used")
)

type OpenConnectionRequest struct {
	EndpointID string `json:"endpointId"`
}

func (r OpenConnectionRequest) Validate() error {
	if strings.TrimSpace(r.EndpointID) == "" {
		return ErrConnectionResponseMalformed
	}
	return nil
}

type OpenConnectionSuccess struct {
	Success *bool               `json:"success"`
	Data    *OpenConnectionData `json:"data"`
}

type OpenConnectionData struct {
	RunnerID                    string            `json:"runnerId"`
	EndpointID                  string            `json:"endpointId"`
	WebSocketURL                string            `json:"webSocketUrl"`
	ConnectionTicket            *ConnectionTicket `json:"connectionTicket"`
	TicketExpiresAtEpochSecond int64             `json:"ticketExpiresAtEpochSecond"`
}

func DecodeOpenConnectionSuccess(data []byte) (*OpenConnectionSuccess, error) {
	var response OpenConnectionSuccess
	keys := []string{"runnerId", "endpointId", "webSocketUrl", "connectionTicket", "ticketExpiresAtEpochSecond"}
	if err := decodeFrozenSuccess(data, keys, &response); err != nil {
		return nil, ErrConnectionResponseMalformed
	}
	if response.Success == nil || !*response.Success || response.Data == nil || strings.TrimSpace(response.Data.RunnerID) == "" || strings.TrimSpace(response.Data.EndpointID) == "" || strings.TrimSpace(response.Data.WebSocketURL) == "" || response.Data.ConnectionTicket == nil || response.Data.ConnectionTicket.empty() || response.Data.TicketExpiresAtEpochSecond <= 0 {
		return nil, ErrConnectionResponseMalformed
	}
	return &response, nil
}

func (d OpenConnectionData) ValidateFor(identity RunnerEndpointIdentity, now time.Time) error {
	expected, err := identity.normalized()
	if err != nil {
		return err
	}
	if strings.TrimSpace(d.RunnerID) == "" || strings.TrimSpace(d.EndpointID) == "" {
		return ErrConnectionResponseMalformed
	}
	if d.RunnerID != expected.RunnerID || d.EndpointID != expected.EndpointID {
		return ErrTicketBindingMismatch
	}
	if d.ConnectionTicket == nil || d.ConnectionTicket.empty() {
		return ErrConnectionResponseMalformed
	}
	if d.TicketExpiresAtEpochSecond <= now.Unix() {
		return ErrTicketExpired
	}
	if err := validateWebSocketURL(d.WebSocketURL); err != nil {
		return err
	}
	return nil
}

func validateWebSocketURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Scheme != "wss" || u.Host == "" || u.User != nil {
		return ErrConnectionResponseMalformed
	}
	for key := range u.Query() {
		switch strings.ToLower(key) {
		case "ticket", "connectionticket", "authorization", "access_token":
			return ErrConnectionResponseMalformed
		}
	}
	return nil
}

type ConnectionTicket struct {
	raw  []byte
	used bool
}

func (t *ConnectionTicket) UnmarshalJSON(data []byte) error {
	if t == nil {
		return ErrConnectionResponseMalformed
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil || strings.TrimSpace(raw) == "" {
		return ErrConnectionResponseMalformed
	}
	t.discard()
	t.raw = append(t.raw[:0], raw...)
	t.used = false
	return nil
}

func (t ConnectionTicket) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED]")
}

func (t ConnectionTicket) String() string {
	return "[REDACTED]"
}

func (t ConnectionTicket) GoString() string {
	return "[REDACTED]"
}

func (t *ConnectionTicket) ApplyAuthorization(header http.Header) error {
	if t == nil || t.used {
		return ErrConnectionTicketAlreadyUsed
	}
	if len(t.raw) == 0 || header == nil {
		return ErrConnectionResponseMalformed
	}
	header.Set("Authorization", "Bearer "+string(t.raw))
	for i := range t.raw {
		t.raw[i] = 0
	}
	t.raw = nil
	t.used = true
	return nil
}

func (t *ConnectionTicket) empty() bool {
	return t == nil || t.used || len(t.raw) == 0
}

func (t *ConnectionTicket) discard() {
	if t == nil {
		return
	}
	for i := range t.raw {
		t.raw[i] = 0
	}
	t.raw = nil
	t.used = true
}

type OpenConnectionFailure struct {
	StatusCode int                  `json:"-"`
	Error      *OpenConnectionError `json:"error"`
}

type OpenConnectionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var openConnectionFailureCodes = map[int]string{
	400: "invalidParameter",
	401: "unauthorized",
	404: "localRunnerNotFound",
	410: "localRunnerRevoked",
	429: "rateLimitExceeded",
	550: "internalError",
}

func DecodeOpenConnectionFailure(statusCode int, data []byte) (*OpenConnectionFailure, error) {
	var failure OpenConnectionFailure
	if err := json.Unmarshal(data, &failure); err != nil || failure.Error == nil {
		return nil, ErrConnectionResponseMalformed
	}
	want, ok := openConnectionFailureCodes[statusCode]
	if !ok || failure.Error.Code != want {
		return nil, ErrConnectionResponseMalformed
	}
	failure.StatusCode = statusCode
	return &failure, nil
}

func (f OpenConnectionFailure) String() string {
	code := "unknown"
	if f.Error != nil {
		code = f.Error.Code
	}
	return fmt.Sprintf("connections/open failed: status=%d code=%s", f.StatusCode, code)
}
