package localrunner

import (
	"context"
	"errors"
	"time"
)

type ConnectionOpener interface {
	OpenConnection(context.Context, RunnerEndpointIdentity, time.Time) (*OpenConnectionData, error)
}

type ConnectionAttemptRunner interface {
	RunAttempt(context.Context, OpenConnectionData, HelloConfig, TunnelFrameHandler) error
}

type ReconnectRunner struct {
	Identity         RunnerEndpointIdentity
	Opener           ConnectionOpener
	Attempts         ConnectionAttemptRunner
	Hello            HelloConfig
	ReconnectBackoff time.Duration
	DisableReconnect bool
	Now              func() time.Time
}

func (r *ReconnectRunner) Run(ctx context.Context, handler TunnelFrameHandler) error {
	if r == nil || r.Opener == nil || r.Attempts == nil || r.Hello.Validate() != nil {
		return ErrTunnelProtocol
	}
	if _, err := r.Identity.normalized(); err != nil {
		return err
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	backoff := r.ReconnectBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		data, err := r.Opener.OpenConnection(ctx, r.Identity, now())
		if err == nil {
			err = r.Attempts.RunAttempt(ctx, *data, r.Hello, handler)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.DisableReconnect || !reconnectableTunnelError(err) {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func reconnectableTunnelError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, ErrEndpointRevoked) || errors.Is(err, ErrInvalidIdentity) || errors.Is(err, ErrTicketBindingMismatch) || errors.Is(err, ErrTunnelProtocol) {
		return false
	}
	var failure *ControlFailure
	if errors.As(err, &failure) {
		switch failure.StatusCode {
		case 400, 401, 404, 410:
			return false
		}
	}
	return true
}
