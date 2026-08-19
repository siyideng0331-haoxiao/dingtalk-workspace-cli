package localrunner

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestReconnectRunnerOpensFreshTicketForEveryAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	opener := &sequenceConnectionOpener{t: t}
	attempts := &recordingAttemptRunner{cancel: cancel}
	runner := &ReconnectRunner{
		Identity:         testIdentity(),
		Opener:           opener,
		Attempts:         attempts,
		Hello:            HelloConfig{AgentCardSHA256: "sha", MaxConcurrent: 1, Streaming: true},
		ReconnectBackoff: time.Millisecond,
		Now:              func() time.Time { return time.Unix(100, 0) },
	}
	if err := runner.Run(ctx, &recordingFrameHandler{}); err != context.Canceled {
		t.Fatalf("run error = %v", err)
	}
	if opener.calls != 2 || attempts.calls != 2 || len(attempts.authorizations) != 2 {
		t.Fatalf("open=%d attempts=%d authorizations=%d", opener.calls, attempts.calls, len(attempts.authorizations))
	}
	if attempts.authorizations[0] == attempts.authorizations[1] {
		t.Fatal("reconnect reused the previous connection ticket")
	}
}

type sequenceConnectionOpener struct {
	t     *testing.T
	calls int
}

func (o *sequenceConnectionOpener) OpenConnection(_ context.Context, _ RunnerEndpointIdentity, _ time.Time) (*OpenConnectionData, error) {
	o.calls++
	body := []byte(`{"success":true,"data":{"runnerId":"runner-1","endpointId":"endpoint-1","webSocketUrl":"wss://gateway.example.test/connect","connectionTicket":"lr1.ticket-` + string(rune('0'+o.calls)) + `","ticketExpiresAtEpochSecond":200}}`)
	response, err := DecodeOpenConnectionSuccess(body)
	if err != nil {
		o.t.Fatal(err)
	}
	return response.Data, nil
}

type recordingAttemptRunner struct {
	cancel         context.CancelFunc
	calls          int
	authorizations []string
}

func (r *recordingAttemptRunner) RunAttempt(ctx context.Context, data OpenConnectionData, _ HelloConfig, _ TunnelFrameHandler) error {
	r.calls++
	header := make(http.Header)
	if err := data.ConnectionTicket.ApplyAuthorization(header); err != nil {
		return err
	}
	r.authorizations = append(r.authorizations, header.Get("Authorization"))
	if r.calls == 1 {
		return ErrTunnelDisconnected
	}
	r.cancel()
	return ctx.Err()
}
