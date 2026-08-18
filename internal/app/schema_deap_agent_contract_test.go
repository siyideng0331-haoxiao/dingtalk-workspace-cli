// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import "testing"

func TestDeapAgentLeavesReachFinalSchema(t *testing.T) {
	wants := map[string]struct {
		cliPath      string
		tool         string
		effect       string
		risk         string
		confirmation string
		parameters   map[string]string
	}{
		"dev.create_digital_employee": {
			"dev deap-agent create", "create_digital_employee", "write", "medium", "not_required",
			map[string]string{
				"name": "name", "description": "description", "dept-id": "deptId", "dept-name": "deptName",
				"icon": "icon", "profile-json": "digitalTagEmployeeProfile",
				"employee-no":    "digitalTagEmployeeProfile.employeeNo",
				"position-name":  "digitalTagEmployeeProfile.positionName",
				"supervisor-uid": "digitalTagEmployeeProfile.directSupervisorUid",
				"response-mode":  "digitalTagEmployeeProfile.responseMode",
			},
		},
		"dev.get_digital_employee_detail": {
			"dev deap-agent detail", "get_digital_employee_detail", "read", "low", "not_required",
			map[string]string{"agent-uuid": "agentUuid", "type": "type"},
		},
		"dev.list_digital_employees": {
			"dev deap-agent list", "list_digital_employees", "read", "low", "not_required",
			map[string]string{"keyword": "keyword", "page": "page", "page-size": "pageSize"},
		},
		"dev.update_digital_employee_draft": {
			"dev deap-agent save-draft", "update_digital_employee_draft", "write", "high", "user_required",
			map[string]string{
				"agent-uuid": "agentUuid", "name": "name", "description": "description", "dept-id": "deptId",
				"dept-name": "deptName", "icon": "icon", "prompt": "prompt", "profile-json": "digitalTagEmployeeProfile",
				"employee-no":    "digitalTagEmployeeProfile.employeeNo",
				"position-name":  "digitalTagEmployeeProfile.positionName",
				"supervisor-uid": "digitalTagEmployeeProfile.directSupervisorUid",
				"response-mode":  "digitalTagEmployeeProfile.responseMode",
				"skills-file":    "skills", "mcps-file": "mcps",
			},
		},
		"dev.publish_digital_employee": {
			"dev deap-agent publish", "publish_digital_employee", "write", "high", "user_required",
			map[string]string{"agent-uuid": "agentUuid", "allow-join-group": "allowJoinGroup"},
		},
		"dev.delete_digital_employee": {
			"dev deap-agent delete", "delete_digital_employee", "destructive", "high", "user_required",
			map[string]string{"agent-uuid": "agentUuid"},
		},
		"dev.query_de_run_status": {
			"dev deap-agent run-status", "query_de_run_status", "read", "low", "not_required",
			map[string]string{"source-id": "sourceId", "source-type": "sourceType", "assistant-id": "assistantId"},
		},
		"dev.query_de_trace": {
			"dev deap-agent trace", "query_de_trace", "read", "high", "not_required",
			map[string]string{"source-id": "sourceId", "source-type": "sourceType", "assistant-id": "assistantId"},
		},
	}
	canonicals := make([]string, 0, len(wants))
	for canonical := range wants {
		canonicals = append(canonicals, canonical)
	}
	payload := schemaContractPayloadForBoundCanonicals(t, NewRootCommand(), canonicals...)
	for canonical, want := range wants {
		tool := payload.Tools[canonical]
		if got := schemaContractString(tool["primary_cli_path"]); got != want.cliPath {
			t.Errorf("%s primary_cli_path = %q, want %q", canonical, got, want.cliPath)
		}
		for field, expected := range map[string]string{
			"effect": want.effect, "risk": want.risk,
			"confirmation": want.confirmation, "availability": "available",
		} {
			if got := schemaContractString(tool[field]); got != expected {
				t.Errorf("%s %s = %q, want %q", canonical, field, got, expected)
			}
		}
		ref := schemaInterfaceObject(tool["interface_ref"])
		if got := schemaContractString(ref["product_id"]); got != "deap-dev" {
			t.Errorf("%s interface product = %q, want deap-dev", canonical, got)
		}
		if got := schemaContractString(ref["rpc_name"]); got != want.tool {
			t.Errorf("%s interface rpc = %q, want %q", canonical, got, want.tool)
		}
		parameters := schemaContractMap(tool["parameters"])
		if len(parameters) != len(want.parameters) {
			t.Errorf("%s parameter count = %d, want %d: %#v", canonical, len(parameters), len(want.parameters), parameters)
		}
		for flagName, property := range want.parameters {
			parameter := parameters[flagName]
			if parameter == nil {
				t.Errorf("%s missing parameter %s", canonical, flagName)
				continue
			}
			if got := schemaContractString(parameter["property"]); got != property {
				t.Errorf("%s parameter %s property = %q, want %q", canonical, flagName, got, property)
			}
		}
		for _, forbidden := range []string{"org-id", "user-id", "agent-type"} {
			if _, ok := parameters[forbidden]; ok {
				t.Errorf("%s exposes forbidden parameter %s", canonical, forbidden)
			}
		}
	}
}

func TestDeapAgentSkillMCPLeavesReachFinalSchema(t *testing.T) {
	wants := map[string]struct {
		cliPath      string
		tool         string
		availability string
		parameters   map[string]string
	}{
		"dev.create_skill_from_file": {
			"dev deap-agent skill create", "", "available",
			map[string]string{"agent-uuid": "agentUuid", "file": "file"},
		},
		"dev.list_skills": {
			"dev deap-agent skill list", "list_skills", "available",
			map[string]string{"agent-uuid": "agentUuid", "snapshot": "snapshot"},
		},
		"dev.get_skill_detail": {
			"dev deap-agent skill query", "get_skill_detail", "available",
			map[string]string{"agent-uuid": "agentUuid", "skill-id": "skillId", "snapshot": "snapshot"},
		},
		"dev.create_mcp": {
			"dev deap-agent mcp create", "create_mcp", "available",
			map[string]string{"config-file": "config"},
		},
		"dev.list_mcps": {
			"dev deap-agent mcp list", "list_mcps", "available",
			map[string]string{"keywords": "keywords", "page": "page", "page-size": "pageSize"},
		},
		"dev.get_mcp_detail": {
			"dev deap-agent mcp query", "get_mcp_detail", "available",
			map[string]string{"mcp-id": "mcpId"},
		},
	}
	canonicals := make([]string, 0, len(wants))
	for canonical := range wants {
		canonicals = append(canonicals, canonical)
	}
	payload := schemaContractPayloadForBoundCanonicals(t, NewRootCommand(), canonicals...)
	for canonical, want := range wants {
		tool := payload.Tools[canonical]
		if got := schemaContractString(tool["primary_cli_path"]); got != want.cliPath {
			t.Errorf("%s primary_cli_path = %q, want %q", canonical, got, want.cliPath)
		}
		if got := schemaContractString(tool["availability"]); got != want.availability {
			t.Errorf("%s availability = %q, want %q", canonical, got, want.availability)
		}
		if want.availability == "unavailable" || want.tool == "" {
			if got := schemaContractString(tool["interface_mode"]); got != "composite" {
				t.Errorf("%s interface_mode = %q, want composite", canonical, got)
			}
			if ref := schemaInterfaceObject(tool["interface_ref"]); len(ref) != 0 {
				t.Errorf("%s composite unexpectedly exposes interface_ref %#v", canonical, ref)
			}
		} else {
			ref := schemaInterfaceObject(tool["interface_ref"])
			if got := schemaContractString(ref["product_id"]); got != "deap-dev" {
				t.Errorf("%s interface product = %q, want deap-dev", canonical, got)
			}
			if got := schemaContractString(ref["rpc_name"]); got != want.tool {
				t.Errorf("%s interface rpc = %q, want %q", canonical, got, want.tool)
			}
		}
		parameters := schemaContractMap(tool["parameters"])
		if len(parameters) != len(want.parameters) {
			t.Errorf("%s parameter count = %d, want %d: %#v", canonical, len(parameters), len(want.parameters), parameters)
		}
		for flagName, property := range want.parameters {
			parameter := parameters[flagName]
			if parameter == nil {
				t.Errorf("%s missing parameter %s", canonical, flagName)
				continue
			}
			if got := schemaContractString(parameter["property"]); got != property {
				t.Errorf("%s parameter %s property = %q, want %q", canonical, flagName, got, property)
			}
		}
	}
}
