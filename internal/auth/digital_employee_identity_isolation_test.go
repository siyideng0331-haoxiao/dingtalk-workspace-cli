package auth

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDigitalEmployeeExactLoginPreservesOperatorCurrent(t *testing.T) {
	cleanupKeychain(t)
	configDir := t.TempDir()

	operator := testToken("operator-access", "corp_same", "同一组织")
	operator.UserID = "operator_uid"
	operator.UserName = "操作人"
	if err := SaveTokenData(configDir, operator); err != nil {
		t.Fatalf("SaveTokenData(operator) error = %v", err)
	}

	employee := testToken("employee-access", "corp_same", "同一组织")
	employee.UserID = "employee_uid"
	employee.UserName = "数字员工"
	previousRuntimeProfile := RuntimeProfile()
	SetRuntimeProfile("corp_same:employee_uid")
	if err := SaveTokenData(configDir, employee); err != nil {
		t.Fatalf("SaveTokenData(employee) error = %v", err)
	}
	SetRuntimeProfile(previousRuntimeProfile)
	t.Cleanup(func() { SetRuntimeProfile(previousRuntimeProfile) })

	loadedOperator, err := LoadTokenDataForProfile(configDir, "corp_same:operator_uid")
	if err != nil {
		t.Fatalf("LoadTokenDataForProfile(operator) error = %v", err)
	}
	if loadedOperator.AccessToken != "operator-access" {
		t.Fatalf("operator token = %q, want unchanged", loadedOperator.AccessToken)
	}
	loadedEmployee, err := LoadTokenDataForProfile(configDir, "corp_same:employee_uid")
	if err != nil {
		t.Fatalf("LoadTokenDataForProfile(employee) error = %v", err)
	}
	if loadedEmployee.AccessToken != "employee-access" {
		t.Fatalf("employee token = %q, want employee-access", loadedEmployee.AccessToken)
	}
	loadedDefault, err := LoadTokenData(configDir)
	if err != nil {
		t.Fatalf("LoadTokenData(default) error = %v", err)
	}
	if loadedDefault.UserID != "operator_uid" || loadedDefault.AccessToken != "operator-access" {
		t.Fatalf("default token changed to employee: %#v", loadedDefault)
	}
	cfg, err := LoadProfiles(configDir)
	if err != nil {
		t.Fatalf("LoadProfiles() error = %v", err)
	}
	if cfg.CurrentProfile != "corp_same:operator_uid" {
		t.Fatalf("current profile = %q, want operator", cfg.CurrentProfile)
	}
}

func TestDigitalEmployeeExactProfileInIsolatedConfigPreservesGlobalToken(t *testing.T) {
	cleanupKeychain(t)
	operatorConfigDir := t.TempDir()
	employeeConfigDir := t.TempDir()
	if err := SaveProfiles(employeeConfigDir, &ProfilesConfig{Version: profilesVersion}); err != nil {
		t.Fatalf("SaveProfiles(employee config) error = %v", err)
	}

	operator := testToken("operator-access", "corp_same", "同一组织")
	operator.UserID = "operator_uid"
	operator.UserName = "操作人"
	if err := SaveTokenData(operatorConfigDir, operator); err != nil {
		t.Fatalf("SaveTokenData(operator) error = %v", err)
	}

	previousRuntimeProfile := RuntimeProfile()
	SetRuntimeProfile("corp_same:employee_uid")
	t.Cleanup(func() { SetRuntimeProfile(previousRuntimeProfile) })

	employee := testToken("employee-access", "corp_same", "同一组织")
	employee.UserID = "employee_uid"
	employee.UserName = "数字员工"
	if err := SaveTokenData(employeeConfigDir, employee); err != nil {
		t.Fatalf("SaveTokenData(employee) error = %v", err)
	}

	global, err := LoadTokenDataKeychain()
	if err != nil {
		t.Fatalf("LoadTokenDataKeychain() error = %v", err)
	}
	if global.UserID != operator.UserID || global.AccessToken != operator.AccessToken {
		t.Fatalf("global token = %#v, want operator unchanged", global)
	}
	organization, err := LoadTokenDataKeychainForCorpID(operator.CorpID)
	if err != nil {
		t.Fatalf("LoadTokenDataKeychainForCorpID() error = %v", err)
	}
	if organization.UserID != operator.UserID || organization.AccessToken != operator.AccessToken {
		t.Fatalf("organization token = %#v, want operator unchanged", organization)
	}

	refreshedEmployee := *employee
	refreshedEmployee.AccessToken = "employee-access-refreshed"
	if err := SaveTokenData(employeeConfigDir, &refreshedEmployee); err != nil {
		t.Fatalf("SaveTokenData(refreshed employee) error = %v", err)
	}
	global, err = LoadTokenDataKeychain()
	if err != nil {
		t.Fatalf("LoadTokenDataKeychain(after refresh) error = %v", err)
	}
	if global.UserID != operator.UserID || global.AccessToken != operator.AccessToken {
		t.Fatalf("global token after refresh = %#v, want operator unchanged", global)
	}
	organization, err = LoadTokenDataKeychainForCorpID(operator.CorpID)
	if err != nil {
		t.Fatalf("LoadTokenDataKeychainForCorpID(after refresh) error = %v", err)
	}
	if organization.UserID != operator.UserID || organization.AccessToken != operator.AccessToken {
		t.Fatalf("organization token after refresh = %#v, want operator unchanged", organization)
	}

	loadedEmployee, err := LoadTokenDataForProfile(employeeConfigDir, "corp_same:employee_uid")
	if err != nil {
		t.Fatalf("LoadTokenDataForProfile(employee) error = %v", err)
	}
	if loadedEmployee.AccessToken != refreshedEmployee.AccessToken {
		t.Fatalf("employee token = %q, want %q", loadedEmployee.AccessToken, refreshedEmployee.AccessToken)
	}
}

func TestExchangeAuthCodeForIdentityRejectsMismatchBeforeSave(t *testing.T) {
	cleanupKeychain(t)
	configDir := t.TempDir()
	if err := SaveProfiles(configDir, &ProfilesConfig{Version: profilesVersion}); err != nil {
		t.Fatalf("SaveProfiles() error = %v", err)
	}

	previousExchange := oauthExchange
	previousSave := oauthSaveToken
	t.Cleanup(func() {
		oauthExchange = previousExchange
		oauthSaveToken = previousSave
	})
	oauthExchange = func(*OAuthProvider, context.Context, string) (*TokenData, error) {
		return &TokenData{
			AccessToken: "unexpected-access",
			CorpID:      "corp_unexpected",
		}, nil
	}
	var saveCalls atomic.Int32
	oauthSaveToken = func(string, *TokenData) error {
		saveCalls.Add(1)
		return nil
	}

	provider := NewOAuthProvider(configDir, nil)
	_, err := provider.ExchangeAuthCodeForIdentity(
		context.Background(), "one-time-code", "corp_expected", "employee_uid")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ExchangeAuthCodeForIdentity() error = %v, want identity mismatch", err)
	}
	if got := saveCalls.Load(); got != 0 {
		t.Fatalf("SaveTokenData calls = %d, want 0", got)
	}
}
