package localrunner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

type cancelAwareDoer struct {
	started chan struct{}
}

func (d cancelAwareDoer) Do(request *http.Request) (*http.Response, error) {
	close(d.started)
	<-request.Context().Done()
	return nil, request.Context().Err()
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
