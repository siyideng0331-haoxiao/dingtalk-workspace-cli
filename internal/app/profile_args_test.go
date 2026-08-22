package app

import (
	"io"
	"os"
	"reflect"
	"testing"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
)

func TestNormalizeProfileFlagArgsAcceptsUnquotedCommaContinuation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root profile before command",
			args: []string{"--mock", "--profile", "corpA,", "corpB", "contact", "user", "get-self"},
			want: []string{"--mock", "--profile", "corpA,corpB", "contact", "user", "get-self"},
		},
		{
			name: "profile after leaf command",
			args: []string{"contact", "user", "get-self", "--profile", "corpA,", "corpB", "--format", "json"},
			want: []string{"contact", "user", "get-self", "--profile", "corpA,corpB", "--format", "json"},
		},
		{
			name: "equals form",
			args: []string{"--profile=corpA,", "corpB", "contact", "user", "get-self"},
			want: []string{"--profile=corpA,corpB", "contact", "user", "get-self"},
		},
		{
			name: "three profiles",
			args: []string{"--profile", "corpA,", "corpB,", "corpC", "contact", "user", "get-self"},
			want: []string{"--profile", "corpA,corpB,corpC", "contact", "user", "get-self"},
		},
		{
			name: "already quoted by shell remains unchanged",
			args: []string{"--profile", "corpA, corpB", "contact", "user", "get-self"},
			want: []string{"--profile", "corpA, corpB", "contact", "user", "get-self"},
		},
		{
			name: "single profile remains unchanged",
			args: []string{"--profile", "corpA", "contact", "user", "get-self"},
			want: []string{"--profile", "corpA", "contact", "user", "get-self"},
		},
		{
			name: "trailing comma before next flag remains validation input",
			args: []string{"--profile", "corpA,", "--format", "json", "contact", "user", "get-self"},
			want: []string{"--profile", "corpA,", "--format", "json", "contact", "user", "get-self"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := normalizeProfileFlagArgs(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeProfileFlagArgs() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestPreparseProfileFlagUsesNormalizedProfileArgs(t *testing.T) {
	got := preparseProfileFlag([]string{"--profile", "corpA,", "corpB", "contact", "user", "get-self"})
	if got != "corpA,corpB" {
		t.Fatalf("preparseProfileFlag() = %q, want corpA,corpB", got)
	}
}

func TestNormalizeProcessProfileArgsRestoresOriginalArgv(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = []string{"dws", "--profile", "corpA,", "corpB", "contact", "user", "get-self"}
	restore := normalizeProcessProfileArgs()
	if want := []string{"dws", "--profile", "corpA,corpB", "contact", "user", "get-self"}; !reflect.DeepEqual(os.Args, want) {
		t.Fatalf("os.Args after normalize = %#v, want %#v", os.Args, want)
	}
	restore()
	if want := []string{"dws", "--profile", "corpA,", "corpB", "contact", "user", "get-self"}; !reflect.DeepEqual(os.Args, want) {
		t.Fatalf("os.Args after restore = %#v, want %#v", os.Args, want)
	}
}

func TestRootUsesDWSProfileEnvironmentWhenFlagIsAbsent(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() {
		os.Args = oldArgs
		authpkg.SetRuntimeProfile("")
	})
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	t.Setenv(authpkg.EnvProfile, "  corp_env:user_env  ")
	authpkg.SetRuntimeProfile("")
	os.Args = []string{"dws", "version"}

	root := NewRootCommand()
	if got := authpkg.RuntimeProfile(); got != "corp_env:user_env" {
		t.Fatalf("runtime profile after root construction = %q, want environment selector", got)
	}

	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := authpkg.RuntimeProfile(); got != "corp_env:user_env" {
		t.Fatalf("runtime profile after command execution = %q, want environment selector", got)
	}
}

func TestRootProfileFlagOverridesDWSProfileEnvironment(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() {
		os.Args = oldArgs
		authpkg.SetRuntimeProfile("")
	})
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	t.Setenv(authpkg.EnvProfile, "corp_env:user_env")
	authpkg.SetRuntimeProfile("")
	os.Args = []string{"dws", "--profile", "corp_flag:user_flag", "version"}

	root := NewRootCommand()
	if got := authpkg.RuntimeProfile(); got != "corp_flag:user_flag" {
		t.Fatalf("runtime profile after root construction = %q, want explicit flag selector", got)
	}

	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--profile", "corp_flag:user_flag", "version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := authpkg.RuntimeProfile(); got != "corp_flag:user_flag" {
		t.Fatalf("runtime profile after command execution = %q, want explicit flag selector", got)
	}
}
