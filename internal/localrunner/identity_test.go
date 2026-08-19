package localrunner

import (
	"errors"
	"testing"
)

func TestRunnerEndpointConfigRejectsBlankIdentityFields(t *testing.T) {
	tests := []struct {
		name     string
		identity RunnerEndpointIdentity
	}{
		{name: "tenant", identity: RunnerEndpointIdentity{OperatorUserID: "operator", RunnerID: "runner", EndpointID: "endpoint"}},
		{name: "operator", identity: RunnerEndpointIdentity{TenantID: "tenant", RunnerID: "runner", EndpointID: "endpoint"}},
		{name: "runner", identity: RunnerEndpointIdentity{TenantID: "tenant", OperatorUserID: "operator", EndpointID: "endpoint"}},
		{name: "endpoint", identity: RunnerEndpointIdentity{TenantID: "tenant", OperatorUserID: "operator", RunnerID: "runner"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRunnerEndpointConfig(tt.identity); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func TestRunnerEndpointConfigContainsOneTrimmedIdentity(t *testing.T) {
	config, err := NewRunnerEndpointConfig(RunnerEndpointIdentity{
		TenantID:       " tenant ",
		OperatorUserID: " operator ",
		RunnerID:       " runner-1 ",
		EndpointID:     " endpoint-1 ",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := RunnerEndpointIdentity{
		TenantID:       "tenant",
		OperatorUserID: "operator",
		RunnerID:       "runner-1",
		EndpointID:     "endpoint-1",
	}
	if got := config.Identity(); got != want {
		t.Fatalf("identity = %#v, want %#v", got, want)
	}
}
