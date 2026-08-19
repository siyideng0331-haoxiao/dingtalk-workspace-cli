package localrunner

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var ErrWSSDialFailed = errors.New("local_runner_wss_dial_failed")

type TunnelSocket interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}

type TunnelDialFunc func(context.Context, string, http.Header) (TunnelSocket, *http.Response, error)

type TunnelSocketDialer interface {
	Dial(context.Context, OpenConnectionData, RunnerEndpointIdentity, time.Time) (TunnelSocket, error)
}

type WSSDialAdapter struct {
	dial TunnelDialFunc
}

func NewWSSDialAdapter(dial TunnelDialFunc) *WSSDialAdapter {
	return &WSSDialAdapter{dial: dial}
}

func NewGorillaWSSDialAdapter(dialer *websocket.Dialer) *WSSDialAdapter {
	if dialer == nil {
		dialer = &websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	}
	return NewWSSDialAdapter(func(ctx context.Context, rawURL string, header http.Header) (TunnelSocket, *http.Response, error) {
		return dialer.DialContext(ctx, rawURL, header)
	})
}

func (d *WSSDialAdapter) Dial(ctx context.Context, data OpenConnectionData, identity RunnerEndpointIdentity, now time.Time) (TunnelSocket, error) {
	if d == nil || d.dial == nil {
		return nil, ErrWSSDialFailed
	}
	if err := data.ValidateFor(identity, now); err != nil {
		return nil, err
	}
	header := make(http.Header)
	if err := data.ConnectionTicket.ApplyAuthorization(header); err != nil {
		return nil, err
	}
	defer header.Del("Authorization")
	socket, response, err := d.dial(ctx, data.WebSocketURL, header)
	if err != nil || socket == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ErrWSSDialFailed
	}
	return socket, nil
}
