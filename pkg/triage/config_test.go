package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnvPreservesProcessEnvironment(t *testing.T) {
	t.Setenv("ASTRIX_TEST_EXISTING", "process")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# demo\nexport ASTRIX_TEST_FROM_FILE='from-file'\nASTRIX_TEST_EXISTING=dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ASTRIX_TEST_FROM_FILE"); got != "from-file" {
		t.Fatalf("loaded value=%q", got)
	}
	if got := os.Getenv("ASTRIX_TEST_EXISTING"); got != "process" {
		t.Fatalf("process environment was overwritten: %q", got)
	}
}

func TestResolveLLMConfigPrecedenceAndSelfHostedBase(t *testing.T) {
	values := map[string]string{
		"ASTRIX_LLM_API_KEY": "astrix-key", "OPENROUTER_API_KEY": "router-key", "OPENAI_API_KEY": "openai-key",
		"ASTRIX_LLM_BASE_URL": "https://llm.example/v1/", "ASTRIX_LLM_MODEL": "custom/model",
	}
	get := func(key string) string { return values[key] }
	cfg := ResolveLLMConfig(get, "file/model")
	if cfg.APIKey != "astrix-key" || cfg.Endpoint != "https://llm.example/v1/chat/completions" || cfg.Model != "custom/model" {
		t.Fatalf("resolved config=%+v", cfg)
	}
	values["ASTRIX_LLM_API_KEY"] = ""
	if cfg = ResolveLLMConfig(get, "file/model"); cfg.APIKey != "router-key" {
		t.Fatalf("OpenRouter key priority=%q", cfg.APIKey)
	}
	delete(values, "ASTRIX_LLM_MODEL")
	if cfg = ResolveLLMConfig(get, "file/model"); cfg.Model != "file/model" {
		t.Fatalf("Astrixfile model fallback=%q", cfg.Model)
	}
	delete(values, "ASTRIX_LLM_BASE_URL")
	if cfg = ResolveLLMConfig(get, ""); !strings.HasPrefix(cfg.Endpoint, "https://openrouter.ai/api/v1/") {
		t.Fatalf("OpenRouter endpoint default=%q", cfg.Endpoint)
	}
}
