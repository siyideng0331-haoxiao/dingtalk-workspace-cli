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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var openCodeLocalLocateBinary = func() (string, bool) {
	spec := agentSpecs["opencode"]
	return locateBinary(spec.bins, spec.globs)
}

var (
	ErrOpenCodeLocalInvalid      = errors.New("opencode local configuration is invalid")
	ErrOpenCodeLocalUnavailable  = errors.New("opencode is unavailable")
	ErrOpenCodeLocalPromptFailed = errors.New("opencode prompt failed")
	ErrOpenCodeLocalEmptyReply   = errors.New("opencode returned no text")
)

type OpenCodeLocalOptions struct {
	WorkDir string
	Model   string
	Timeout time.Duration
}

type OpenCodeLocal struct {
	forwarder *opencodeForwarder
	closeOnce sync.Once
	closeErr  error
}

func StartOpenCodeLocal(ctx context.Context, options OpenCodeLocalOptions) (*OpenCodeLocal, error) {
	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" || workDir != options.WorkDir || !filepath.IsAbs(workDir) || filepath.Clean(workDir) != workDir {
		return nil, ErrOpenCodeLocalInvalid
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return nil, ErrOpenCodeLocalInvalid
	}

	bin, ok := openCodeLocalLocateBinary()
	if !ok {
		return nil, ErrOpenCodeLocalUnavailable
	}
	forwarder := newOpencodeForwarder(bin, nil, options.Timeout, connectAgentOptions{
		Memory:  true,
		Model:   strings.TrimSpace(options.Model),
		WorkDir: workDir,
		Yolo:    true,
	}, "").(*opencodeForwarder)
	forwarder.server.output = io.Discard
	if _, err := forwarder.server.ensure(ctx); err != nil {
		_ = forwarder.close()
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrOpenCodeLocalUnavailable
	}
	return &OpenCodeLocal{forwarder: forwarder}, nil
}

func (l *OpenCodeLocal) Prompt(ctx context.Context, sessionKey, prompt string) (string, error) {
	if l == nil || l.forwarder == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", ErrOpenCodeLocalInvalid
	}
	reply, err := l.forwarder.forwardRaw(ctx, sessionKey, prompt)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", ErrOpenCodeLocalPromptFailed
	}
	if agentReplyIsError(reply) {
		return "", ErrOpenCodeLocalPromptFailed
	}
	if strings.TrimSpace(reply) == "" {
		return "", ErrOpenCodeLocalEmptyReply
	}
	return reply, nil
}

func (l *OpenCodeLocal) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		if l.forwarder != nil {
			l.closeErr = l.forwarder.close()
		}
	})
	return l.closeErr
}
