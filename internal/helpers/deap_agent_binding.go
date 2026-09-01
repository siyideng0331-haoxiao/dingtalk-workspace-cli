// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package helpers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// digitalEmployeeBindingPath 使用 Profile 摘要隔离多员工配置；文件内容仍保留
// 精确 Profile 用于读回校验，但不保存任何 Token 或授权码。
func digitalEmployeeBindingPath(configDir, profile string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(profile)))
	return filepath.Join(configDir, "digital-employees", fmt.Sprintf("%x.json", digest[:]))
}

func saveDigitalEmployeeBinding(configDir string, binding digitalEmployeeBinding) error {
	if binding.SchemaVersion != 1 || !validMachineString(binding.AgentUUID) || !validMachineString(binding.DWSProfile) ||
		!validMachineString(binding.OperatorOpenDingTalkID) {
		return fmt.Errorf("invalid digital employee binding")
	}
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("encode digital employee binding: %w", err)
	}
	return AtomicWriteJSON(digitalEmployeeBindingPath(configDir, binding.DWSProfile), append(data, '\n'))
}

func loadDigitalEmployeeBinding(configDir, profile string) (digitalEmployeeBinding, error) {
	var binding digitalEmployeeBinding
	data, err := os.ReadFile(digitalEmployeeBindingPath(configDir, profile))
	if err != nil {
		return binding, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return digitalEmployeeBinding{}, fmt.Errorf("invalid digital employee binding")
	}
	if binding.SchemaVersion != 1 || binding.DWSProfile != strings.TrimSpace(profile) || !validMachineString(binding.AgentUUID) ||
		!validMachineString(binding.OperatorOpenDingTalkID) {
		return digitalEmployeeBinding{}, fmt.Errorf("invalid digital employee binding")
	}
	return binding, nil
}
