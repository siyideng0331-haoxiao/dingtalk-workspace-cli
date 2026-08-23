package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestAndResponseStartAttributesUseFrozenShape(t *testing.T) {
	request := validRequestStartFrame("request-1", 5)
	attributes, err := DecodeRequestStartAttributes(request.Attributes)
	if err != nil {
		t.Fatal(err)
	}
	if attributes.Method != "POST" || attributes.Path != "/rpc" || attributes.Query != "stream=true" || attributes.ContentLength != 5 || len(attributes.Headers["accept"]) != 1 {
		t.Fatalf("request attributes = %#v", attributes)
	}

	invalid := request.Attributes
	invalid["headers"] = json.RawMessage(`{"accept":"application/json"}`)
	if _, err := DecodeRequestStartAttributes(invalid); err != ErrTunnelProtocol {
		t.Fatalf("scalar header error = %v", err)
	}

	response, err := EncodeResponseStartAttributes(http.StatusOK, http.Header{
		"Content-Type": []string{"text/event-stream"},
		"Set-Cookie":   []string{"secret=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponseStartAttributes(response)
	if err != nil || decoded.Status != http.StatusOK || len(decoded.Headers) != 1 || decoded.Headers["content-type"][0] != "text/event-stream" {
		t.Fatalf("response attributes = %#v, error = %v", decoded, err)
	}
}

func TestDecodeRequestStartDeadlineSemantics(t *testing.T) {
	for name, test := range map[string]struct {
		deadline int64
		valid    bool
	}{
		"zero has no absolute deadline": {deadline: 0, valid: true},
		"positive is absolute deadline": {deadline: time.Now().Add(time.Minute).UnixMilli(), valid: true},
		"negative is invalid":           {deadline: -1, valid: false},
	} {
		t.Run(name, func(t *testing.T) {
			frame := validRequestStartFrame("request-1", 0)
			frame.Attributes["deadlineEpochMs"] = json.RawMessage(jsonInt64(test.deadline))
			attributes, err := DecodeRequestStartAttributes(frame.Attributes)
			if test.valid {
				if err != nil || attributes.DeadlineEpochMs != test.deadline {
					t.Fatalf("deadline = %d, attributes = %#v, error = %v", test.deadline, attributes, err)
				}
				return
			}
			if err != ErrTunnelProtocol {
				t.Fatalf("deadline = %d, error = %v", test.deadline, err)
			}
		})
	}
}

func TestLocalA2AProxyRequestStartDeadlineSemantics(t *testing.T) {
	for name, test := range map[string]struct {
		deadline    int64
		wantDeadline bool
		wantInvalid  bool
	}{
		"zero has no absolute deadline": {deadline: 0},
		"positive is absolute deadline": {deadline: time.Now().Add(time.Minute).UnixMilli(), wantDeadline: true},
		"negative is invalid":           {deadline: -1, wantInvalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			started := make(chan context.Context, 1)
			stopped := make(chan error, 1)
			proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{
				TargetURL: "http://127.0.0.1:8080/rpc",
				HTTPClient: contextCapturingDoer{started: started, stopped: stopped},
				MaxConcurrent: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			writer := newRecordingProxyWriter()
			start := validRequestStartFrame("request-1", 0)
			start.Attributes["deadlineEpochMs"] = json.RawMessage(jsonInt64(test.deadline))
			if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
				t.Fatal(err)
			}

			if test.wantInvalid {
				frames := writer.Frames()
				if len(frames) != 1 || frames[0].Type != FrameError || string(frames[0].Attributes["code"]) != `"frame_malformed"` {
					t.Fatalf("negative deadline frames = %#v", frames)
				}
				return
			}
			if frames := writer.Frames(); len(frames) != 0 {
				t.Fatalf("valid deadline produced error frames = %#v", frames)
			}

			var requestContext context.Context
			select {
			case requestContext = <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("local request did not start")
			}
			deadline, hasDeadline := requestContext.Deadline()
			if hasDeadline != test.wantDeadline {
				t.Fatalf("context deadline present = %v, want %v", hasDeadline, test.wantDeadline)
			}
			if test.wantDeadline && deadline.UnixMilli() != test.deadline {
				t.Fatalf("context deadline = %d, want %d", deadline.UnixMilli(), test.deadline)
			}

			cancel := validFrame(FrameCancel)
			cancel.RequestID = start.RequestID
			if err := proxy.HandleFrame(context.Background(), cancel, writer); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-stopped:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("local request stopped with %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("explicit cancel did not stop local request")
			}
			if proxy.InflightCount() != 0 {
				t.Fatalf("inflight = %d", proxy.InflightCount())
			}
			for _, frame := range writer.Frames() {
				if frame.Type == FrameError && string(frame.Attributes["code"]) == `"frame_malformed"` {
					t.Fatalf("valid deadline produced frame_malformed: %#v", frame)
				}
			}
		})
	}
}

func TestLocalA2AProxyStreamsResponseAndStripsSensitiveHeaders(t *testing.T) {
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local-rpc" || r.URL.RawQuery != "stream=true" || r.Header.Get("Authorization") != "Bearer local-secret" {
			t.Errorf("local request target or authorization mismatch")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Test") != "one" {
			t.Errorf("local request header filter mismatch")
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 5 {
			t.Errorf("local request body length = %d", len(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "must-not-tunnel")
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-releaseResponse
		_, _ = w.Write([]byte("second"))
	}))
	defer server.Close()

	proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{
		TargetURL:     server.URL + "/local-rpc",
		HTTPClient:    server.Client(),
		Authorization: staticLocalAuthorization("Bearer local-secret"),
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := newRecordingProxyWriter()
	start := validRequestStartFrame("request-1", 5)
	start.Attributes["headers"] = json.RawMessage(`{"accept":["text/event-stream"],"cookie":["secret"],"x-forwarded-for":["127.0.0.1"],"x-test":["one"]}`)
	if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
		t.Fatal(err)
	}
	chunk := validFrame(FrameRequestChunk)
	chunk.RequestID = "request-1"
	chunk.Payload = []byte("hello")
	if err := proxy.HandleFrame(context.Background(), chunk, writer); err != nil {
		t.Fatal(err)
	}
	end := validFrame(FrameRequestEnd)
	end.RequestID = "request-1"
	if err := proxy.HandleFrame(context.Background(), end, writer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.firstChunk:
	case <-time.After(2 * time.Second):
		t.Fatal("first response chunk was not forwarded before response end")
	}
	close(releaseResponse)
	select {
	case <-writer.responseEnd:
	case <-time.After(2 * time.Second):
		t.Fatal("response did not finish")
	}
	frames := writer.Frames()
	if len(frames) < 4 || frames[0].Type != FrameResponseStart || frames[1].Type != FrameResponseChunk || frames[len(frames)-1].Type != FrameResponseEnd {
		t.Fatalf("response frame types/count mismatch: count=%d", len(frames))
	}
	responseStart, err := DecodeResponseStartAttributes(frames[0].Attributes)
	if err != nil || responseStart.Headers["set-cookie"] != nil {
		t.Fatalf("response headers = %#v, error = %v", responseStart.Headers, err)
	}
}

func TestLocalA2AProxyCancelIsIdempotentAndCleansInflight(t *testing.T) {
	started := make(chan struct{})
	doer := cancelAwareDoer{started: started}
	proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{TargetURL: "http://127.0.0.1:8080/rpc", HTTPClient: doer, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	writer := newRecordingProxyWriter()
	start := validRequestStartFrame("request-1", 0)
	if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
		t.Fatal(err)
	}
	end := validFrame(FrameRequestEnd)
	end.RequestID = "request-1"
	if err := proxy.HandleFrame(context.Background(), end, writer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("local request did not start")
	}
	cancel := validFrame(FrameCancel)
	cancel.RequestID = "request-1"
	if err := proxy.HandleFrame(context.Background(), cancel, writer); err != nil {
		t.Fatal(err)
	}
	if err := proxy.HandleFrame(context.Background(), cancel, writer); err != nil {
		t.Fatal(err)
	}
	if proxy.InflightCount() != 0 {
		t.Fatalf("inflight = %d", proxy.InflightCount())
	}
}

func TestLocalA2AProxyLogsSafeCompletedRequestMetadata(t *testing.T) {
	responsePayload := []byte("response-body-secret")
	logs := captureLocalProxyConsole(t, func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			_, _ = io.ReadAll(request.Body)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(responsePayload)
		}))
		defer server.Close()

		proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{
			TargetURL: server.URL + "/local-rpc", HTTPClient: server.Client(),
			Authorization: staticLocalAuthorization("Bearer local-authorization-secret"), MaxConcurrent: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		writer := newRecordingProxyWriter()
		requestPayload := []byte("request-body-secret")
		start := validRequestStartFrame("request-sensitive-id", int64(len(requestPayload)))
		start.Attributes["path"] = json.RawMessage(`"/v1/a2a/local-runners/endpoint-sensitive/rpc"`)
		start.Attributes["query"] = json.RawMessage(`"sensitive-query=true"`)
		start.Attributes["headers"] = json.RawMessage(`{"authorization":["Bearer tunnel-secret"],"x-test":["sensitive-header-value"]}`)
		if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
			t.Fatal(err)
		}
		chunk := validFrame(FrameRequestChunk)
		chunk.RequestID = start.RequestID
		chunk.Payload = requestPayload
		if err := proxy.HandleFrame(context.Background(), chunk, writer); err != nil {
			t.Fatal(err)
		}
		end := validFrame(FrameRequestEnd)
		end.RequestID = start.RequestID
		if err := proxy.HandleFrame(context.Background(), end, writer); err != nil {
			t.Fatal(err)
		}
		select {
		case <-writer.responseEnd:
		case <-time.After(2 * time.Second):
			t.Fatal("response did not finish")
		}
		waitForLocalProxyIdle(t, proxy)
	})

	for _, want := range []string{
		"msg=localrunner.request.completed", "method=POST",
		"path=/v1/a2a/local-runners/{endpointId}/rpc", "status=202",
		fmt.Sprintf("responseBytes=%d", len(responsePayload)), "latencyMs=", "streaming=true",
		"outcome=completed", `errorCategory=""`, "requestIdHash=",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("completion log missing %q: %s", want, logs)
		}
	}
	for _, secret := range []string{
		"request-sensitive-id", "endpoint-sensitive", "sensitive-query", "sensitive-header-value",
		"tunnel-secret", "local-authorization-secret", "request-body-secret", "response-body-secret",
		"authorization", "headers", "query",
	} {
		if strings.Contains(strings.ToLower(logs), strings.ToLower(secret)) {
			t.Fatalf("completion log exposed forbidden value %q: %s", secret, logs)
		}
	}
}

func TestLocalA2AProxyLogsErrorsAndCancellationWithoutDetails(t *testing.T) {
	t.Run("request error", func(t *testing.T) {
		logs := captureLocalProxyConsole(t, func() {
			proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{
				TargetURL: "http://127.0.0.1:8080/rpc", HTTPClient: failingProxyDoer{}, MaxConcurrent: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			writer := newRecordingProxyWriter()
			start := validRequestStartFrame("request-error-secret", 0)
			if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
				t.Fatal(err)
			}
			end := validFrame(FrameRequestEnd)
			end.RequestID = start.RequestID
			if err := proxy.HandleFrame(context.Background(), end, writer); err != nil {
				t.Fatal(err)
			}
			waitForLocalProxyIdle(t, proxy)
		})
		for _, want := range []string{"outcome=error", "errorCategory=local_request_failed", "status=0", "responseBytes=0", "streaming=false"} {
			if !strings.Contains(logs, want) {
				t.Fatalf("error completion log missing %q: %s", want, logs)
			}
		}
		if strings.Contains(logs, "upstream-sensitive-error") || strings.Contains(logs, "request-error-secret") {
			t.Fatalf("error completion log exposed detail: %s", logs)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		logs := captureLocalProxyConsole(t, func() {
			started := make(chan struct{})
			proxy, err := NewLocalA2AProxy(LocalA2AProxyConfig{
				TargetURL: "http://127.0.0.1:8080/rpc", HTTPClient: cancelAwareDoer{started: started}, MaxConcurrent: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			writer := newRecordingProxyWriter()
			start := validRequestStartFrame("request-cancel-secret", 0)
			if err := proxy.HandleFrame(context.Background(), start, writer); err != nil {
				t.Fatal(err)
			}
			end := validFrame(FrameRequestEnd)
			end.RequestID = start.RequestID
			if err := proxy.HandleFrame(context.Background(), end, writer); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("local request did not start")
			}
			cancel := validFrame(FrameCancel)
			cancel.RequestID = start.RequestID
			if err := proxy.HandleFrame(context.Background(), cancel, writer); err != nil {
				t.Fatal(err)
			}
		})
		for _, want := range []string{"outcome=canceled", "errorCategory=canceled", "status=0", "responseBytes=0", "streaming=false"} {
			if !strings.Contains(logs, want) {
				t.Fatalf("cancel completion log missing %q: %s", want, logs)
			}
		}
		if strings.Contains(logs, "request-cancel-secret") {
			t.Fatalf("cancel completion log exposed request id: %s", logs)
		}
	})
}

type cancelAwareDoer struct {
	started chan struct{}
}

func (d cancelAwareDoer) Do(request *http.Request) (*http.Response, error) {
	close(d.started)
	<-request.Context().Done()
	return nil, request.Context().Err()
}

type contextCapturingDoer struct {
	started chan context.Context
	stopped chan error
}

func (d contextCapturingDoer) Do(request *http.Request) (*http.Response, error) {
	d.started <- request.Context()
	<-request.Context().Done()
	d.stopped <- request.Context().Err()
	return nil, request.Context().Err()
}

type failingProxyDoer struct{}

func (failingProxyDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("upstream-sensitive-error")
}

func captureLocalProxyConsole(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writer
	defer func() {
		os.Stderr = previous
		_ = writer.Close()
		_ = reader.Close()
	}()
	run()
	os.Stderr = previous
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func waitForLocalProxyIdle(t *testing.T, proxy *LocalA2AProxy) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for proxy.InflightCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("proxy still has %d inflight request(s)", proxy.InflightCount())
		}
		time.Sleep(time.Millisecond)
	}
}

func validRequestStartFrame(requestID string, contentLength int64) TunnelFrame {
	frame := validFrame(FrameRequestStart)
	frame.RequestID = requestID
	frame.Attributes = map[string]json.RawMessage{
		"method":          json.RawMessage(`"POST"`),
		"path":            json.RawMessage(`"/rpc"`),
		"query":           json.RawMessage(`"stream=true"`),
		"headers":         json.RawMessage(`{"accept":["application/json"]}`),
		"contentLength":   json.RawMessage(jsonInt64(contentLength)),
		"deadlineEpochMs": json.RawMessage(jsonInt64(time.Now().Add(time.Minute).UnixMilli())),
	}
	return frame
}

func jsonInt64(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

type staticLocalAuthorization string

func (a staticLocalAuthorization) ApplyLocalAuthorization(_ context.Context, header http.Header) error {
	header.Set("Authorization", string(a))
	return nil
}

type recordingProxyWriter struct {
	mu          sync.Mutex
	frames      []TunnelFrame
	firstChunk  chan struct{}
	responseEnd chan struct{}
	chunkOnce   sync.Once
	endOnce     sync.Once
}

func newRecordingProxyWriter() *recordingProxyWriter {
	return &recordingProxyWriter{firstChunk: make(chan struct{}), responseEnd: make(chan struct{})}
}

func (w *recordingProxyWriter) WriteFrame(_ context.Context, frame TunnelFrame) error {
	w.mu.Lock()
	w.frames = append(w.frames, frame)
	w.mu.Unlock()
	if frame.Type == FrameResponseChunk {
		w.chunkOnce.Do(func() { close(w.firstChunk) })
	}
	if frame.Type == FrameResponseEnd {
		w.endOnce.Do(func() { close(w.responseEnd) })
	}
	return nil
}

func (w *recordingProxyWriter) Frames() []TunnelFrame {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]TunnelFrame(nil), w.frames...)
}
