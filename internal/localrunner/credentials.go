package localrunner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/keychain"
)

const endpointBearerAccountPrefix = "local-runner-endpoint:"

var (
	ErrEndpointBearerNotFound          = errors.New("endpoint_bearer_not_found")
	ErrEndpointBearerKeyringUnavailable = errors.New("endpoint_bearer_keyring_unavailable")
)

type EndpointBearerSecretBackend interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Remove(service, account string) error
}

type EndpointBearerKeyring struct {
	backend EndpointBearerSecretBackend
}

func NewEndpointBearerKeyring(backend EndpointBearerSecretBackend) *EndpointBearerKeyring {
	return &EndpointBearerKeyring{backend: backend}
}

func NewSystemEndpointBearerKeyring() *EndpointBearerKeyring {
	return NewEndpointBearerKeyring(systemEndpointBearerKeyring{})
}

func (s *EndpointBearerKeyring) StoreEndpointBearer(_ context.Context, runnerID, endpointID string, secret []byte) error {
	account, err := endpointBearerAccount(runnerID, endpointID)
	if err != nil || s == nil || s.backend == nil || len(secret) == 0 {
		return ErrLifecycleResponseMalformed
	}
	if err := s.backend.Set(keychain.Service, account, string(secret)); err != nil {
		return fmt.Errorf("%w: store", ErrEndpointBearerKeyringUnavailable)
	}
	return nil
}

func (s *EndpointBearerKeyring) LoadEndpointBearer(_ context.Context, runnerID, endpointID string) ([]byte, error) {
	account, err := endpointBearerAccount(runnerID, endpointID)
	if err != nil || s == nil || s.backend == nil {
		return nil, ErrLifecycleResponseMalformed
	}
	value, err := s.backend.Get(keychain.Service, account)
	if err != nil {
		return nil, fmt.Errorf("%w: load", ErrEndpointBearerKeyringUnavailable)
	}
	if value == "" {
		return nil, ErrEndpointBearerNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *EndpointBearerKeyring) RemoveEndpointBearer(_ context.Context, runnerID, endpointID string) error {
	account, err := endpointBearerAccount(runnerID, endpointID)
	if err != nil || s == nil || s.backend == nil {
		return ErrLifecycleResponseMalformed
	}
	if err := s.backend.Remove(keychain.Service, account); err != nil {
		return fmt.Errorf("%w: remove", ErrEndpointBearerKeyringUnavailable)
	}
	return nil
}

func endpointBearerAccount(runnerID, endpointID string) (string, error) {
	runnerID = strings.TrimSpace(runnerID)
	endpointID = strings.TrimSpace(endpointID)
	if runnerID == "" || endpointID == "" {
		return "", ErrInvalidIdentity
	}
	digest := sha256.Sum256([]byte(runnerID + "\x00" + endpointID))
	return fmt.Sprintf("%s%x", endpointBearerAccountPrefix, digest), nil
}

type systemEndpointBearerKeyring struct{}

func (systemEndpointBearerKeyring) Get(service, account string) (string, error) {
	return keychain.Get(service, account)
}

func (systemEndpointBearerKeyring) Set(service, account, value string) error {
	return keychain.Set(service, account, value)
}

func (systemEndpointBearerKeyring) Remove(service, account string) error {
	return keychain.Remove(service, account)
}
