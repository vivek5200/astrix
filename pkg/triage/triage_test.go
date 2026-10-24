package triage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeLogRemovesVolatileNoise(t *testing.T) {
	got := SanitizeLog("\x1b[31m2025-01-02T03:04:05Z\x1b[0m id=123e4567-e89b-12d3-a456-426614174000 ptr=0xABCDEF0123 failed")
	for _, volatile := range []string{"\x1b", "2025-01-02", "123e4567", "0xABCDEF"} {
		if strings.Contains(got, volatile) {
			t.Errorf("volatile token %q remains in %q", volatile, got)
		}
	}
}

type fakeModel struct {
	patches []string
	calls   int
}

func (m *fakeModel) ProposePatch(context.Context, string) (string, error) {
	if m.calls >= len(m.patches) {
		return "", errors.New("no patch")
	}
	p := m.patches[m.calls]
	m.calls++
	return p, nil
}

type fakeSandbox struct {
	calls int
	fail  bool
}

func (s *fakeSandbox) ApplyAndTest(context.Context, string) error {
	s.calls++
	if s.fail {
		return errors.New("tests failed")
	}
	return nil
}

func TestValidatePatchRejectsTestsAndTraversal(t *testing.T) {
	for _, patch := range []string{"--- a/pkg/foo_test.go\n+++ b/pkg/foo_test.go\n@@ -1 +1 @@\n-a\n+b\n", "--- a/../secret\n+++ b/../secret\n@@ -1 +1 @@\n-a\n+b\n"} {
		if err := ValidatePatch(patch, false); err == nil {
			t.Fatalf("accepted unsafe patch %q", patch)
		}
	}
}

func TestRepairCircuitBreakerAndSuccessfulRetry(t *testing.T) {
	model := &fakeModel{patches: []string{"not a diff", "--- a/pkg/fix.go\n+++ b/pkg/fix.go\n@@ -1 +1 @@\n-old\n+new\n"}}
	sandbox := &fakeSandbox{fail: true}
	p := Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 4}
	if _, err := p.Repair(context.Background(), FailureContext{}); err == nil {
		t.Fatal("expected attempt limit validation")
	}
	p.MaxAttempts = 3
	if _, err := p.Repair(context.Background(), FailureContext{}); err == nil {
		t.Fatal("expected sandbox failure")
	}
	if model.calls != 2 || sandbox.calls != 1 {
		t.Fatalf("model calls=%d sandbox calls=%d", model.calls, sandbox.calls)
	}
}

func TestRepairReturnsOnlySandboxPassingPatch(t *testing.T) {
	patch := "--- a/pkg/fix.go\n+++ b/pkg/fix.go\n@@ -1 +1 @@\n-old\n+new\n"
	model := &fakeModel{patches: []string{patch}}
	sandbox := &fakeSandbox{}
	result, err := (&Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 1}).Repair(context.Background(), FailureContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Patch != patch || result.Attempts != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestOpenAICompatModelReturnsUnifiedDiff(t *testing.T) {
	patch := "--- a/pkg/fix.go\n+++ b/pkg/fix.go\n@@ -1 +1 @@\n-old\n+new\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "qwen" {
			t.Errorf("model=%q", request.Model)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "```diff\n" + patch + "```"}}}})
	}))
	defer server.Close()
	model := OpenAICompatModel{Endpoint: server.URL, APIKey: "key", Model: "qwen"}
	got, err := model.ProposePatch(context.Background(), "fix this")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(patch) {
		t.Fatalf("patch=%q", got)
	}
}
