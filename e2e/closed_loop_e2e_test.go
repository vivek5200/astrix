package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vivek5200/astrix/pkg/app"
	"github.com/vivek5200/astrix/pkg/astgraph"
	"github.com/vivek5200/astrix/pkg/cache"
	"github.com/vivek5200/astrix/pkg/ingress"
	"github.com/vivek5200/astrix/pkg/policy"
	"github.com/vivek5200/astrix/pkg/triage"
)

func TestClosedControlLoop_SelfHealingAndPolicyGate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Astrixfile.ax", `guardrail "security_boundaries" {
  deny when { diff.touches("internal/auth/**") }
}
stage "verify" { scope = ast.blast_radius }
heal "triage_loop" on failure(stage.verify) {
  max_attempts = 3
  allow when { true }
}
`)
	write("go.mod", "module closedloop.example\n\ngo 1.23\n")
	write("pkg/mathutil/math.go", "package mathutil\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n")
	write("pkg/mathutil/mathutil_test.go", "package mathutil\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 { t.Fatalf(\"Add(2, 3) = %d, want 5\", got) }\n}\n")
	write("pkg/unrelated/unrelated.go", "package unrelated\n\nfunc Unrelated() int { return 7 }\n")
	write("pkg/unrelated/unrelated_test.go", "package unrelated\n\nimport \"testing\"\n\nfunc TestUnrelated(t *testing.T) { if got := Unrelated(); got != 7 { t.Fatalf(\"got %d\", got) } }\n")
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=Astrix E2E", "-c", "user.email=e2e@astrix.local", "commit", "-qm", "baseline")

	// Break only Add and capture an actual repository diff.
	write("pkg/mathutil/math.go", "package mathutil\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n")
	diff := git(t, root, "diff", "--unified=0", "HEAD")
	if len(ingress.ScanUnifiedDiffSecrets(diff)) != 0 {
		t.Fatal("fixture diff unexpectedly triggered secret ingress")
	}
	impact, changes, err := app.Impact(root, diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "pkg/mathutil/math.go" {
		t.Fatalf("diff changes=%+v", changes)
	}
	if len(impact.Tests) != 1 || impact.Tests[0] != "pkg/mathutil/mathutil_test.go::TestAdd" {
		t.Fatalf("blast radius should select only mathutil test, got %+v", impact)
	}
	for _, id := range impact.ImpactedSymbols {
		if strings.Contains(id, "unrelated") {
			t.Fatalf("unrelated package was impacted: %v", impact.ImpactedSymbols)
		}
	}

	testOutput, testErr := run(root, "go", "test", "./pkg/mathutil")
	if testErr == nil {
		t.Fatal("broken mathutil test unexpectedly passed")
	}
	if !strings.Contains(testOutput, "FAIL") {
		t.Fatalf("expected test failure output, got: %s", testOutput)
	}
	noisy := "2026-02-03T04:05:06Z run=123e4567-e89b-42d3-a456-426614174000 \x1b[31mrunner output\x1b[0m\n" + testOutput
	sanitized := triage.SanitizeLog(noisy)
	if strings.Contains(sanitized, "2026-02-03T04:05:06Z") || strings.Contains(sanitized, "123e4567-e89b-42d3-a456-426614174000") || strings.Contains(sanitized, "\x1b[") {
		t.Fatalf("sanitizer left volatile log content: %q", sanitized)
	}

	badPatch := "diff --git a/internal/auth/secret.go b/internal/auth/secret.go\nnew file mode 100644\n--- /dev/null\n+++ b/internal/auth/secret.go\n@@ -0,0 +1 @@\n+package auth\n"
	goodPatch := "diff --git a/pkg/mathutil/math.go b/pkg/mathutil/math.go\n--- a/pkg/mathutil/math.go\n+++ b/pkg/mathutil/math.go\n@@ -3,3 +3,3 @@\n func Add(a, b int) int {\n-\treturn a - b\n+\treturn a + b\n }\n"
	var requests int
	var promptBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Messages) == 0 {
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		promptBodies = append(promptBodies, request.Messages[len(request.Messages)-1].Content)
		patch := badPatch
		if requests >= 2 {
			patch = goodPatch
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": patch}}}})
	}))
	defer server.Close()

	gate, err := app.PolicyGate(filepath.Join(root, "Astrixfile.ax"))
	if err != nil {
		t.Fatal(err)
	}
	policyDenied, policyAllowed := false, false
	sandbox := patchSandbox{root: root, calls: 0}
	patcher := &triage.Patcher{
		Model:   triage.OpenAICompatModel{Endpoint: server.URL + "/v1/chat/completions", Model: "mock", Client: server.Client()},
		Sandbox: &sandbox, MaxAttempts: 3,
		Policy: func(ctx context.Context, patch string) error {
			paths, err := patchPaths(patch)
			if err != nil {
				return err
			}
			decision, err := gate.Evaluate(ctx, policy.ActionIntent{Actor: policy.Actor{Type: "autonomous_llm"}, Target: policy.Target{Env: "development"}, ModifiedFiles: paths, Diff: policy.Diff{ModifiesTests: "false"}})
			if err != nil {
				return err
			}
			if decision.Denied {
				policyDenied = true
				return fmt.Errorf("OPA denied patch: %s", decision.Reason)
			}
			if !decision.Allowed {
				return fmt.Errorf("OPA did not allow patch: %s", decision.Reason)
			}
			policyAllowed = true
			return nil
		},
	}
	result, err := patcher.Repair(ctx, triage.FailureContext{Impact: impact, SanitizedLog: sanitized})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || requests != 2 {
		t.Fatalf("expected two model attempts, attempts=%d requests=%d", result.Attempts, requests)
	}
	if !policyDenied || !policyAllowed {
		t.Fatalf("policy decisions: denied=%v allowed=%v", policyDenied, policyAllowed)
	}
	if sandbox.calls != 1 {
		t.Fatalf("sandbox should only test policy-approved patch; calls=%d", sandbox.calls)
	}
	if len(promptBodies) != 2 || !strings.Contains(promptBodies[0], "<volatile>") || strings.Contains(promptBodies[0], "2026-02-03T04:05:06Z") {
		t.Fatalf("sanitized failure prompt not sent: %#v", promptBodies)
	}
	if err := apply(root, result.Patch); err != nil {
		t.Fatal(err)
	}
	if output, err := run(root, "go", "test", "./pkg/mathutil"); err != nil {
		t.Fatalf("re-run impacted package: %v\n%s", err, output)
	}

	// Store the passing outcome by content-aware graph hash, then verify a second
	// no-change pass resolves the same test key from cache without running Go tests.
	fixedGraph, err := astgraph.LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	testID := "pkg/mathutil/mathutil_test.go::TestAdd"
	key := cache.SymbolSubgraphHash(fixedGraph, []string{testID})
	cacheStore := cache.Cache{Root: filepath.Join(root, ".astrix", "cache")}
	if err := cacheStore.Put(key, cache.TestOutcome{Passed: true}); err != nil {
		t.Fatal(err)
	}
	noChangeDiff := git(t, root, "diff", "--unified=0", "HEAD")
	noChangeImpact, _, err := app.Impact(root, noChangeDiff)
	if err != nil {
		t.Fatal(err)
	}
	if len(noChangeImpact.Tests) != 0 {
		t.Fatalf("expected no impacted tests with no diff: %+v", noChangeImpact)
	}
	secondGraph, err := astgraph.LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	secondKey := cache.SymbolSubgraphHash(secondGraph, []string{testID})
	outcome, hit, err := cacheStore.Get(secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if !hit || !outcome.Passed || key != secondKey {
		t.Fatalf("cache miss or changed key on unchanged source: hit=%v outcome=%+v", hit, outcome)
	}
	if sandbox.calls != 1 {
		t.Fatalf("cache-hit pass executed tests unexpectedly; sandbox calls=%d", sandbox.calls)
	}

	// Drive the shipped `astrix verify --fix` command with a local LLM endpoint
	// and a Docker shim that executes the isolated copy in this self-contained test.
	cliTemp, err := os.MkdirTemp("", "astrix-cli-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cliTemp)
	binary := filepath.Join(cliTemp, "astrix")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../cmd/astrix")
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build CLI: %v\n%s", e, output)
	}
	fakeBin := filepath.Join(cliTemp, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(cliTemp, "test-invocations")
	goWrapper := "#!/bin/sh\nif [ \"$1\" = test ]; then echo test >> \"$ASTRIX_TEST_LOG\"; fi\nexec \"$ASTRIX_REAL_GO\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(goWrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	dockerLog := filepath.Join(cliTemp, "docker-network")
	dockerWrapper := "#!/bin/sh\n[ \"$1\" = run ] || exit 64\nshift\nroot=\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --rm) shift ;;\n    --network=none) echo none >> \"$ASTRIX_DOCKER_LOG\"; shift ;;\n    --*) shift ;;\n    -v) case \"$2\" in *:/workspace) root=${2%:/workspace} ;; esac; shift 2 ;;\n    -w|-e) shift 2 ;;\n    *) image=$1; shift; break ;;\n  esac\ndone\n[ -n \"$root\" ] || exit 65\ncd \"$root\" || exit 66\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(dockerWrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	cliRequests := 0
	cliServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cliRequests++
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		patch := badPatch
		if cliRequests >= 2 {
			patch = goodPatch
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": patch}}}})
	}))
	defer cliServer.Close()
	cliEnv := append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "ASTRIX_REAL_GO="+filepath.Join(runtime.GOROOT(), "bin", "go"), "ASTRIX_TEST_LOG="+logPath, "ASTRIX_DOCKER_LOG="+dockerLog, "ASTRIX_LLM_ENDPOINT="+cliServer.URL+"/v1/chat/completions", "ASTRIX_LLM_MODEL=mock")
	write("pkg/mathutil/math.go", "package mathutil\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n")
	runCLI := func() string {
		t.Helper()
		cmd := exec.Command(binary, "verify", "--fix")
		cmd.Dir = root
		cmd.Env = cliEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("astrix verify: %v\n%s", err, output)
		}
		return string(output)
	}
	firstCLI := runCLI()
	if !strings.Contains(firstCLI, "repair patch passed isolated tests after 2 attempt(s) and was applied") {
		t.Fatalf("CLI did not complete two-attempt repair: %q", firstCLI)
	}
	if cliRequests != 2 {
		t.Fatalf("CLI model requests=%d, want 2", cliRequests)
	}
	dockerCalls := countLines(t, dockerLog)
	if dockerCalls != 1 {
		t.Fatalf("approved patch should invoke one --network=none verification container, calls=%d", dockerCalls)
	}
	firstCount := countLines(t, logPath)
	if firstCount != 2 {
		t.Fatalf("CLI should run failing initial tests and passing sandbox tests, got %d Go test commands", firstCount)
	}
	if output, err := run(root, "git", "diff", "--quiet", "HEAD"); err != nil {
		t.Fatalf("self-healed worktree still differs from baseline: %v %s", err, output)
	}
	secondCLI := func() string {
		t.Helper()
		cmd := exec.Command(binary, "verify")
		cmd.Dir = root
		cmd.Env = cliEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("astrix verify: %v\n%s", err, output)
		}
		return string(output)
	}
	secondOutput := secondCLI()
	if !strings.Contains(secondOutput, "cache hit: 100%; tests executed: 0") {
		t.Fatalf("unchanged CLI verification did not report cache hit: %q", secondOutput)
	}
	if secondCount := countLines(t, logPath); secondCount != firstCount {
		t.Fatalf("no-change CLI verify ran tests: before=%d after=%d", firstCount, secondCount)
	}
}

type patchSandbox struct {
	root  string
	calls int
}

func (s *patchSandbox) ApplyAndTest(ctx context.Context, diff string) error {
	s.calls++
	temp, err := os.MkdirTemp("", "astrix-e2e-sandbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = copyTree(s.root, temp); err != nil {
		return err
	}
	init := exec.CommandContext(ctx, "git", "init", "-q")
	init.Dir = temp
	if output, e := init.CombinedOutput(); e != nil {
		return fmt.Errorf("git init sandbox: %w: %s", e, output)
	}
	applyCmd := exec.CommandContext(ctx, "git", "apply", "-")
	applyCmd.Dir = temp
	applyCmd.Stdin = strings.NewReader(diff)
	if output, e := applyCmd.CombinedOutput(); e != nil {
		return fmt.Errorf("apply sandbox patch: %w: %s (patch=%q)", e, output, diff)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "./pkg/mathutil")
	cmd.Dir = temp
	if output, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("sandbox test failed: %w: %s", e, output)
	}
	return nil
}

func copyTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".astrix" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		if err = os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, data, info.Mode().Perm())
	})
}

// apply creates a local git repository before applying a patch, matching git apply behavior.
func apply(root, diff string) error {
	cmd := exec.Command("git", "apply", "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(diff)
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, data)
	}
	return nil
}

func patchPaths(diff string) ([]string, error) {
	if err := triage.ValidatePatch(diff, false); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		for _, prefix := range []string{"+++ b/", "--- a/"} {
			if strings.HasPrefix(line, prefix) {
				name := strings.TrimPrefix(line, prefix)
				if name != "/dev/null" && !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
	}
	return out, nil
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := run(root, "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}
func run(root, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return 0
	}
	return strings.Count(string(data), "\n")
}
