package policy

import (
	"context"
	"fmt"

	"github.com/open-policy-agent/opa/rego"
)

// Decision is the fail-closed result of evaluating an ActionIntent.
type Decision struct {
	Allowed          bool   `json:"allowed"`
	Denied           bool   `json:"denied"`
	RequiresApproval bool   `json:"requires_approval"`
	Reason           string `json:"reason,omitempty"`
}

// Gate evaluates generated Rego v1 modules with embedded OPA.
type Gate struct{ query rego.PreparedEvalQuery }

// NewGate creates a policy gate. Empty modules are rejected so a missing policy
// cannot accidentally become an allow-all decision.
func NewGate(module string) (*Gate, error) {
	if module == "" {
		return nil, fmt.Errorf("policy module is empty")
	}
	query, err := rego.New(rego.Query("data.astrix.generated"), rego.Module("generated.rego", module)).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("prepare policy: %w", err)
	}
	return &Gate{query: query}, nil
}

// Evaluate runs the policy and denies on compilation, evaluation, or shape errors.
func (g *Gate) Evaluate(ctx context.Context, intent ActionIntent) (Decision, error) {
	deny := Decision{Denied: true, Allowed: false, Reason: "policy evaluation failed closed"}
	if g == nil {
		return deny, fmt.Errorf("policy gate is not configured")
	}
	results, err := g.query.Eval(ctx, rego.EvalInput(intent.Input()))
	if err != nil {
		return deny, fmt.Errorf("evaluate policy: %w", err)
	}
	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return deny, fmt.Errorf("policy returned no decision")
	}
	docs, ok := results[0].Expressions[0].Value.(map[string]interface{})
	if !ok {
		return deny, fmt.Errorf("policy result has unexpected type %T", results[0].Expressions[0].Value)
	}
	allowed, okA := docs["allow"].(bool)
	denied, okD := docs["deny"].(bool)
	approval, okR := docs["require_approval"].(bool)
	if !okA || !okD || !okR {
		return deny, fmt.Errorf("policy is missing one of allow, deny, or require_approval rules")
	}
	decision := Decision{Denied: denied, RequiresApproval: approval, Allowed: allowed && !denied && (!approval || intent.Approved)}
	if denied {
		decision.Reason = "an explicit deny rule matched"
	} else if approval && !intent.Approved {
		decision.Reason = "approval is required"
	} else if !allowed {
		decision.Reason = "no allow rule matched"
	}
	return decision, nil
}
