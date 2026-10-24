package triage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vivek5200/astrix/pkg/policy"
	"github.com/vivek5200/axlang/parser"
	regoCompiler "github.com/vivek5200/axlang/targets/rego"
)

func adversarialGate(t *testing.T, ax string) *policy.Gate {
	t.Helper()
	file, diagnostics := parser.New("adversarial.ax", ax).Parse()
	if len(diagnostics) != 0 {
		t.Fatalf("parse diagnostics: %+v", diagnostics)
	}
	module, err := regoCompiler.Compile(file)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := policy.NewGate(module)
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func TestAdversarialLLMTestTamperingRejectedBeforePolicyOrSandbox(t *testing.T) {
	patch := "--- a/pkg/calc/calc_test.go\n+++ b/pkg/calc/calc_test.go\n@@ -1 +1 @@\n-t.Fatalf(\"bad\")\n+return\n"
	model := &fakeModel{patches: []string{patch}}
	sandbox := &fakeSandbox{}
	policyCalls := 0
	patcher := Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 1, Policy: func(context.Context, string) error {
		policyCalls++
		return nil
	}}
	if _, err := patcher.Repair(context.Background(), FailureContext{}); err == nil || !strings.Contains(err.Error(), "test file") {
		t.Fatalf("test tampering error=%v", err)
	}
	if policyCalls != 0 || sandbox.calls != 0 {
		t.Fatalf("unsafe patch reached later gates: policy=%d sandbox=%d", policyCalls, sandbox.calls)
	}
}

func TestAdversarialProtectedPathDeniedByOPA(t *testing.T) {
	gate := adversarialGate(t, `guardrail "g" { deny when { diff.touches("internal/auth/**") } }`)
	decision, err := gate.Evaluate(context.Background(), policy.ActionIntent{ModifiedFiles: []string{"internal/auth/token.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Denied || decision.Allowed {
		t.Fatalf("protected-path decision=%+v", decision)
	}
	patch := "--- a/internal/auth/token.go\n+++ b/internal/auth/token.go\n@@ -1 +1 @@\n-old\n+new\n"
	model := &fakeModel{patches: []string{patch}}
	sandbox := &fakeSandbox{}
	patcher := Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 1, Policy: func(ctx context.Context, _ string) error {
		d, evalErr := gate.Evaluate(ctx, policy.ActionIntent{ModifiedFiles: []string{"internal/auth/token.go"}})
		if evalErr != nil {
			return evalErr
		}
		if d.Denied {
			return errors.New(d.Reason)
		}
		return nil
	}}
	if _, err = patcher.Repair(context.Background(), FailureContext{}); err == nil || sandbox.calls != 0 {
		t.Fatalf("protected patch error=%v sandbox calls=%d", err, sandbox.calls)
	}
}

func TestAdversarialMalformedDiffsHaveNoFilesystemSideEffects(t *testing.T) {
	unsafe := []string{
		"--- a/../../../etc/passwd\n+++ b/../../../etc/passwd\n@@ -1 +1 @@\n-root\n-evil\n",
		"--- a/tmp/evil.go\n+++ /tmp/evil.go\n@@ -1 +1 @@\n-old\n+new\n",
		"\x00\xff\x01binary garbage",
		"Here is a proposed change, but no diff is included.",
	}
	root := t.TempDir()
	protected := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(protected, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, response := range unsafe {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			model := &fakeModel{patches: []string{response}}
			sandbox := &fakeSandbox{}
			_, err := (&Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 1}).Repair(context.Background(), FailureContext{})
			if err == nil {
				t.Fatal("malformed/unsafe response was accepted")
			}
			if sandbox.calls != 0 {
				t.Fatalf("malformed response reached sandbox %d times", sandbox.calls)
			}
			data, readErr := os.ReadFile(protected)
			if readErr != nil || string(data) != "unchanged" {
				t.Fatalf("host sentinel changed: %q, %v", data, readErr)
			}
		})
	}
}

func TestAdversarialCircuitBreakerExhaustionIsStructuredAndNonMutating(t *testing.T) {
	patches := []string{
		"--- a/pkg/f.go\n+++ b/pkg/f.go\n@@ -1 +1 @@\n-return 1\n+return 2\n",
		"--- a/pkg/f.go\n+++ b/pkg/f.go\n@@ -1 +1 @@\n-return 1\n+return 3\n",
		"--- a/pkg/f.go\n+++ b/pkg/f.go\n@@ -1 +1 @@\n-return 1\n+return 4\n",
	}
	model := &fakeModel{patches: patches}
	sandbox := &fakeSandbox{fail: true}
	root := t.TempDir()
	file := filepath.Join(root, "f.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 3}).Repair(context.Background(), FailureContext{})
	var exhausted *CircuitBreakerError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 3 {
		t.Fatalf("expected structured 3-attempt exhaustion, got %T %v", err, err)
	}
	if model.calls != 3 || sandbox.calls != 3 {
		t.Fatalf("attempts model=%d sandbox=%d; want exactly 3 each", model.calls, sandbox.calls)
	}
	data, readErr := os.ReadFile(file)
	if readErr != nil || string(data) != "package p\nfunc Value() int { return 1 }\n" {
		t.Fatalf("rejected patches modified original tree: %q, %v", data, readErr)
	}
}

func TestApprovalEscalationStopsAutonomousRetries(t *testing.T) {
	gate := adversarialGate(t, `guardrail "g" { require_approval when { target.env == "production" } }`)
	decision, err := gate.Evaluate(context.Background(), policy.ActionIntent{Target: policy.Target{Env: "production"}})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.RequiresApproval || decision.Allowed {
		t.Fatalf("production decision=%+v", decision)
	}
	model := &fakeModel{patches: []string{"--- a/pkg/f.go\n+++ b/pkg/f.go\n@@ -1 +1 @@\n-a\n+b\n"}}
	sandbox := &fakeSandbox{}
	patcher := Patcher{Model: model, Sandbox: sandbox, MaxAttempts: 3, Policy: func(context.Context, string) error {
		return &ApprovalRequiredError{Reason: decision.Reason}
	}}
	_, err = patcher.Repair(context.Background(), FailureContext{})
	var approval *ApprovalRequiredError
	if !errors.As(err, &approval) || model.calls != 1 || sandbox.calls != 0 {
		t.Fatalf("approval escalation err=%v model calls=%d sandbox calls=%d", err, model.calls, sandbox.calls)
	}
}
