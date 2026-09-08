package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkScratchOCEnv(t *testing.T) *ocEnv {
	t.Helper()
	base := t.TempDir()
	cfgDir := filepath.Join(base, "config", "opencode")
	dataDir := filepath.Join(base, "data", "opencode")
	os.MkdirAll(cfgDir, 0o755)
	os.MkdirAll(dataDir, 0o755)
	e := &ocEnv{ConfigDir: cfgDir, DataDir: dataDir}
	return e
}

func TestSetModelAndRead(t *testing.T) {
	e := mkScratchOCEnv(t)
	start := "{\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"model\": \"deepseek/deepseek-v4-flash\",\n  \"small_model\": \"deepseek/deepseek-v4-flash\"\n}\n"
	if err := os.WriteFile(e.configPath(), []byte(start), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.currentModel(); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("currentModel=%q", got)
	}
	if err := e.setModel("anthropic/claude-sonnet-4-5"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.configPath())
	s := string(b)
	if !strings.Contains(s, `"model": "anthropic/claude-sonnet-4-5"`) {
		t.Fatalf("model not written:\n%s", s)
	}
	if !strings.Contains(s, `"small_model": "anthropic/claude-sonnet-4-5"`) {
		t.Fatalf("small_model not written:\n%s", s)
	}
	if got := e.currentModel(); got != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("re-read=%q", got)
	}
	// بقیه فایل حفظ شده باشد
	if !strings.Contains(s, "$schema") {
		t.Fatalf("schema line lost:\n%s", s)
	}
}

func TestSetModelCreatesFile(t *testing.T) {
	e := mkScratchOCEnv(t)
	if err := e.setModel("openai/gpt-5"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.configPath())
	if !strings.Contains(string(b), `"model": "openai/gpt-5"`) {
		t.Fatalf("not created:\n%s", string(b))
	}
}

func TestSetModelRejectsBadFormat(t *testing.T) {
	e := mkScratchOCEnv(t)
	if err := e.setModel("nodash"); err == nil {
		t.Fatal("expected error for missing provider/model")
	}
}

func TestAuthKeyCRUD(t *testing.T) {
	e := mkScratchOCEnv(t)
	if e.hasKey("deepseek") {
		t.Fatal("should not have key initially")
	}
	if err := e.addAuthKey("deepseek", "sk-abc123"); err != nil {
		t.Fatal(err)
	}
	if !e.hasKey("deepseek") {
		t.Fatal("key should exist now")
	}
	got := e.providersConfigured()
	if len(got) != 1 || got[0] != "deepseek" {
		t.Fatalf("providersConfigured=%v", got)
	}
	if err := e.removeAuthKey("deepseek"); err != nil {
		t.Fatal(err)
	}
	if e.hasKey("deepseek") {
		t.Fatal("key should be gone")
	}
}
