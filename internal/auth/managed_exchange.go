// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package auth

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// ManagedExchangeRequest 描述一次受管身份的短期授权码换票。ClientID 必须来自
// 同一份授权响应，PreserveProfile 是发起操作的主管精确 Profile。
type ManagedExchangeRequest struct {
	ClientID        string
	AuthCode        string
	UID             string
	ExpectedOrgID   string
	PreserveProfile string
}

var (
	managedExchangePreparePersistence = prepareLoginPersistence
	managedExchangePersistToken       = persistManagedExchangeToken
)

// ExchangeManagedAuthCode 使用显式 ClientID 直接走 MCP Exchange，并把结果写入
// 数字员工的精确身份槽。它不读取或修改全局 ClientID/ClientSecret、环境变量、
// app.json 或 RuntimeProfile；PreserveProfile 只参与本次持久化写计划。
func ExchangeManagedAuthCode(ctx context.Context, configDir string, request ManagedExchangeRequest) (*TokenData, error) {
	clientID := strings.TrimSpace(request.ClientID)
	authCode := strings.TrimSpace(request.AuthCode)
	uid := strings.TrimSpace(request.UID)
	expectedOrgID := strings.TrimSpace(request.ExpectedOrgID)
	preserveProfile := strings.TrimSpace(request.PreserveProfile)
	if clientID == "" || authCode == "" || uid == "" || expectedOrgID == "" || preserveProfile == "" {
		return nil, fmt.Errorf("managed exchange requires clientId, authCode, uid, expected orgId and supervisor profile")
	}
	if err := managedExchangePreparePersistence(configDir); err != nil {
		return nil, fmt.Errorf("local login state cannot be safely updated: %w", err)
	}

	provider := &OAuthProvider{
		configDir:  configDir,
		clientID:   clientID,
		Output:     io.Discard,
		httpClient: oauthHTTPClient,
	}
	data, err := provider.exchangeCodeViaMCPClientID(ctx, authCode, clientID)
	if err != nil {
		// 授权码、Token 和服务端原始正文都不得进入错误链或调试输出。
		return nil, fmt.Errorf("managed token exchange failed")
	}
	if data == nil {
		return nil, fmt.Errorf("managed token exchange returned no token data")
	}
	returnedUID := strings.TrimSpace(data.UserID)
	if returnedUID == "" || returnedUID != uid {
		return nil, fmt.Errorf("managed token identity does not match the authorized digital employee")
	}
	returnedOrgID := strings.TrimSpace(data.CorpID)
	if returnedOrgID == "" || returnedOrgID != expectedOrgID {
		return nil, fmt.Errorf("managed token organization does not match the authorization response")
	}
	data.ClientID = clientID
	data.Source = "mcp"
	data.FreshAuthorization = true
	if err := managedExchangePersistToken(configDir, preserveProfile, data); err != nil {
		return nil, fmt.Errorf("save managed digital employee profile: %w", err)
	}
	return data, nil
}

func persistManagedExchangeToken(configDir, preserveProfile string, data *TokenData) error {
	if data == nil {
		return fmt.Errorf("token data is empty")
	}
	if err := preflightTokenWritePersistenceForSelector(configDir, data, preserveProfile); err != nil {
		return fmt.Errorf("local login state cannot be safely updated: %w", err)
	}
	return withProfilesLock(configDir, func() error {
		return saveTokenDataLockedForSelector(configDir, data, preserveProfile)
	})
}
