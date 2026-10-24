package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatModel calls a Chat Completions-compatible endpoint when configured.
type OpenAICompatModel struct {
	Endpoint string
	APIKey   string
	Model    string
	Client   *http.Client
}

func (m OpenAICompatModel) ProposePatch(ctx context.Context, prompt string) (string, error) {
	if m.Endpoint == "" || m.Model == "" {
		return "", fmt.Errorf("LLM endpoint and model are required")
	}
	body, err := json.Marshal(map[string]any{"model": m.Model, "temperature": 0, "messages": []any{
		map[string]string{"role": "system", "content": "You produce precise source-code repairs. Return only a valid unified diff with --- a/<path>, +++ b/<path>, and @@ hunk headers. Never modify test files or assertions. Do not include prose or Markdown fences."},
		map[string]string{"role": "user", "content": prompt},
	}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LLM endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return "", err
	}
	if response.Error != nil {
		return "", fmt.Errorf("LLM error: %s", response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no patch choice")
	}
	return response.Choices[0].Message.Content, nil
}

func stripDiffFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		if i := strings.IndexByte(value, '\n'); i >= 0 {
			value = value[i+1:]
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, "```"))
	}
	return value
}
