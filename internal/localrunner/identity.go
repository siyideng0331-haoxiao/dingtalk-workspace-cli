package localrunner

import (
	"errors"
	"strings"
)

var ErrInvalidIdentity = errors.New("invalid_identity")

type RunnerEndpointIdentity struct {
	TenantID       string `json:"tenantId"`
	OperatorUserID string `json:"operatorUserId"`
	RunnerID       string `json:"runnerId"`
	EndpointID     string `json:"endpointId"`
}

func (i RunnerEndpointIdentity) normalized() (RunnerEndpointIdentity, error) {
	i.TenantID = strings.TrimSpace(i.TenantID)
	i.OperatorUserID = strings.TrimSpace(i.OperatorUserID)
	i.RunnerID = strings.TrimSpace(i.RunnerID)
	i.EndpointID = strings.TrimSpace(i.EndpointID)
	if i.TenantID == "" || i.OperatorUserID == "" || i.RunnerID == "" || i.EndpointID == "" {
		return RunnerEndpointIdentity{}, ErrInvalidIdentity
	}
	return i, nil
}

type RunnerEndpointConfig struct {
	identity RunnerEndpointIdentity
}

func NewRunnerEndpointConfig(identity RunnerEndpointIdentity) (RunnerEndpointConfig, error) {
	normalized, err := identity.normalized()
	if err != nil {
		return RunnerEndpointConfig{}, err
	}
	return RunnerEndpointConfig{identity: normalized}, nil
}

func (c RunnerEndpointConfig) Identity() RunnerEndpointIdentity {
	return c.identity
}
