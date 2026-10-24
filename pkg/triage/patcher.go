package triage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vivek5200/astrix/pkg/runner"
)

// Model returns a proposed unified diff for the supplied repair prompt.
type Model interface {
	ProposePatch(context.Context, string) (string, error)
}

// Sandbox validates and tests a patch in an isolated environment.
type Sandbox interface {
	ApplyAndTest(context.Context, string) error
}

// PolicyCheck enforces policy on a proposed patch before sandbox execution.
type PolicyCheck func(context.Context, string) error

// Patcher coordinates a model, policy gate, and isolated test sandbox.
type Patcher struct {
	Model            Model
	Sandbox          Sandbox
	MaxAttempts      int
	AllowTestChanges bool
	Policy           PolicyCheck
}

// Result contains the first patch that passed validation, policy, and sandbox tests.
type Result struct {
	Patch    string
	Attempts int
}

// CircuitBreakerError reports bounded repair-loop exhaustion without implying
// that any rejected patch was applied to the caller's working tree.
type CircuitBreakerError struct {
	Attempts int
	Cause    error
}

func (e *CircuitBreakerError) Error() string {
	return fmt.Sprintf("repair circuit breaker stopped after %d attempts: %v", e.Attempts, e.Cause)
}
func (e *CircuitBreakerError) Unwrap() error { return e.Cause }

// ApprovalRequiredError stops autonomous retries when policy requires a human.
type ApprovalRequiredError struct{ Reason string }

func (e *ApprovalRequiredError) Error() string {
	if e.Reason == "" {
		return "policy requires human approval"
	}
	return "policy requires human approval: " + e.Reason
}

// Repair performs at most three repair attempts. It does not modify the caller's tree;
// the accepted diff is returned for an explicit apply step by the caller.
func (p *Patcher) Repair(ctx context.Context, base FailureContext) (Result, error) {
	if p == nil || p.Model == nil || p.Sandbox == nil {
		return Result{}, fmt.Errorf("repair model and sandbox are required")
	}
	max := p.MaxAttempts
	if max == 0 {
		max = 3
	}
	if max < 1 || max > 3 {
		return Result{}, fmt.Errorf("max_attempts must be between 1 and 3")
	}
	var previous string
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		base.Attempt = attempt
		base.MaxAttempts = max
		base.PreviousPatch = previous
		base.PreviousError = ""
		if lastErr != nil {
			base.PreviousError = lastErr.Error()
		}
		base.AllowTestChanges = p.AllowTestChanges
		response, err := p.Model.ProposePatch(ctx, BuildPrompt(base))
		if err != nil {
			lastErr = fmt.Errorf("request repair patch: %w", err)
			continue
		}
		patch, err := ExtractUnifiedDiff(response)
		if err != nil {
			previous = response
			lastErr = err
			continue
		}
		if err = ValidatePatch(patch, p.AllowTestChanges); err != nil {
			previous = patch
			lastErr = err
			continue
		}
		if p.Policy != nil {
			if err = p.Policy(ctx, patch); err != nil {
				var approval *ApprovalRequiredError
				if errors.As(err, &approval) {
					return Result{}, approval
				}
				previous = patch
				lastErr = fmt.Errorf("policy rejected repair patch: %w", err)
				continue
			}
		}
		if err = p.Sandbox.ApplyAndTest(ctx, patch); err != nil {
			previous = patch
			lastErr = fmt.Errorf("sandbox rejected repair patch: %w", err)
			continue
		}
		return Result{Patch: patch, Attempts: attempt}, nil
	}
	return Result{}, &CircuitBreakerError{Attempts: max, Cause: lastErr}
}

var fileHeader = regexp.MustCompile(`^(?:\+\+\+|---)\s+([^\t]+)`)

// ValidatePatch checks unified-diff structure, path traversal, and test-file edits.
func ValidatePatch(diff string, allowTestChanges bool) error {
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("empty unified diff")
	}
	if !strings.Contains(diff, "@@ ") {
		return fmt.Errorf("patch contains no unified diff hunk")
	}
	files := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		match := fileHeader.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		name := strings.TrimSpace(match[1])
		if name == "/dev/null" {
			continue
		}
		name = strings.TrimPrefix(strings.TrimPrefix(name, "a/"), "b/")
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("patch path escapes workspace: %q", name)
		}
		files[filepath.ToSlash(clean)] = true
	}
	if len(files) == 0 {
		return fmt.Errorf("patch does not identify any files")
	}
	if !allowTestChanges {
		for path := range files {
			lower := strings.ToLower("/" + path)
			base := strings.ToLower(filepath.Base(path))
			if strings.HasSuffix(lower, "_test.go") || strings.Contains(lower, "/tests/") || strings.Contains(lower, "/test/") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx") || strings.HasSuffix(base, ".test.mts") || strings.HasSuffix(base, ".spec.mts") {
				return fmt.Errorf("patch modifies protected test file %q", path)
			}
		}
	}
	return nil
}

// DockerSandbox is kept as a source-compatible alias; its implementation lives
// with the execution runners in pkg/runner.
type DockerSandbox = runner.DockerSandbox

// WaitForRetryDelay is available for callers that add a paced model retry policy.
func WaitForRetryDelay(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
