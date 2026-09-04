package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestSetAndLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-secret"); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnv(home)
	if err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-secret" {
		t.Errorf("env = %v", env)
	}
}

func TestTheEnvFileIsNotWorldReadable(t *testing.T) {
	home := t.TempDir()
	if err := SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(EnvPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file is %v, want 0600: this is where the token lives instead of the plist",
			info.Mode().Perm())
	}
}

func TestSettingAValueTwiceReplacesRatherThanAppends(t *testing.T) {
	home := t.TempDir()
	_ = SetEnv(home, "TOKEN", "first")
	_ = SetEnv(home, "TOKEN", "second")
	body, _ := os.ReadFile(EnvPath(home))
	if strings.Count(string(body), "TOKEN=") != 1 {
		t.Errorf("the file accumulated duplicates:\n%s", body)
	}
	env, _ := LoadEnv(home)
	if env["TOKEN"] != "second" {
		t.Errorf("value = %q, want the newer one", env["TOKEN"])
	}
}

func TestAnEmptyValueRemovesTheKey(t *testing.T) {
	home := t.TempDir()
	_ = SetEnv(home, "TOKEN", "value")
	_ = SetEnv(home, "TOKEN", "")
	env, _ := LoadEnv(home)
	if _, present := env["TOKEN"]; present {
		t.Error("setting an empty value should remove the key, not store an empty one")
	}
}

func TestMissingEnvFileIsNotAnError(t *testing.T) {
	env, err := LoadEnv(t.TempDir())
	if err != nil || len(env) != 0 {
		t.Errorf("env=%v err=%v, want an empty map and no error on a fresh install", env, err)
	}
}

func TestCommentsBlankLinesAndStraySpaceAreIgnored(t *testing.T) {
	home := t.TempDir()
	body := "# a comment\n\nA=1\n  CLAUDE_CODE_OAUTH_TOKEN=sk-ant-secret  \n"
	if err := os.WriteFile(EnvPath(home), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnv(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 {
		t.Fatalf("env has %d entries, want 2: %v", len(env), env)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-secret" {
		t.Errorf("token = %q, want surrounding whitespace stripped. A token pasted with a stray "+
			"space is the exact shape of a bug we have already hit once", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
}
