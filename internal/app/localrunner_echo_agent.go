package app

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

const (
	localRunnerTestEchoRef          = "test-echo"
	localRunnerTestEchoDisplayName  = "DWS Test Echo"
	localRunnerTestEchoAgentVersion = localRunnerOpenCodeAgentVersion
	localRunnerTestEchoCardPath     = localRunnerOpenCodeCardPath
	localRunnerTestEchoRPCPath      = localRunnerOpenCodeRPCPath
	localRunnerTestEchoMaxBodyBytes = localRunnerOpenCodeMaxBodyBytes
)

var localRunnerTestEchoAgentStarter = startLocalRunnerTestEchoAgent
var localRunnerTestEchoAgentRestarter = startLocalRunnerTestEchoAgentAt

type localRunnerBuiltInAgent = localRunnerOpenCodeAgent

type localRunnerTestEchoBackend struct{}

func (localRunnerTestEchoBackend) Prompt(_ context.Context, _ string, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("test_echo_prompt_invalid")
	}
	return prompt, nil
}

func (localRunnerTestEchoBackend) Stream(ctx context.Context, contextID, prompt string, onDelta func(string)) (string, error) {
	reply, err := (localRunnerTestEchoBackend{}).Prompt(ctx, contextID, prompt)
	if err == nil && onDelta != nil {
		onDelta(reply)
	}
	return reply, err
}

func (localRunnerTestEchoBackend) Close() error { return nil }

func startLocalRunnerTestEchoAgent() (*localRunnerBuiltInAgent, error) {
	return startLocalRunnerTestEchoAgentOn("127.0.0.1:0")
}

func startLocalRunnerTestEchoAgentAt(rawOrigin string) (*localRunnerBuiltInAgent, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawOrigin))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid_local_runner_test_echo_origin")
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("invalid_local_runner_test_echo_origin")
	}
	return startLocalRunnerTestEchoAgentOn(parsed.Host)
}

func startLocalRunnerTestEchoAgentOn(listenAddress string) (*localRunnerBuiltInAgent, error) {
	return startLocalRunnerLocalAgentWithBackend(localRunnerTestEchoBackend{}, listenAddress, localRunnerTestEchoRef)
}
