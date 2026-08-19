package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
		return nil
	}
	requestContext, cancel := context.WithDeadline(parent, time.UnixMilli(attributes.DeadlineEpochMs))
	bodyReader, bodyWriter := io.Pipe()
	request := &localProxyRequest{
		ctx:           requestContext,
		cancel:        cancel,
		bodyWriter:    bodyWriter,
		writer:        writer,
		contentLength: attributes.ContentLength,
	}
	p.mu.Lock()
	if len(p.inflight) >= p.maxConcurrent || p.inflight[frame.RequestID] != nil {
		p.mu.Unlock()
		cancel()
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
		_ = writer.WriteFrame(parent, proxyErrorFrame(frame.RequestID, "session_conflict", "Local runner request capacity is unavailable", true))
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
	request.cancel()
	_ = request.bodyWriter.CloseWithError(context.Canceled)
	return nil
}

func (p *LocalA2AProxy) execute(requestID string, inflight *localProxyRequest, attributes *RequestStartAttributes, body io.ReadCloser) {
	defer body.Close()
	defer inflight.cancel()
	defer p.removeIfSame(requestID, inflight)

	target := *p.target
	target.RawQuery = attributes.Query
	req, err := http.NewRequestWithContext(inflight.ctx, attributes.Method, target.String(), body)
	if err != nil {
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
	responseAttributes, err := EncodeResponseStartAttributes(response.StatusCode, response.Header)
	if err != nil || !p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseStart, RequestID: requestID, Attributes: responseAttributes}) {
		return
	}
	buffer := make([]byte, 32<<10)
	var responseBytes int64
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			responseBytes += int64(count)
			if responseBytes > p.maxBodyBytes {
				p.writeProxyErrorIfActive(requestID, inflight, "frame_too_large", "Local A2A response exceeds its limit", false)
				return
			}
			if !p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseChunk, RequestID: requestID, Payload: append([]byte(nil), buffer[:count]...)}) {
				return
			}
		}
		if readErr == io.EOF {
			p.writeIfActive(requestID, inflight, TunnelFrame{Type: FrameResponseEnd, RequestID: requestID})
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
		request.cancel()
		_ = request.bodyWriter.CloseWithError(ErrTunnelProtocol)
	}
}

func (p *LocalA2AProxy) FailRequest(requestID string, _ error) {
	request := p.remove(requestID)
	if request != nil {
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
