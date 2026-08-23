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

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	localRunnerA2AStreamFlushInterval = 100 * time.Millisecond
	localRunnerA2AStreamHeartbeatInterval = 15 * time.Second
	localRunnerA2AStreamMaxTextBytes = 1 << 20
	localRunnerA2AStreamMaxArtifactEvents = 1024
)

var errLocalRunnerA2AStreamLimit = errors.New("local agent streaming output limit exceeded")

type localRunnerA2AStreamResult struct {
	reply string
	err error
}

func localRunnerA2AStreamingCall(ctx context.Context) bool {
	callContext, ok := a2asrv.CallContextFrom(ctx)
	return ok && callContext.Method() == "SendStreamingMessage"
}

func localRunnerArtifactDelta(previous, next string) (string, bool) {
	if previous != "" && strings.HasPrefix(next, previous) {
		return strings.TrimPrefix(next, previous), true
	}
	return next, false
}

func (e *localRunnerA2AExecutor) executeStream(ctx context.Context, execCtx *a2asrv.ExecutorContext, contextID, prompt string, yield func(a2a.Event, error) bool) {
	streamContext, cancel := context.WithCancel(ctx)
	defer cancel()

	task := a2a.NewSubmittedTask(execCtx, execCtx.Message)
	if !yield(task, nil) {
		return
	}
	if !yield(a2a.NewStatusUpdateEvent(task, a2a.TaskStateWorking, nil), nil) {
		return
	}

	snapshots := make(chan string, 32)
	result := make(chan localRunnerA2AStreamResult, 1)
	go func() {
		reply, err := e.backend.Stream(streamContext, contextID, prompt, func(snapshot string) {
			if strings.TrimSpace(snapshot) == "" {
				return
			}
			select {
			case snapshots <- snapshot:
			default:
				select {
				case <-snapshots:
				default:
				}
				select {
				case snapshots <- snapshot:
				default:
				}
			}
		})
		result <- localRunnerA2AStreamResult{reply: reply, err: err}
	}()

	heartbeat := time.NewTicker(localRunnerA2AStreamHeartbeatInterval)
	defer heartbeat.Stop()
	var flushTimer *time.Timer
	var flush <-chan time.Time
	var pending string
	var emitted string
	var artifactID a2a.ArtifactID
	artifactEvents := 0

	stopFlushTimer := func() {
		if flushTimer != nil && !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
		flush = nil
	}
	scheduleFlush := func() {
		if flush != nil {
			return
		}
		if flushTimer == nil {
			flushTimer = time.NewTimer(localRunnerA2AStreamFlushInterval)
		} else {
			flushTimer.Reset(localRunnerA2AStreamFlushInterval)
		}
		flush = flushTimer.C
	}
	emitSnapshot := func(snapshot string, last bool) error {
		if snapshot == emitted || strings.TrimSpace(snapshot) == "" {
			return nil
		}
		if len(snapshot) > localRunnerA2AStreamMaxTextBytes || artifactEvents >= localRunnerA2AStreamMaxArtifactEvents {
			return errLocalRunnerA2AStreamLimit
		}
		text, appendPart := localRunnerArtifactDelta(emitted, snapshot)
		if text == "" {
			return nil
		}
		var event *a2a.TaskArtifactUpdateEvent
		if artifactID == "" {
			event = a2a.NewArtifactEvent(task, a2a.NewTextPart(text))
			artifactID = event.Artifact.ID
		} else {
			event = a2a.NewArtifactUpdateEvent(task, artifactID, a2a.NewTextPart(text))
			event.Append = appendPart
		}
		event.LastChunk = last
		if !yield(event, nil) {
			return context.Canceled
		}
		localRunnerLogA2AMessage(ctx, "localrunner.a2a.message.outbound", e.harness, "stream", text, contextID, execCtx.Message.ID, artifactEvents+1, appendPart, last)
		emitted = snapshot
		artifactEvents++
		return nil
	}
	fail := func(err error) {
		localRunnerLogBackendFailure(ctx, e.harness, "stream", prompt, contextID, err)
		state := a2a.TaskStateFailed
		message := "Local agent request failed"
		if errors.Is(err, context.Canceled) {
			state = a2a.TaskStateCanceled
			message = "Local agent request canceled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			message = "Local agent request timed out"
		}
		statusMessage := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(message))
		statusMessage.ContextID = task.ContextID
		yield(a2a.NewStatusUpdateEvent(task, state, statusMessage), nil)
	}
	flushPending := func(last bool) bool {
		if pending == "" {
			return true
		}
		snapshot := pending
		pending = ""
		if err := emitSnapshot(snapshot, last); err != nil {
			if !errors.Is(err, context.Canceled) {
				fail(err)
			}
			return false
		}
		return true
	}
	acceptSnapshot := func(snapshot string) bool {
		if emitted == "" {
			if err := emitSnapshot(snapshot, false); err != nil {
				if !errors.Is(err, context.Canceled) {
					fail(err)
				}
				return false
			}
			return true
		}
		pending = snapshot
		scheduleFlush()
		return true
	}

	for {
		select {
		case snapshot := <-snapshots:
			if !acceptSnapshot(snapshot) {
				return
			}
		case <-flush:
			flush = nil
			if !flushPending(false) {
				return
			}
		case <-heartbeat.C:
			if pending != "" {
				if !flushPending(false) {
					return
				}
				continue
			}
			if !yield(a2a.NewStatusUpdateEvent(task, a2a.TaskStateWorking, nil), nil) {
				return
			}
		case completed := <-result:
			stopFlushTimer()
			for {
				select {
				case snapshot := <-snapshots:
					if !acceptSnapshot(snapshot) {
						return
					}
				default:
					goto snapshotsDrained
				}
			}
		snapshotsDrained:
			stopFlushTimer()
			if completed.err != nil {
				fail(completed.err)
				return
			}
			if strings.TrimSpace(completed.reply) == "" {
				fail(errors.New("empty backend reply"))
				return
			}
			pending = completed.reply
			if !flushPending(true) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(task, a2a.TaskStateCompleted, nil), nil)
			return
		case <-ctx.Done():
			fail(ctx.Err())
			return
		}
	}
}
