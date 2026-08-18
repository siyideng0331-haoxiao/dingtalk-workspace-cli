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
		"deap.create_digital_employee": {
			"deap manage create", "create_digital_employee", "write", "medium", "not_required",
			map[string]string{
				"name": "name", "description": "description", "dept-id": "deptId", "dept-name": "deptName",
				"icon": "icon", "profile-json": "digitalTagEmployeeProfile",
				"employee-no":    "digitalTagEmployeeProfile.employeeNo",
				"position-name":  "digitalTagEmployeeProfile.positionName",
				"supervisor-uid": "digitalTagEmployeeProfile.directSupervisorUid",
				"response-mode":  "digitalTagEmployeeProfile.responseMode",
			},
		},
		"deap.get_digital_employee_detail": {
			"deap manage detail", "get_digital_employee_detail", "read", "low", "not_required",
			map[string]string{"assistant-id": "assistantId", "type": "type"},
		},
		"deap.list_digital_employees": {
			"deap manage list", "list_digital_employees", "read", "low", "not_required",
			map[string]string{"keyword": "keyword", "page": "page", "page-size": "pageSize"},
		},
		"deap.update_digital_employee_draft": {
			"deap manage save-draft", "update_digital_employee_draft", "write", "high", "user_required",
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
		"deap.publish_digital_employee": {
			"deap manage publish", "publish_digital_employee", "write", "high", "user_required",
			map[string]string{"agent-uuid": "agentUuid", "allow-join-group": "allowJoinGroup"},
		},
		"deap.delete_digital_employee": {
			"deap manage delete", "delete_digital_employee", "destructive", "high", "user_required",
			map[string]string{"agent-uuid": "agentUuid"},
		},
		"deap.query_de_run_status": {
			"deap observe run-status", "query_de_run_status", "read", "low", "not_required",
			map[string]string{"source-id": "sourceId", "source-type": "sourceType", "assistant-id": "assistantId"},
		},
		"deap.query_de_trace": {
			"deap observe trace", "query_de_trace", "read", "high", "not_required",
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
		"deap.create_skill_from_file": {
			"deap skill create", "", "available",
			map[string]string{"agent-uuid": "agentUuid", "file": "file"},
		},
		"deap.list_skills": {
			"deap skill list", "list_skills", "available",
			map[string]string{"agent-uuid": "agentUuid", "snapshot": "snapshot"},
		},
		"deap.get_skill_detail": {
			"deap skill query", "get_skill_detail", "available",
			map[string]string{"agent-uuid": "agentUuid", "skill-id": "skillId", "snapshot": "snapshot"},
		},
		"deap.create_mcp": {
			"deap mcp create", "create_mcp", "available",
			map[string]string{"config-file": "config"},
		},
		"deap.list_mcps": {
			"deap mcp list", "list_mcps", "available",
			map[string]string{"keywords": "keywords", "page": "page", "page-size": "pageSize"},
		},
		"deap.get_mcp_detail": {
			"deap mcp query", "get_mcp_detail", "available",
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
