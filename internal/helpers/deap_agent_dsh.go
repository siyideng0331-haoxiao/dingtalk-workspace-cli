// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
)

func resolveExactOperatorOpenDingTalkID(ctx context.Context, userID string) (string, error) {
	response, err := callPrivateMCPJSON(ctx, "contact", "get_user_info_by_user_ids", map[string]any{"user_id_list": []string{userID}})
	if err != nil {
		return "", fmt.Errorf("resolve supervisor operator identity: %w", err)
	}
	candidates := map[string]struct{}{}
	collectExactOpenDingTalkIDs(response, userID, candidates)
	if len(candidates) != 1 {
		return "", apperrors.NewValidation("主管 userId 未能精确解析出唯一 operatorOpenDingTalkId；禁止按姓名猜测或手工覆盖")
	}
	for candidate := range candidates {
		return candidate, nil
	}
	return "", apperrors.NewValidation("主管 operatorOpenDingTalkId 缺失")
}

func collectExactOpenDingTalkIDs(value any, targetUserID string, candidates map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		matched := false
		for _, key := range []string{"userId", "user_id", "userid", "uid", "staffId", "staff_id"} {
			if jsonScalar(typed[key]) == targetUserID {
				matched = true
				break
			}
		}
		if nested, ok := typed[targetUserID]; ok {
			collectOpenDingTalkScalars(nested, candidates)
		}
		if matched {
			collectOpenDingTalkScalars(typed, candidates)
		}
		for _, nested := range typed {
			collectExactOpenDingTalkIDs(nested, targetUserID, candidates)
		}
	case []any:
		for _, nested := range typed {
			collectExactOpenDingTalkIDs(nested, targetUserID, candidates)
		}
	}
}

func collectOpenDingTalkScalars(value any, candidates map[string]struct{}) {
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"openDingTalkId", "openDingtalkId", "open_dingtalk_id", "operatorOpenDingTalkId"} {
			if candidate := jsonScalar(object[key]); candidate != "" {
				candidates[candidate] = struct{}{}
			}
		}
	}
}

func runDigitalEmployeeDSHRegister(ctx context.Context, registration map[string]any) (string, error) {
	input, err := json.Marshal(registration)
	if err != nil {
		return "", fmt.Errorf("encode DSH registration: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "dsh-dingtalk", "digital-employee", "register", "--stdin", "--json")
	command.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("dsh-dingtalk register exited unsuccessfully")
	}
	if stdout.Len() > 64*1024 {
		return "", fmt.Errorf("dsh-dingtalk register output too large")
	}
	var response struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&response); err != nil {
		return "", fmt.Errorf("dsh-dingtalk register returned invalid JSON")
	}
	switch response.Status {
	case "created", "updated", "unchanged":
		return response.Status, nil
	default:
		return "", fmt.Errorf("dsh-dingtalk register returned invalid status")
	}
}
