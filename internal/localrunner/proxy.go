package localrunner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultProxyBodyLimit = int64(16 << 20)

var ErrLocalProxyInvalid = errors.New("local_a2a_proxy_invalid")

type LocalAuthorizationProvider interface {
	ApplyLocalAuthorization(context.Context, http.Header) error
}

type LocalA2AProxyConfig struct {
	TargetURL      string
	HTTPClient     HTTPDoer
	Authorization LocalAuthorizationProvider
	MaxConcurrent  int
	MaxBodyBytes   int64
}

type LocalA2AProxy struct {
	target        *url.URL
	httpClient    HTTPDoer
	authorization LocalAuthorizationProvider
	maxConcurrent int
	maxBodyBytes  int64
	logger        *slog.Logger
	mu            sync.Mutex
	inflight      map[string]*localProxyRequest
}

type localProxyRequest struct {
	ctx             context.Context
	cancel          context.CancelFunc
	bodyWriter      *io.PipeWriter
	writer          TunnelFrameWriter
	contentLength   int64
	receivedBytes   int64
	requestIDHash   string
	method          string
	path            string
	startedAt       time.Time
	completed       bool
}

func NewLocalA2AProxy(config LocalA2AProxyConfig) (*LocalA2AProxy, error) {
	target, err := url.Parse(strings.TrimSpace(config.TargetURL))
	if err != nil || !validLoopbackHTTPURL(config.TargetURL) || config.HTTPClient == nil {
		return nil, ErrLocalProxyInvalid
	}
	maxConcurrent := config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultProxyBodyLimit
	}
	return &LocalA2AProxy{
		target:        target,
		httpClient:    config.HTTPClient,
		authorization: config.Authorization,
		maxConcurrent: maxConcurrent,
		maxBodyBytes:  maxBodyBytes,
		logger:        slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		inflight:      make(map[string]*localProxyRequest),
	}, nil
}

func (p *LocalA2AProxy) HandleFrame(ctx context.Context, frame TunnelFrame, writer TunnelFrameWriter) error {
	if p == nil || writer == nil || frame.RequestID == "" {
		return ErrLocalProxyInvalid
	}
	switch frame.Type {
	case FrameRequestStart:
		return p.start(ctx, frame, writer)
	case FrameRequestChunk:
		return p.chunk(frame)
	case FrameRequestEnd:
		return p.end(frame)
	case FrameCancel:
		return p.cancelRequest(frame.RequestID, frame.Attributes)
	case FrameError:
		p.FailRequest(frame.RequestID, ErrTunnelProtocol)
		return nil
	default:
		return ErrTunnelProtocol
	}
}

func (p *LocalA2AProxy) start(parent context.Context, frame TunnelFrame, writer TunnelFrameWriter) error {
	attributes, err := DecodeRequestStartAttributes(frame.Attributes)
	if err != nil || attributes.ContentLength > p.maxBodyBytes {
		_ = writer.WriteFrame(parent, proxyErrorFrame(frame.RequestID, "frame_malformed", "Tunnel request metadata is invalid", false))
		p.logCompleted(&localProxyRequest{
			requestIDHash: safeProxyRequestIDHash(frame.RequestID),
			method:        "OTHER",
			path:          "/other",
			startedAt:     time.Now(),
		}, 0, 0, false, "error", "frame_malformed")
		return nil
	}
	var requestContext context.Context
	var cancel context.CancelFunc
	if attributes.DeadlineEpochMs == 0 {
		requestContext, cancel = context.WithCancel(parent)
	} else {
		requestContext, cancel = context.WithDeadline(parent, time.UnixMilli(attributes.DeadlineEpochMs))
	}
	bodyReader, bodyWriter := io.Pipe()
	request := &localProxyRequest{
		ctx:           requestContext,
		cancel:        cancel,
		bodyWriter:    bodyWriter,
		writer:        writer,
		contentLength: attributes.ContentLength,
		requestIDHash: safeProxyRequestIDHash(frame.RequestID),
		method:        safeProxyLogMethod(attributes.Method),
		path:          safeProxyLogPath(attributes.Path),
		startedAt:     time.Now(),
	}
	p.mu.Lock()
	if len(p.inflight) >= p.maxConcurrent || p.inflight[frame.RequestID] != nil {
		p.mu.Unlock()
		cancel()
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		_ = writer.WriteFrame(parent, proxyErrorFrame(frame.RequestID, "session_conflict", "Local runner request capacity is unavailable", true))
		p.logCompleted(request, 0, 0, false, "error", "session_conflict")
		return nil
	}
	p.inflight[frame.RequestID] = request
	p.mu.Unlock()
	go p.execute(frame.RequestID, request, attributes, bodyReader)
	return nil
}

func (p *LocalA2AProxy) chunk(frame TunnelFrame) error {
	request := p.get(frame.RequestID)
	if request == nil {
		return nil
	}
	p.mu.Lock()
	request.receivedBytes += int64(len(frame.Payload))
	overLimit := request.receivedBytes > p.maxBodyBytes || request.receivedBytes > request.contentLength
	p.mu.Unlock()
	if overLimit {
		p.failWithFrame(frame.RequestID, request, "frame_too_large", "Local A2A request body exceeds its declared limit", false)
		return nil
	}
	if _, err := request.bodyWriter.Write(frame.Payload); err != nil && request.ctx.Err() == nil {
		p.failWithFrame(frame.RequestID, request, "frame_malformed", "Local A2A request body could not be forwarded", true)
	}
	return nil
}

func (p *LocalA2AProxy) end(frame TunnelFrame) error {
	if len(frame.Attributes) != 0 {
		request := p.get(frame.RequestID)
		if request != nil {
			p.failWithFrame(frame.RequestID, request, "frame_malformed", "Tunnel request end is invalid", false)
		}
		return nil
	}
	request := p.get(frame.RequestID)
	if request == nil {
		return nil
	}
	p.mu.Lock()
	matches := request.receivedBytes == request.contentLength
	p.mu.Unlock()
	if !matches {
		p.failWithFrame(frame.RequestID, request, "frame_malformed", "Local A2A request length does not match", false)
		return nil
	}
	_ = request.bodyWriter.Close()
	return nil
}

func (p *LocalA2AProxy) cancelRequest(requestID string, attributes map[string]json.RawMessage) error {
	if len(attributes) != 0 {
		return nil
	}
	request := p.remove(requestID)
	if request == nil {
		return nil
	}
	p.logCompleted(request, 0, 0, false, "canceled", "canceled")
	request.cancel()
	_ = request.bodyWriter.CloseWithError(context.Canceled)
	return nil
}

func (p *LocalA2AProxy) execute(requestID string, inflight *localProxyRequest, attributes *RequestStartAttributes, body io.ReadCloser) {
	defer p.removeIfSame(requestID, inflight)
	defer inflight.cancel()
	defer body.Close()
	status := 0
	var responseBytes int64
	streaming := false
	outcome := "error"
	errorCategory := "local_request_failed"
	defer func() {
		if inflight.ctx.Err() != nil && outcome != "completed" {
			outcome = "canceled"
			errorCategory = "canceled"
			if errors.Is(inflight.ctx.Err(), context.DeadlineExceeded) {
				outcome = "error"
				errorCategory = "deadline_exceeded"
			}
		}
		p.logCompleted(inflight, status, responseBytes, streaming, outcome, errorCategory)
	}()

	target := *p.target
	target.RawQuery = attributes.Query
	req, err := http.NewRequestWithContext(inflight.ctx, attributes.Method, target.String(), body)
	if err != nil {
		errorCategory = "frame_malformed"
		p.writeProxyErrorIfActive(requestID, inflight, "frame_malformed", "Local A2A request could not be created", false)
		return
	}
	req.ContentLength = attributes.ContentLength
	for name, values := range attributes.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if p.authorization != nil {
		if err := p.authorization.ApplyLocalAuthorization(inflight.ctx, req.Header); err != nil {
			errorCategory = "local_authorization_failed"
			p.writeProxyErrorIfActive(requestID, inflight, "local_authorization_failed", "Local authorization is unavailable", false)
			return
		}
	}
	response, err := p.httpClient.Do(req)
	if err != nil {
		if inflight.ctx.Err() == nil {
			p.writeProxyErrorIfActive(requestID, inflight, "local_request_failed", "Local A2A request failed", true)
		}
		return
	}
	defer response.Body.Close()
	status = response.StatusCode
	streaming = proxyResponseIsStreaming(response.Header)
	responseAttributes, err := EncodeResponseStartAttributes(response.StatusCode, response.Header)
	if err != nil {
		errorCategory = "local_response_invalid"
		return
	}
	if !p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseStart, RequestID: requestID, Attributes: responseAttributes}) {
		errorCategory = "tunnel_write_failed"
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			responseBytes += int64(count)
			if responseBytes > p.maxBodyBytes {
				errorCategory = "frame_too_large"
				p.writeProxyErrorIfActive(requestID, inflight, "frame_too_large", "Local A2A response exceeds its limit", false)
				return
			}
			if !p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseChunk, RequestID: requestID, Payload: append([]byte(nil), buffer[:count]...)}) {
				errorCategory = "tunnel_write_failed"
				return
			}
		}
		if readErr == io.EOF {
			if p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseEnd, RequestID: requestID}) {
				outcome = "completed"
				errorCategory = ""
			} else {
				errorCategory = "tunnel_write_failed"
			}
			return
		}
		if readErr != nil {
			if inflight.ctx.Err() == nil {
				p.writeProxyErrorIfActive(requestID, inflight, "local_request_failed", "Local A2A response failed", true)
			}
			return
		}
	}
}

func (p *LocalA2AProxy) writeIfActive(requestID string, request *localProxyRequest, frame TunnelFrame) bool {
	if !p.isActive(requestID, request) {
		return false
	}
	return request.writer.WriteFrame(request.ctx, frame) == nil
}

func (p *LocalA2AProxy) writeProxyErrorIfActive(requestID string, request *localProxyRequest, code, message string, retryable bool) {
	p.writeIfActive(requestID, request, proxyErrorFrame(requestID, code, message, retryable))
}

func proxyErrorFrame(requestID, code, message string, retryable bool) TunnelFrame {
	codeJSON, _ := json.Marshal(code)
	messageJSON, _ := json.Marshal(message)
	retryableJSON, _ := json.Marshal(retryable)
	return TunnelFrame{
		Type:      FrameError,
		RequestID: requestID,
		Attributes: map[string]json.RawMessage{
			"code":      codeJSON,
			"message":   messageJSON,
			"retryable": retryableJSON,
		},
	}
}

func (p *LocalA2AProxy) failWithFrame(requestID string, request *localProxyRequest, code, message string, retryable bool) {
	p.writeProxyErrorIfActive(requestID, request, code, message, retryable)
	removed := p.removeIfSame(requestID, request)
	if removed {
		p.logCompleted(request, 0, 0, false, "error", code)
		request.cancel()
		_ = request.bodyWriter.CloseWithError(ErrTunnelProtocol)
	}
}

func (p *LocalA2AProxy) FailRequest(requestID string, _ error) {
	request := p.remove(requestID)
	if request != nil {
		p.logCompleted(request, 0, 0, false, "error", "tunnel_protocol")
		request.cancel()
		_ = request.bodyWriter.CloseWithError(ErrTunnelProtocol)
	}
}

func (p *LocalA2AProxy) FailAll(_ error) {
	p.mu.Lock()
	requests := make([]*localProxyRequest, 0, len(p.inflight))
	for _, request := range p.inflight {
		requests = append(requests, request)
	}
	p.inflight = make(map[string]*localProxyRequest)
	p.mu.Unlock()
	for _, request := range requests {
		p.logCompleted(request, 0, 0, false, "error", "tunnel_disconnected")
		request.cancel()
		_ = request.bodyWriter.CloseWithError(ErrTunnelDisconnected)
	}
}

func (p *LocalA2AProxy) InflightCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inflight)
}

func (p *LocalA2AProxy) get(requestID string) *localProxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight[requestID]
}

func (p *LocalA2AProxy) isActive(requestID string, request *localProxyRequest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight[requestID] == request
}

func (p *LocalA2AProxy) remove(requestID string) *localProxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := p.inflight[requestID]
	delete(p.inflight, requestID)
	return request
}

func (p *LocalA2AProxy) removeIfSame(requestID string, request *localProxyRequest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inflight[requestID] != request {
		return false
	}
	delete(p.inflight, requestID)
	return true
}

func (p *LocalA2AProxy) logCompleted(request *localProxyRequest, status int, responseBytes int64, streaming bool, outcome, errorCategory string) {
	p.mu.Lock()
	if request.completed {
		p.mu.Unlock()
		return
	}
	request.completed = true
	p.mu.Unlock()
	latency := time.Since(request.startedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	p.logger.Info("localrunner.request.completed",
		"requestIdHash", request.requestIDHash,
		"method", request.method,
		"path", request.path,
		"status", status,
		"responseBytes", responseBytes,
		"latencyMs", latency,
		"streaming", streaming,
		"outcome", outcome,
		"errorCategory", errorCategory,
	)
}

func safeProxyRequestIDHash(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("%x", digest[:8])
}

func safeProxyLogMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || len(method) > 16 {
		return "OTHER"
	}
	for _, character := range method {
		if character < 'A' || character > 'Z' {
			return "OTHER"
		}
	}
	return method
}

func safeProxyLogPath(requestPath string) string {
	if requestPath == "/rpc" {
		return "/rpc"
	}
	parts := strings.Split(requestPath, "/")
	if len(parts) == 6 && parts[0] == "" && parts[1] == "v1" && parts[2] == "a2a" && parts[3] == "local-runners" && parts[4] != "" && parts[5] == "rpc" {
		return "/v1/a2a/local-runners/{endpointId}/rpc"
	}
	return "/other"
}

func proxyResponseIsStreaming(header http.Header) bool {
	contentType := header.Get("Content-Type")
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = contentType[:separator]
	}
	return strings.EqualFold(strings.TrimSpace(contentType), "text/event-stream")
}
