package policy

import (
	"context"
	"testing"

	"github.com/vivek5200/axlang/parser"
	regoCompiler "github.com/vivek5200/axlang/targets/rego"
)

func TestGeneratedMixedBooleanRegoEvaluatesPrecedenceAccurately(t *testing.T) {
	source := `guardrail "g" { deny when { (actor.type == "autonomous_llm" or actor.type == "bot") and (diff.touches("auth/**") or diff.touches("db/**")) } } heal "h" on failure(stage.verify) { max_attempts = 1 allow when { true } }`
	file, diagnostics := parser.New("policy.ax", source).Parse()
	if len(diagnostics) != 0 {
		t.Fatalf("parse diagnostics: %+v", diagnostics)
	}
	module, err := regoCompiler.Compile(file)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewGate(module)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		actor  string
		files  []string
		denied bool
	}{
		{"autonomous auth", "autonomous_llm", []string{"auth/login.go"}, true},
		{"bot db", "bot", []string{"db/schema.sql"}, true},
		{"human auth", "human", []string{"auth/login.go"}, false},
		{"autonomous unrelated", "autonomous_llm", []string{"pkg/calc/calc.go"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision, err := gate.Evaluate(context.Background(), ActionIntent{Actor: Actor{Type: test.actor}, ModifiedFiles: test.files, Diff: Diff{ModifiesTests: "false"}})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Denied != test.denied || decision.Allowed == test.denied {
				t.Fatalf("decision=%+v denied want %v", decision, test.denied)
			}
		})
	}
}
