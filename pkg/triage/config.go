package triage

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs without overwriting variables already set by
// the process. It intentionally supports a small, predictable .env subset.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		at := strings.IndexByte(line, '=')
		if at <= 0 {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key := strings.TrimSpace(line[:at])
		if !validEnvKey(key) {
			return fmt.Errorf("%s:%d: invalid environment variable name %q", path, lineNo, key)
		}
		value := strings.TrimSpace(line[at+1:])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func validEnvKey(key string) bool {
	for i, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return key != ""
}

// LLMConfig contains resolved OpenAI-compatible endpoint credentials.
type LLMConfig struct{ Endpoint, APIKey, Model string }

// ResolveLLMConfig applies Astrix, OpenRouter, then OpenAI key precedence. A
// custom ASTRIX_LLM_BASE_URL supports self-hosted OpenAI-compatible endpoints.
func ResolveLLMConfig(getenv func(string) string, astrixfileModel string) LLMConfig {
	key := getenv("ASTRIX_LLM_API_KEY")
	provider := "astrix"
	if key == "" {
		key = getenv("OPENROUTER_API_KEY")
		if key != "" {
			provider = "openrouter"
		}
	}
	if key == "" {
		key = getenv("OPENAI_API_KEY")
		if key != "" {
			provider = "openai"
		}
	}
	endpoint := strings.TrimRight(getenv("ASTRIX_LLM_ENDPOINT"), "/")
	if endpoint == "" {
		base := strings.TrimRight(getenv("ASTRIX_LLM_BASE_URL"), "/")
		if base == "" {
			switch provider {
			case "openrouter":
				base = "https://openrouter.ai/api/v1"
			case "openai":
				base = "https://api.openai.com/v1"
			default:
				base = "http://localhost:11434/v1"
			}
		}
		if strings.HasSuffix(base, "/chat/completions") {
			endpoint = base
		} else {
			endpoint = base + "/chat/completions"
		}
	}
	model := getenv("ASTRIX_LLM_MODEL")
	if model == "" {
		model = astrixfileModel
	}
	if model == "" {
		model = "qwen/qwen-2.5-coder-32b-instruct"
	}
	return LLMConfig{Endpoint: endpoint, APIKey: key, Model: model}
}
