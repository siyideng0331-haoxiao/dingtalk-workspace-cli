package localrunner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOpenConnectionRequestContainsOnlyEndpointID(t *testing.T) {
	encoded, err := json.Marshal(OpenConnectionRequest{EndpointID: "endpoint-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"endpointId":"endpoint-1"}` {
		t.Fatalf("request shape = %s", encoded)
	}
}

func TestOpenConnectionSuccessMatchesFrozenEnvelope(t *testing.T) {
	response, err := DecodeOpenConnectionSuccess([]byte(`{
		"success": true,
		"data": {
			"runnerId": "runner-1",
			"endpointId": "endpoint-1",
			"webSocketUrl": "wss://gateway.example.test/v1/local-runners/connections/runner-1",
			"connectionTicket": "lr1.payload.signature",
			"ticketExpiresAtEpochSecond": 200
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Data.ValidateFor(testIdentity(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenConnectionSuccessRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing success", body: `{"data":{}}`},
		{name: "false success", body: `{"success":false,"data":{}}`},
		{name: "missing data", body: `{"success":true}`},
		{name: "missing required data field", body: `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","webSocketUrl":"wss://gateway.example.test/connect","connectionTicket":"lr1.payload.signature"}}`},
		{name: "unknown data field", body: `{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","webSocketUrl":"wss://gateway.example.test/connect","connectionTicket":"lr1.payload.signature","ticketExpiresAtEpochSecond":200,"extra":true}}`},
		{name: "malformed", body: `{"success":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeOpenConnectionSuccess([]byte(tt.body)); !errors.Is(err, ErrConnectionResponseMalformed) {
				t.Fatalf("error = %v, want malformed response", err)
			}
		})
	}
}

func TestOpenConnectionDataRejectsPreDialDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OpenConnectionData)
		want   error
	}{
		{name: "runner", mutate: func(data *OpenConnectionData) { data.RunnerID = "other" }, want: ErrTicketBindingMismatch},
		{name: "endpoint", mutate: func(data *OpenConnectionData) { data.EndpointID = "other" }, want: ErrTicketBindingMismatch},
		{name: "non wss", mutate: func(data *OpenConnectionData) { data.WebSocketURL = "ws://gateway.example.test/connect" }, want: ErrConnectionResponseMalformed},
		{name: "relative wss", mutate: func(data *OpenConnectionData) { data.WebSocketURL = "/connect" }, want: ErrConnectionResponseMalformed},
		{name: "credential query", mutate: func(data *OpenConnectionData) { data.WebSocketURL = "wss://gateway.example.test/connect?ticket=secret" }, want: ErrConnectionResponseMalformed},
		{name: "expired", mutate: func(data *OpenConnectionData) { data.TicketExpiresAtEpochSecond = 100 }, want: ErrTicketExpired},
		{name: "missing ticket", mutate: func(data *OpenConnectionData) { data.ConnectionTicket = nil }, want: ErrConnectionResponseMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := validOpenConnectionData(t)
			tt.mutate(data)
			if err := data.ValidateFor(testIdentity(), time.Unix(100, 0)); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestOpenConnectionFailureUsesStatusAndCode(t *testing.T) {
	tests := []struct {
		status int
		code   string
	}{
		{status: 400, code: "invalidParameter"},
		{status: 401, code: "unauthorized"},
		{status: 404, code: "localRunnerNotFound"},
		{status: 410, code: "localRunnerRevoked"},
		{status: 429, code: "rateLimitExceeded"},
		{status: 550, code: "internalError"},
	}
	for _, tt := range tests {
		body := []byte(fmt.Sprintf(`{"error":{"code":%q,"message":"diagnostic only"}}`, tt.code))
		failure, err := DecodeOpenConnectionFailure(tt.status, body)
		if err != nil {
			t.Fatalf("status %d: %v", tt.status, err)
		}
		if failure.StatusCode != tt.status || failure.Error == nil || failure.Error.Code != tt.code {
			t.Fatalf("status/code mismatch for %d", tt.status)
		}
		if strings.Contains(failure.String(), "diagnostic only") {
			t.Fatal("stable failure summary included diagnostic message")
		}
	}
}

func TestOpenConnectionFailureRejectsUnknownPairWithoutBodyLeak(t *testing.T) {
	body := []byte(`{"error":{"code":"secret-value","message":"ticket=do-not-print"}}`)
	_, err := DecodeOpenConnectionFailure(404, body)
	if !errors.Is(err, ErrConnectionResponseMalformed) {
		t.Fatalf("error kind = %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "do-not-print") {
		t.Fatal("parse error exposed response body")
	}
}

func TestConnectionTicketIsRedactedAndAppliedOnce(t *testing.T) {
	data := validOpenConnectionData(t)
	if strings.Contains(fmt.Sprint(data.ConnectionTicket), "lr1.") || strings.Contains(fmt.Sprintf("%#v", data.ConnectionTicket), "lr1.") {
		t.Fatal("formatted ticket was not redacted")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("lr1.")) {
		t.Fatal("JSON exposed ticket")
	}

	header := make(http.Header)
	if err := data.ConnectionTicket.ApplyAuthorization(header); err != nil {
		t.Fatal(err)
	}
	if got := header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") || len(got) <= len("Bearer ") {
		t.Fatal("authorization header was not applied")
	}
	if len(data.ConnectionTicket.raw) != 0 || !data.ConnectionTicket.used {
		t.Fatal("ticket bytes were not cleared after use")
	}
	if err := data.ConnectionTicket.ApplyAuthorization(make(http.Header)); !errors.Is(err, ErrConnectionTicketAlreadyUsed) {
		t.Fatalf("second apply error = %v", err)
	}
}

func testIdentity() RunnerEndpointIdentity {
	return RunnerEndpointIdentity{
		TenantID:       "tenant",
		OperatorUserID: "operator",
		RunnerID:       "runner-1",
		EndpointID:     "endpoint-1",
	}
}

func validOpenConnectionData(t *testing.T) *OpenConnectionData {
	t.Helper()
	response, err := DecodeOpenConnectionSuccess([]byte(`{
		"success": true,
		"data": {
			"runnerId": "runner-1",
			"endpointId": "endpoint-1",
			"webSocketUrl": "wss://gateway.example.test/v1/local-runners/connections/runner-1",
			"connectionTicket": "lr1.payload.signature",
			"ticketExpiresAtEpochSecond": 200
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return response.Data
}
