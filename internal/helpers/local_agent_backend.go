// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helpers

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrLocalAgentBackendInvalid = errors.New("local agent backend configuration is invalid")
	ErrLocalAgentBackendUnsupported = errors.New("local agent backend channel is unsupported")
)

type LocalAgentBackendOptions struct {
	Channel  string
	ClientID string
	AgentCommand string
	WorkDir  string
	Model    string
	Memory   bool
	Yolo     bool
	Timeout  time.Duration
}

type LocalAgentAttachment struct {
	LocalPath string
	FileName  string
	MediaType string
}

type LocalAgentBackend struct {
	channel   string
	forwarder forwarder
	closeOnce sync.Once
	closeErr  error
}

var localAgentForwarderFactory = forwarderForChannel

func LocalAgentBackendChannels() []string {
	channels := make([]string, 0, len(agentSpecs))
	for channel := range agentSpecs {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	return channels
}

func IsLocalAgentBackendChannel(channel string) bool {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return false
	}
	_, ok := agentSpecs[channel]
	return ok
}

func LocalRunnerAgentChannels() []string {
	channels := LocalAgentBackendChannels()
	result := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel != "gemini" {
			result = append(result, channel)
		}
	}
	return result
}

func IsLocalRunnerAgentChannel(channel string) bool {
	return strings.ToLower(strings.TrimSpace(channel)) != "gemini" && IsLocalAgentBackendChannel(channel)
}

func StartLocalAgentBackend(ctx context.Context, options LocalAgentBackendOptions) (*LocalAgentBackend, error) {
	channel := strings.ToLower(strings.TrimSpace(options.Channel))
	if !IsLocalAgentBackendChannel(channel) {
		return nil, ErrLocalAgentBackendUnsupported
	}
	if ctx == nil || options.Timeout < 0 {
		return nil, ErrLocalAgentBackendInvalid
	}
	fwd, err := localAgentForwarderFactory(channel, strings.TrimSpace(options.ClientID), connectAgentOptions{
		AgentCommand: strings.TrimSpace(options.AgentCommand), Model: strings.TrimSpace(options.Model), WorkDir: strings.TrimSpace(options.WorkDir),
		Memory: options.Memory, Yolo: options.Yolo, Timeout: options.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &LocalAgentBackend{channel: channel, forwarder: fwd}, nil
}

func (b *LocalAgentBackend) Prompt(ctx context.Context, sessionKey, prompt string) (string, error) {
	if b == nil || b.forwarder == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", ErrLocalAgentBackendInvalid
	}
	return forwardConnectTurn(ctx, b.forwarder, sessionKey, prompt, nil, nil)
}

func (b *LocalAgentBackend) Stream(ctx context.Context, sessionKey, prompt string, onDelta func(string)) (string, error) {
	if b == nil || b.forwarder == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", ErrLocalAgentBackendInvalid
	}
	return forwardConnectTurn(ctx, b.forwarder, sessionKey, prompt, nil, onDelta)
}

func (b *LocalAgentBackend) PromptWithAttachments(ctx context.Context, sessionKey, prompt string, attachments []LocalAgentAttachment) (string, error) {
	if b == nil || b.forwarder == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", ErrLocalAgentBackendInvalid
	}
	converted := make([]connectMediaAttachment, len(attachments))
	for index := range attachments {
		converted[index] = connectMediaAttachment{
			LocalPath: attachments[index].LocalPath,
			FileName: attachments[index].FileName,
			MediaType: attachments[index].MediaType,
		}
	}
	return forwardConnectTurn(ctx, b.forwarder, sessionKey, prompt, converted, nil)
}

func (b *LocalAgentBackend) forward(ctx context.Context, sessionKey, prompt string) (string, error) {
	return b.Prompt(ctx, sessionKey, prompt)
}

func (b *LocalAgentBackend) label() string {
	if b == nil || b.forwarder == nil {
		return "local-agent:unavailable"
	}
	return b.forwarder.label()
}

func (b *LocalAgentBackend) close() error {
	return b.Close()
}

func (b *LocalAgentBackend) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		if closer, ok := b.forwarder.(forwarderCloser); ok {
			b.closeErr = closer.close()
		}
	})
	return b.closeErr
}

func unwrapLocalAgentForwarder(fwd forwarder) forwarder {
	if backend, ok := fwd.(*LocalAgentBackend); ok && backend != nil && backend.forwarder != nil {
		return backend.forwarder
	}
	return fwd
}
