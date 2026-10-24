package policy

import (
	"context"
	"github.com/vivek5200/axlang/parser"
	regoCompiler "github.com/vivek5200/axlang/targets/rego"
	"testing"
)

func policyGate(t *testing.T) *Gate {
	t.Helper()
	source := `guardrail "g" { deny when { actor.type == "autonomous_llm" and diff.touches("internal/auth/**") } require_approval when { target.env == "production" } } heal "h" on failure(stage.s) { max_attempts = 1 allow when { actor.type == "autonomous_llm" and diff.modifies_tests("false") } }`
	file, diags := parser.New("policy.ax", source).Parse()
	if len(diags) > 0 {
		t.Fatalf("parse: %#v", diags)
	}
	module, err := regoCompiler.Compile(file)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewGate(module)
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func TestGateAllowsExplicitSafeAction(t *testing.T) {
	d, err := policyGate(t).Evaluate(context.Background(), ActionIntent{Actor: Actor{Type: "autonomous_llm"}, Target: Target{Env: "staging"}, Diff: Diff{ModifiesTests: "false"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.Denied {
		t.Fatalf("decision=%+v", d)
	}
}

func TestGateDeniesCriticalPathAndRequiresApproval(t *testing.T) {
	gate := policyGate(t)
	d, err := gate.Evaluate(context.Background(), ActionIntent{Actor: Actor{Type: "autonomous_llm"}, Target: Target{Env: "staging"}, ModifiedFiles: []string{"internal/auth/login.go"}, Diff: Diff{ModifiesTests: "false"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Denied || d.Allowed {
		t.Fatalf("critical path decision=%+v", d)
	}
	d, err = gate.Evaluate(context.Background(), ActionIntent{Actor: Actor{Type: "autonomous_llm"}, Target: Target{Env: "production"}, Diff: Diff{ModifiesTests: "false"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.RequiresApproval || d.Allowed {
		t.Fatalf("approval decision=%+v", d)
	}
	d, err = gate.Evaluate(context.Background(), ActionIntent{Actor: Actor{Type: "autonomous_llm"}, Target: Target{Env: "production"}, Approved: true, Diff: Diff{ModifiesTests: "false"}})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatalf("approved decision=%+v", d)
	}
}
func TestGateFailsClosedOnInvalidModule(t *testing.T) {
	if _, err := NewGate("not rego!"); err == nil {
		t.Fatal("invalid policy module was accepted")
	}
}
