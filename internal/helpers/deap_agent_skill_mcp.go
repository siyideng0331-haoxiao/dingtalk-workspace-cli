// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package helpers

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/apiclient"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/cmdutil"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/edition"
	"github.com/spf13/cobra"
)

const (
	deapAgentSkillCreateFileTool = "create_skill_from_file"
	deapAgentSkillCreateURLTool  = "create_skill_by_url"
	deapAgentSkillListTool       = "list_skills"
	deapAgentSkillQueryTool      = "get_skill_detail"
	deapAgentMCPCreateTool       = "create_mcp"
	deapAgentMCPListTool         = "list_mcps"
	deapAgentMCPQueryTool        = "get_mcp_detail"
	deapAgentSkillUploadPath     = "/v1.0/assistant/skills/upload"

	deapAgentConfigFileMaxSize    = 1024 * 1024
	deapAgentSkillMaxPackageSize  = 50 * 1024 * 1024
	deapAgentSkillMaxExpandedSize = 200 * 1024 * 1024
	deapAgentSkillMaxEntries      = 10000
)

var deapAgentSnapshots = []string{"draft", "published"}

type deapAgentSkillPackage struct {
	path string
	size int64
}

type deapAgentSkillCreated struct {
	SkillID     string `json:"skillId"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Version     int64  `json:"version,omitempty"`
}

type deapAgentSkillPackageUploader interface {
	Upload(ctx context.Context, filePath string) (string, error)
}

type deapAgentOpenAPISkillUploader struct {
	baseURL      string
	httpClient   *http.Client
	resolveToken func(context.Context) (string, error)
}

type deapAgentOpenAPISkillDetail struct {
	SkillID     string `json:"skillId"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type deapAgentOpenAPISkillPayload struct {
	SkillID string                       `json:"skillId"`
	Skill   *deapAgentOpenAPISkillDetail `json:"skill"`
}

type deapAgentOpenAPISkillEnvelope struct {
	SkillID string                        `json:"skillId"`
	Skill   *deapAgentOpenAPISkillDetail  `json:"skill"`
	Success *bool                         `json:"success"`
	Data    *deapAgentOpenAPISkillPayload `json:"data"`
	Result  *deapAgentOpenAPISkillPayload `json:"result"`
	Content *deapAgentOpenAPISkillPayload `json:"content"`
}

type deapAgentOpenAPIUploadPayload struct {
	FileURL string `json:"fileUrl"`
}

type deapAgentOpenAPIUploadEnvelope struct {
	FileURL string                         `json:"fileUrl"`
	Success *bool                          `json:"success"`
	Data    *deapAgentOpenAPIUploadPayload `json:"data"`
	Result  *deapAgentOpenAPIUploadPayload `json:"result"`
	Content *deapAgentOpenAPIUploadPayload `json:"content"`
}

func (u deapAgentOpenAPISkillUploader) Upload(ctx context.Context, filePath string) (string, error) {
	token, err := u.accessToken(ctx)
	if err != nil {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("OpenAPI 认证失败")}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("Skill ZIP 文件不可读")}
	}
	defer file.Close()

	client := apiclient.NewClient(token, u.baseURL)
	if u.httpClient != nil {
		client.HTTPClient = u.httpClient
	}
	response, err := client.UploadMultipart(ctx, apiclient.MultipartUploadRequest{
		Path:      deapAgentSkillUploadPath,
		FieldName: "file",
		FileName:  filepath.Base(filePath),
		File:      file,
	})
	if err != nil {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("OpenAPI 上传请求失败")}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", &deapAgentSkillStageError{
			Stage: "upload",
			Err:   fmt.Errorf("OpenAPI 返回 HTTP %d", response.StatusCode),
		}
	}

	var envelope deapAgentOpenAPIUploadEnvelope
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("OpenAPI 上传响应格式非法")}
	}
	if envelope.Success != nil && !*envelope.Success {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("OpenAPI 上传失败")}
	}
	payload := deapAgentOpenAPIUploadPayload{FileURL: envelope.FileURL}
	for _, candidate := range []*deapAgentOpenAPIUploadPayload{envelope.Data, envelope.Result, envelope.Content} {
		if candidate != nil {
			payload = *candidate
			break
		}
	}
	if strings.TrimSpace(payload.FileURL) == "" {
		return "", &deapAgentSkillStageError{Stage: "upload", Err: fmt.Errorf("OpenAPI 上传响应缺少 fileUrl")}
	}
	return payload.FileURL, nil
}

func deapAgentParseSkillCreated(body []byte) (deapAgentSkillCreated, error) {
	var envelope deapAgentOpenAPISkillEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return deapAgentSkillCreated{}, fmt.Errorf("创建响应格式非法")
	}
	payload := deapAgentOpenAPISkillPayload{SkillID: envelope.SkillID, Skill: envelope.Skill}
	for _, candidate := range []*deapAgentOpenAPISkillPayload{envelope.Data, envelope.Result, envelope.Content} {
		if candidate != nil {
			payload = *candidate
			break
		}
	}
	if strings.TrimSpace(payload.SkillID) == "" {
		return deapAgentSkillCreated{}, fmt.Errorf("创建响应缺少 skillId")
	}
	created := deapAgentSkillCreated{SkillID: payload.SkillID}
	if payload.Skill != nil {
		created.Name = payload.Skill.Name
		created.DisplayName = payload.Skill.DisplayName
		created.Description = payload.Skill.Description
	}
	return created, nil
}

func (u deapAgentOpenAPISkillUploader) accessToken(ctx context.Context) (string, error) {
	if u.resolveToken != nil {
		return u.resolveToken(ctx)
	}
	if deps == nil || deps.Caller == nil {
		return "", fmt.Errorf("OpenAPI token resolver is not configured")
	}
	provider, ok := deps.Caller.(edition.AccessTokenCaller)
	if !ok {
		return "", fmt.Errorf("OpenAPI token resolver is not supported")
	}
	token, err := provider.AccessToken(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("OpenAPI access token is unavailable")
	}
	return strings.TrimSpace(token), nil
}

func deapAgentSkillStageFromResponse(body []byte, fallback string) string {
	lower := strings.ToLower(string(body))
	for _, stage := range []string{"upload", "create", "query"} {
		if strings.Contains(lower, stage+" stage") {
			return stage
		}
	}
	return fallback
}

type deapAgentSkillStageError struct {
	Stage string
	Err   error
}

func (e *deapAgentSkillStageError) Error() string {
	if e == nil {
		return "skill create 阶段失败"
	}
	detail := ""
	if e.Err != nil {
		detail = e.Err.Error()
	}
	lower := strings.ToLower(detail)
	for _, marker := range []string{"http://", "https://", "token", "secret", "credential", "password", "fileurl", "uploadurl"} {
		if strings.Contains(lower, marker) {
			detail = "下游错误详情已脱敏"
			break
		}
	}
	if detail == "" {
		return fmt.Sprintf("skill create %s 阶段失败", e.Stage)
	}
	return fmt.Sprintf("skill create %s 阶段失败: %s", e.Stage, detail)
}

func (e *deapAgentSkillStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

var deapAgentSkillUploader deapAgentSkillPackageUploader = deapAgentOpenAPISkillUploader{}

func deapAgentValidateSkillPackage(rawPath string) (deapAgentSkillPackage, error) {
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(rawPath)), ".zip") {
		return deapAgentSkillPackage{}, apperrors.NewValidation("参数 --file 必须使用 .zip 扩展名")
	}
	resolved, err := apperrors.SafeInputPath(strings.TrimSpace(rawPath))
	if err != nil {
		return deapAgentSkillPackage{}, apperrors.NewValidation(fmt.Sprintf("参数 --file 路径不安全: %v", err))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return deapAgentSkillPackage{}, apperrors.NewValidation("参数 --file 文件不可读")
	}
	if !info.Mode().IsRegular() {
		return deapAgentSkillPackage{}, apperrors.NewValidation("参数 --file 必须是普通文件")
	}
	if info.Size() > deapAgentSkillMaxPackageSize {
		return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 不能超过 50 MiB")
	}
	reader, err := zip.OpenReader(resolved)
	if err != nil {
		return deapAgentSkillPackage{}, apperrors.NewValidation("参数 --file 不是有效 ZIP 文件")
	}
	defer reader.Close()
	if len(reader.File) > deapAgentSkillMaxEntries {
		return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 文件条目过多")
	}
	foundSkill := false
	var declaredSize uint64
	var expandedSize int64
	for _, entry := range reader.File {
		name := entry.Name
		cleanName := pathpkg.Clean(strings.ReplaceAll(name, "\\", "/"))
		if strings.Contains(name, "\\") || pathpkg.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, "../") || (len(cleanName) >= 2 && cleanName[1] == ':') {
			return deapAgentSkillPackage{}, apperrors.NewValidation(fmt.Sprintf("Skill ZIP 包含不安全路径 %q", name))
		}
		mode := entry.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !mode.IsRegular()) {
			return deapAgentSkillPackage{}, apperrors.NewValidation(fmt.Sprintf("Skill ZIP 包含不安全文件类型 %q", name))
		}
		if entry.UncompressedSize64 > uint64(deapAgentSkillMaxExpandedSize) || declaredSize > uint64(deapAgentSkillMaxExpandedSize)-entry.UncompressedSize64 {
			return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 解压后内容过大")
		}
		declaredSize += entry.UncompressedSize64
		if !entry.FileInfo().IsDir() && pathpkg.Base(cleanName) == "SKILL.md" {
			foundSkill = true
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		body, openErr := entry.Open()
		if openErr != nil {
			return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 文件内容损坏")
		}
		remaining := deapAgentSkillMaxExpandedSize - expandedSize
		readSize, readErr := io.CopyN(io.Discard, body, remaining+1)
		closeErr := body.Close()
		expandedSize += readSize
		if expandedSize > deapAgentSkillMaxExpandedSize {
			return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 解压后内容过大")
		}
		if readErr != nil && readErr != io.EOF {
			return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 文件内容损坏")
		}
		if closeErr != nil {
			return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 文件内容损坏")
		}
	}
	if !foundSkill {
		return deapAgentSkillPackage{}, apperrors.NewValidation("Skill ZIP 中缺少 SKILL.md")
	}
	return deapAgentSkillPackage{path: resolved, size: info.Size()}, nil
}

func newDeapAgentSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "skill",
		Short:             "管理数字员工 Skill",
		Long:              "创建和查询独立 Skill 资源。Skill 与数字员工的关联由 save-draft skills 配置负责。",
		Args:              cobra.NoArgs,
		TraverseChildren:  true,
		DisableAutoGenTag: true,
		RunE:              groupRunE,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(newDeapAgentSkillCreateCommand(), newDeapAgentSkillListCommand(), newDeapAgentSkillQueryCommand())
	return cmd
}

func newDeapAgentMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "mcp",
		Short:             "管理数字员工 MCP",
		Long:              "创建和查询独立 MCP 资源。敏感配置通过本地 JSON 文件传入；与数字员工的关联由 save-draft mcps 配置负责。",
		Args:              cobra.NoArgs,
		TraverseChildren:  true,
		DisableAutoGenTag: true,
		RunE:              groupRunE,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(newDeapAgentMCPCreateCommand(), newDeapAgentMCPListCommand(), newDeapAgentMCPQueryCommand())
	return cmd
}

func newDeapAgentSkillCreateCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "create", Short: "从本地 ZIP 创建 Skill 资源",
		Long: "校验本地 Skill ZIP 后，先通过 OpenAPI multipart 接口上传，再调用 create_skill_by_url 完成 Skill Center create 与 query。ZIP 不进入 MCP JSON，临时签名 URL 不落盘、不输出。",
		Tool: deapAgentSkillCreateFileTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "目标数字员工 UUID（Skill Center V2 tenant）", Bind: "agentUuid", Required: true, Trim: true},
			{Name: "file", Usage: "本地 Skill ZIP（相对当前目录、最大 50 MiB、必须包含 SKILL.md）", Bind: "file", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{Effect: "write", Risk: "high", Confirmation: "user_required", Idempotency: "non_idempotent"},
		Validate: func(cmd *cobra.Command, _ []string) error {
			rawPath, _ := cmd.Flags().GetString("file")
			if _, err := deapAgentValidateSkillPackage(rawPath); err != nil {
				return &deapAgentSkillStageError{Stage: "validate", Err: err}
			}
			return nil
		},
		Call: deapAgentCallSkillCreate,
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentSkillCreateFileTool, CanonicalPath: "deap.create_skill_from_file", CLIPath: "deap skill create", PrimaryCLIPath: "deap skill create", Group: "skill"},
			Description: "校验本地 ZIP，依次调用 OpenAPI upload 与 create_skill_by_url，并只输出安全创建结果。",
			DryRun:      deapAgentDryRun,
			Interface:   &contract.InterfaceSpec{Mode: contract.InterfaceModeComposite, Availability: contract.InterfaceAvailable, Reason: "本地 ZIP 校验后串联 OpenAPI multipart upload 与 create_skill_by_url"},
			Selection:   contract.SelectionSpec{AgentSummary: "从本地 ZIP 创建 Skill 资源", UseWhen: []string{"已有合法 Skill ZIP，需要为目标数字员工创建并取得 skillId 时"}, AvoidWhen: []string{"只有远程 URL 的纯 MCP 场景使用 create_skill_by_url"}, Examples: []string{"dws deap skill create --agent-uuid <agentUuid> --file ./my-skill.zip --dry-run --format json"}},
			Parameters: []contract.ParamDecl{
				{Name: "agent-uuid", Property: "agentUuid", InterfaceType: "string"},
				{Name: "file", Property: "file", InterfaceType: "binary"},
			},
		},
	})
}

func deapAgentCallSkillCreate(cmd *cobra.Command, _ string, args map[string]any) error {
	agentUUID, _ := args["agentUuid"].(string)
	rawPath, _ := args["file"].(string)
	pkg, err := deapAgentValidateSkillPackage(rawPath)
	if err != nil {
		return &deapAgentSkillStageError{Stage: "validate", Err: err}
	}
	if deps.Caller.DryRun() {
		return deps.Out.PrintJSON(map[string]any{
			"dryRun":    true,
			"action":    "upload_then_create_skill",
			"agentUuid": agentUUID,
			"fileName":  filepath.Base(pkg.path),
			"fileSize":  pkg.size,
		})
	}
	fileURL, err := deapAgentSkillUploader.Upload(cmd.Context(), pkg.path)
	if err != nil {
		if staged, ok := err.(*deapAgentSkillStageError); ok {
			return staged
		}
		return &deapAgentSkillStageError{Stage: "upload", Err: err}
	}
	responseText, err := callMCPToolReturnTextOnServer(cmd.Context(), deapAgentServerID, deapAgentSkillCreateURLTool, map[string]any{
		"agentUuid": agentUUID,
		"fileUrl":   fileURL,
	})
	if err != nil {
		stage := deapAgentSkillStageFromResponse([]byte(err.Error()), "create")
		return &deapAgentSkillStageError{Stage: stage, Err: err}
	}
	result, err := deapAgentParseSkillCreated([]byte(responseText))
	if err != nil {
		return &deapAgentSkillStageError{Stage: "query", Err: err}
	}
	if strings.TrimSpace(result.SkillID) == "" {
		return &deapAgentSkillStageError{Stage: "query", Err: fmt.Errorf("创建响应缺少 skillId")}
	}
	return deps.Out.PrintJSON(result)
}

func newDeapAgentSkillListCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "list", Short: "查询 Skill 资源列表",
		Long: "查询目标数字员工 tenant 下的 Skill 资源列表及非敏感配置。查询无数据会返回健康空列表。",
		Tool: deapAgentSkillListTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "目标数字员工 UUID（Skill Center V2 tenant）", Bind: "agentUuid", Required: true, Trim: true},
			{Name: "snapshot", Usage: "配置快照：draft 或 published", Bind: "snapshot", Default: "draft", ArgDefault: "draft", Enum: deapAgentSnapshots},
		},
		Safety: contract.SafetySpec{Effect: "read", Risk: "low", Confirmation: "not_required", Idempotency: "idempotent"},
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentSkillListTool, CanonicalPath: "deap.list_skills", CLIPath: "deap skill list", PrimaryCLIPath: "deap skill list", Group: "skill"},
			Description: "查询独立 Skill 资源列表和非敏感配置。",
			DryRun:      deapAgentDryRun, Interface: deapAgentMCPInterface(deapAgentSkillListTool),
			Selection: contract.SelectionSpec{AgentSummary: "查询目标数字员工 tenant 下的 Skill 列表", UseWhen: []string{"需要选择或核对目标数字员工的 Skill 时"}, AvoidWhen: []string{"已知 skillId 需要完整详情时使用 skill query"}, Examples: []string{"dws deap skill list --agent-uuid <agentUuid> --snapshot draft --format json"}},
		},
	})
}

func newDeapAgentSkillQueryCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "query", Short: "查询 Skill 资源详情",
		Long: "按 skillId 查询独立 Skill 资源详情和非敏感配置。查询失败与查询无数据由服务端分别返回。",
		Tool: deapAgentSkillQueryTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "目标数字员工 UUID（Skill Center V2 tenant）", Bind: "agentUuid", Required: true, Trim: true},
			{Name: "skill-id", Usage: "Skill ID", Bind: "skillId", Required: true, Trim: true},
			{Name: "snapshot", Usage: "配置快照：draft 或 published", Bind: "snapshot", Default: "draft", ArgDefault: "draft", Enum: deapAgentSnapshots},
		},
		Safety: contract.SafetySpec{Effect: "read", Risk: "low", Confirmation: "not_required", Idempotency: "idempotent"},
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentSkillQueryTool, CanonicalPath: "deap.get_skill_detail", CLIPath: "deap skill query", PrimaryCLIPath: "deap skill query", Group: "skill"},
			Description: "按 skillId 查询独立 Skill 资源详情和非敏感配置。",
			DryRun:      deapAgentDryRun, Interface: deapAgentMCPInterface(deapAgentSkillQueryTool),
			Selection: contract.SelectionSpec{AgentSummary: "查询目标数字员工 tenant 下的一个 Skill", UseWhen: []string{"已知 agentUuid 和 skillId，需要核对解析信息或配置时"}, AvoidWhen: []string{"需要浏览全部 Skill 时使用 skill list"}, Examples: []string{"dws deap skill query --agent-uuid <agentUuid> --skill-id <skillId> --format json"}},
		},
	})
}

func newDeapAgentMCPCreateCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "create", Short: "创建 MCP 资源",
		Long: "从本地 JSON 对象文件创建独立 MCP 资源。配置字段遵循 McpConfigParam：name、description、detailIntro、userQuestionTips、configType、configString、envs、toolsDisabled。凭据不会进入 argv。",
		Tool: deapAgentMCPCreateTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags:  []LeafFlag{{Name: "config-file", Usage: "McpConfigParam JSON 对象文件（最大 1 MiB；敏感值放 configString/envs）", Bind: "configFile", Required: true, Trim: true}},
		Safety: contract.SafetySpec{Effect: "write", Risk: "high", Confirmation: "user_required", Idempotency: "unknown"},
		Call:   deapAgentCallMCPCreateFromFile,
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentMCPCreateTool, CanonicalPath: "deap.create_mcp", CLIPath: "deap mcp create", PrimaryCLIPath: "deap mcp create", Group: "mcp"},
			Description: "通过本地 JSON 文件安全传入定义和凭据，创建独立 MCP 资源。",
			DryRun:      deapAgentDryRun, Interface: deapAgentMCPInterface(deapAgentMCPCreateTool),
			Selection:  contract.SelectionSpec{AgentSummary: "从本地配置文件创建独立 MCP 资源", UseWhen: []string{"需要注册新的 MCP 定义和鉴权配置并取得 mcpId 时"}, AvoidWhen: []string{"只需查询现有 MCP 时使用 mcp list 或 mcp query", "不要把凭据直接拼进命令行"}, Examples: []string{"dws deap mcp create --config-file ./mcp.json --dry-run --format json"}},
			Parameters: []contract.ParamDecl{{Name: "config-file", Property: "config", InterfaceType: "object"}},
		},
	})
}

func newDeapAgentMCPListCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "list", Short: "查询 MCP 资源列表",
		Long: "查询当前企业资源域的 MCP 资源列表和服务端脱敏配置。任何凭据都不得出现在响应中。",
		Tool: deapAgentMCPListTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "keywords", Usage: "名称或描述关键词", Bind: "keywords", Trim: true},
			{Name: "page", Usage: "页码", Bind: "page", Kind: LeafInt, Default: "1", ArgDefault: "1"},
			{Name: "page-size", Usage: "每页数量", Bind: "pageSize", Kind: LeafInt, Default: "20", ArgDefault: "20"},
		},
		Safety: contract.SafetySpec{Effect: "read", Risk: "low", Confirmation: "not_required", Idempotency: "idempotent"},
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentMCPListTool, CanonicalPath: "deap.list_mcps", CLIPath: "deap mcp list", PrimaryCLIPath: "deap mcp list", Group: "mcp"},
			Description: "查询独立 MCP 资源列表和服务端脱敏配置。",
			DryRun:      deapAgentDryRun, Interface: deapAgentMCPInterface(deapAgentMCPListTool),
			Selection: contract.SelectionSpec{AgentSummary: "查询当前企业的 MCP 资源列表", UseWhen: []string{"需要选择可关联到数字员工草稿的 MCP 时"}, AvoidWhen: []string{"已知 mcpId 需要单项详情时使用 mcp query"}, Examples: []string{"dws deap mcp list --keywords 文档 --page 1 --page-size 20 --format json"}},
		},
	})
}

func newDeapAgentMCPQueryCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use: "query", Short: "查询 MCP 资源详情",
		Long: "按 mcpId 查询独立 MCP 资源定义、工具解析结果和服务端脱敏配置。响应不得包含密钥、Token 或临时签名地址。",
		Tool: deapAgentMCPQueryTool, Server: deapAgentServerID, PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "mcp-id", Usage: "MCP ID", Bind: "mcpId", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{Effect: "read", Risk: "low", Confirmation: "not_required", Idempotency: "idempotent"},
		Contract: LeafContract{
			Identity:    contract.ToolIdentitySpec{ProductID: deapProductID, Name: deapAgentMCPQueryTool, CanonicalPath: "deap.get_mcp_detail", CLIPath: "deap mcp query", PrimaryCLIPath: "deap mcp query", Group: "mcp"},
			Description: "按 mcpId 查询独立 MCP 资源定义、工具列表和脱敏配置。",
			DryRun:      deapAgentDryRun, Interface: deapAgentMCPInterface(deapAgentMCPQueryTool),
			Selection: contract.SelectionSpec{AgentSummary: "查询一个独立 MCP 资源的脱敏详情", UseWhen: []string{"已知 mcpId，需要核对定义或工具解析结果时"}, AvoidWhen: []string{"需要取得明文凭据时不要使用，系统不提供明文回显"}, Examples: []string{"dws deap mcp query --mcp-id <mcpId> --format json"}},
		},
	})
}

func deapAgentCallMCPCreateFromFile(_ *cobra.Command, tool string, args map[string]any) error {
	rawPath, _ := args["configFile"].(string)
	config, err := deapAgentReadJSONObjectFile(rawPath, "config-file")
	if err != nil {
		return err
	}
	delete(args, "configFile")
	if deps.Caller.DryRun() {
		args["config"] = map[string]any{"provided": true, "redacted": true}
		return callMCPToolOnServer(deapAgentServerID, tool, args)
	}
	args["config"] = config
	return callMCPToolOnServer(deapAgentServerID, tool, args)
}

func deapAgentCallWithProfileAndDraftFiles(cmd *cobra.Command, tool string, args map[string]any) error {
	summaries := map[string]any{}
	for _, item := range []struct {
		argument string
		flagName string
		property string
	}{
		{"skillsFile", "skills-file", "skills"},
		{"mcpsFile", "mcps-file", "mcps"},
	} {
		rawPath, ok := args[item.argument].(string)
		if !ok || strings.TrimSpace(rawPath) == "" {
			continue
		}
		value, err := deapAgentReadJSONArrayFile(rawPath, item.flagName)
		if err != nil {
			return err
		}
		delete(args, item.argument)
		args[item.property] = value
		summaries[item.property] = map[string]any{"provided": true, "count": len(value), "redacted": true}
	}
	if deps.Caller.DryRun() && len(summaries) > 0 {
		for property, summary := range summaries {
			args[property] = summary
		}
	}
	return deapAgentCallWithProfile(cmd, tool, args)
}

func deapAgentReadJSONObjectFile(rawPath, flagName string) (map[string]any, error) {
	value, err := deapAgentReadJSONFile(rawPath, flagName)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 必须指向 JSON 对象文件", flagName))
	}
	return object, nil
}

func deapAgentReadJSONArrayFile(rawPath, flagName string) ([]any, error) {
	value, err := deapAgentReadJSONFile(rawPath, flagName)
	if err != nil {
		return nil, err
	}
	array, ok := value.([]any)
	if !ok {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 必须指向 JSON 数组文件", flagName))
	}
	return array, nil
}

func deapAgentReadJSONFile(rawPath, flagName string) (any, error) {
	path, err := apperrors.SafeInputPath(strings.TrimSpace(rawPath))
	if err != nil {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 路径不安全: %v", flagName, err))
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 文件不可读", flagName))
	}
	if !info.Mode().IsRegular() {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 必须是普通文件", flagName))
	}
	if info.Size() > deapAgentConfigFileMaxSize {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 文件不能超过 1 MiB", flagName))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 文件不可读", flagName))
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, apperrors.NewValidation(fmt.Sprintf("参数 --%s 必须是合法 JSON", flagName))
	}
	return value, nil
}
