package helpers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
	"github.com/spf13/cobra"
)

type recordingLocalAgentForwarder struct {
	calls      []string
	closeCalls int
}

func (f *recordingLocalAgentForwarder) forward(_ context.Context, sessionKey, prompt string) (string, error) {
	f.calls = append(f.calls, "prompt:"+sessionKey+":"+prompt)
	return "final reply", nil
}

func (f *recordingLocalAgentForwarder) label() string { return "recording" }

func (f *recordingLocalAgentForwarder) forwardStream(_ context.Context, sessionKey, prompt string, onDelta func(string)) (string, error) {
	f.calls = append(f.calls, "stream:"+sessionKey+":"+prompt)
	if onDelta != nil {
		onDelta("final reply")
	}
	return "final reply", nil
}

func (f *recordingLocalAgentForwarder) canStream() bool { return true }

func (f *recordingLocalAgentForwarder) forwardWithAttachments(_ context.Context, sessionKey, prompt string, attachments []connectMediaAttachment) (string, error) {
	if len(attachments) == 0 {
		f.calls = append(f.calls, "prompt:"+sessionKey+":"+prompt)
		return "final reply", nil
	}
	f.calls = append(f.calls, "attachments:"+sessionKey+":"+prompt+":"+attachments[0].FileName)
	return "attachment reply", nil
}

func (f *recordingLocalAgentForwarder) forwardStreamWithAttachments(_ context.Context, sessionKey, prompt string, attachments []connectMediaAttachment, onDelta func(string)) (string, error) {
	if len(attachments) > 0 {
		f.calls = append(f.calls, "attachments:"+sessionKey+":"+prompt+":"+attachments[0].FileName)
		return "attachment reply", nil
	}
	f.calls = append(f.calls, "stream:"+sessionKey+":"+prompt)
	if onDelta != nil {
		onDelta("final reply")
	}
	return "final reply", nil
}

func (f *recordingLocalAgentForwarder) close() error {
	f.closeCalls++
	return nil
}

func TestLocalAgentBackendUsesConnectRegistryForwarderAndOwnsLifecycle(t *testing.T) {
	fake := &recordingLocalAgentForwarder{}
	var capturedChannel string
	var capturedClientID string
	var captured connectAgentOptions
	testseam.Swap(t, &localAgentForwarderFactory, func(channel, clientID string, options connectAgentOptions) (forwarder, error) {
		capturedChannel = channel
		capturedClientID = clientID
		captured = options
		return fake, nil
	})

	backend, err := StartLocalAgentBackend(context.Background(), LocalAgentBackendOptions{
		Channel: "opencode", ClientID: "runner-1", WorkDir: "/tmp/project", Model: "provider/model",
		Memory: true, Yolo: true, Timeout: 17 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedChannel != "opencode" || capturedClientID != "runner-1" || captured.WorkDir != "/tmp/project" || captured.Model != "provider/model" || !captured.Memory || !captured.Yolo || captured.Timeout != 17*time.Second {
		t.Fatalf("shared backend factory channel=%q clientID=%q options=%#v", capturedChannel, capturedClientID, captured)
	}
	if reply, err := backend.Prompt(context.Background(), "context-1", "question"); err != nil || reply != "final reply" {
		t.Fatalf("Prompt() reply=%q error=%v", reply, err)
	}
	var deltas []string
	if reply, err := backend.Stream(context.Background(), "context-2", "stream question", func(delta string) { deltas = append(deltas, delta) }); err != nil || reply != "final reply" || strings.Join(deltas, "") != "final reply" {
		t.Fatalf("Stream() reply=%q deltas=%v error=%v", reply, deltas, err)
	}
	if reply, err := backend.PromptWithAttachments(context.Background(), "context-3", "inspect", []LocalAgentAttachment{{LocalPath: "/tmp/image.png", FileName: "image.png", MediaType: "image/png"}}); err != nil || reply != "attachment reply" {
		t.Fatalf("PromptWithAttachments() reply=%q error=%v", reply, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("forwarder close calls = %d, want 1", fake.closeCalls)
	}
}

func TestLocalAgentBackendSupportsConnectRegistryAndExcludesGeminiOnlyFromLocalRunner(t *testing.T) {
	want := []string{"claudecode", "codebuddy", "codex", "custom", "gemini", "opencode", "qoder", "qoderwork", "workbuddy"}
	if got := strings.Join(LocalAgentBackendChannels(), ","); got != strings.Join(want, ",") {
		t.Fatalf("LocalAgentBackendChannels() = %q, want %q", got, strings.Join(want, ","))
	}
	if IsLocalRunnerAgentChannel("gemini") {
		t.Fatal("gemini remote API backend was exposed as a LocalRunner process channel")
	}
	fake := &recordingLocalAgentForwarder{}
	testseam.Swap(t, &localAgentForwarderFactory, func(channel, _ string, _ connectAgentOptions) (forwarder, error) {
		if channel != "gemini" {
			t.Fatalf("factory channel = %q, want gemini", channel)
		}
		return fake, nil
	})
	backend, err := StartLocalAgentBackend(context.Background(), LocalAgentBackendOptions{Channel: "gemini"})
	if err != nil || backend == nil {
		t.Fatalf("dev connect gemini shared backend = %v, error=%v", backend, err)
	}
	if _, err := StartLocalAgentBackend(context.Background(), LocalAgentBackendOptions{Channel: "unknown"}); !errors.Is(err, ErrLocalAgentBackendUnsupported) {
		t.Fatalf("unknown backend error = %v, want ErrLocalAgentBackendUnsupported", err)
	}
}

func TestDevConnectCreatesBackendThroughSharedLifecycleFactory(t *testing.T) {
	fake := &recordingLocalAgentForwarder{}
	backend := &LocalAgentBackend{channel: "custom", forwarder: fake}
	var captured LocalAgentBackendOptions
	testseam.Swap(t, &devAppStartLocalAgentBackend, func(_ context.Context, options LocalAgentBackendOptions) (*LocalAgentBackend, error) {
		captured = options
		return backend, nil
	})
	var streamed forwarder
	testseam.Swap(t, &devAppRunStreamConnector, func(_ context.Context, _, _, _ string, fwd forwarder, _ *aiCardClient, _ *connectExtras) error {
		streamed = fwd
		return nil
	})

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	options := connectAgentOptions{WorkDir: "/tmp/project", Model: "model", Memory: true, Yolo: true, Timeout: 3 * time.Second}
	if err := launchConnector(cmd, nil, "custom", "client-1", "secret", options); err != nil {
		t.Fatal(err)
	}
	if captured.Channel != "custom" || captured.ClientID != "client-1" || captured.WorkDir != options.WorkDir || captured.Model != options.Model || !captured.Memory || !captured.Yolo || captured.Timeout != options.Timeout {
		t.Fatalf("dev connect shared backend options = %#v", captured)
	}
	if streamed != backend {
		t.Fatalf("stream connector forwarder = %T, want shared backend", streamed)
	}
}
