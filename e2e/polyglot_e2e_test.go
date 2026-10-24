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

func TestPolyglotClosedLoop_MultiLanguageBlastRadiusAndHeal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the Python closed-loop fixture")
	}
	if out, err := exec.Command("python3", "-m", "pytest", "--version").CombinedOutput(); err != nil {
		t.Skipf("pytest is required: %s", out)
	}
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if e := os.MkdirAll(filepath.Dir(p), 0o755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(content), 0o644); e != nil {
			t.Fatal(e)
		}
	}
	write("Astrixfile.ax", `heal "triage" on failure(stage.verify) { max_attempts = 3 allow when { true } }
stage "verify" { scope = ast.blast_radius }
`)
	write("calc.py", "def divide(a, b):\n    return a * b\n")
	write("tests/test_calc.py", "from calc import divide\ndef test_divide():\n    assert divide(8, 2) == 4\n")
	write("math.ts", "export function add(a: number, b: number) { return a + b; }\n")
	write("math.test.ts", "import { add } from './math';\nfunction testAdd() { return add(1, 2); }\n")
	write("src/lib.rs", "pub fn add(a: i32, b: i32) -> i32 { a+b }\n#[test]\nfn test_add() { assert_eq!(add(1,2),3); }\n")
	files := map[string][]byte{}
	for _, name := range []string{"calc.py", "tests/test_calc.py", "math.ts", "math.test.ts", "src/lib.rs"} {
		data, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		files[name] = data
	}
	graph, e := astgraph.ParseFilesWithAdapters(files, astgraph.DefaultTreeSitterAdapters())
	if e != nil {
		t.Fatal(e)
	}
	impact := astgraph.BlastRadius(graph, []ingress.FileChange{{Path: "calc.py", Ranges: []ingress.LineRange{{Start: 1, End: 2}}}})
	if len(impact.Tests) != 1 || impact.Tests[0] != "tests/test_calc.py::test_divide" {
		t.Fatalf("polyglot isolation tests=%v impact=%+v", impact.Tests, impact)
	}
	for _, id := range impact.ImpactedSymbols {
		if strings.Contains(id, "math.test") || strings.Contains(id, "lib.rs") {
			t.Fatalf("unrelated language entered impact: %+v", impact)
		}
	}
	initialTest := exec.Command("python3", "-m", "pytest", "-q", "tests/test_calc.py")
	initialTest.Dir = root
	if output, err := initialTest.CombinedOutput(); err == nil {
		t.Fatalf("buggy Python fixture unexpectedly passed: %s", output)
	}
	patch := "diff --git a/calc.py b/calc.py\n--- a/calc.py\n+++ b/calc.py\n@@ -1,2 +1,2 @@\n def divide(a, b):\n-    return a * b\n+    return a / b\n"
	model := staticPatchModel{patch: patch}
	sandbox := pythonPatchSandbox{root: root}
	gate, e := app.PolicyGate(filepath.Join(root, "Astrixfile.ax"))
	if e != nil {
		t.Fatal(e)
	}
	patcher := triage.Patcher{Model: model, Sandbox: &sandbox, MaxAttempts: 3, Policy: func(ctx context.Context, diff string) error {
		paths, err := patchPaths(diff)
		if err != nil {
			return err
		}
		decision, err := gate.Evaluate(ctx, policy.ActionIntent{Actor: policy.Actor{Type: "autonomous_llm"}, Target: policy.Target{Env: "development"}, ModifiedFiles: paths, Diff: policy.Diff{ModifiesTests: "false"}})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return fmt.Errorf("policy denied: %s", decision.Reason)
		}
		return nil
	}}
	result, e := patcher.Repair(context.Background(), triage.FailureContext{Impact: impact, SanitizedLog: "assert divide(8, 2) == 4 failed"})
	if e != nil {
		t.Fatal(e)
	}
	if result.Attempts < 1 || result.Attempts > 3 {
		t.Fatalf("attempts=%d, want bounded successful repair within 1..3", result.Attempts)
	}
	if e = apply(root, result.Patch); e != nil {
		t.Fatal(e)
	}
	afterTest := exec.Command("python3", "-m", "pytest", "-q", "tests/test_calc.py")
	afterTest.Dir = root
	if output, err := afterTest.CombinedOutput(); err != nil {
		t.Fatalf("pytest after repair: %v\n%s", err, output)
	}
	for _, bad := range []string{
		"diff --git a/src/lib.rs b/src/lib.rs\n--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -2,2 +2,2 @@\n #[test]\n-fn test_add() { assert_eq!(add(1,2),3); }\n+fn test_add() { assert_eq!(add(1,2),4); }\n",
		"diff --git a/math.test.ts b/math.test.ts\n--- a/math.test.ts\n+++ b/math.test.ts\n@@ -1,2 +1,2 @@\n import { add } from './math';\n-function testAdd() { return add(1, 2); }\n+function testAdd() { return add(1, 3); }\n",
	} {
		if err := triage.ValidatePatch(bad, false); err == nil {
			t.Fatal("test tampering patch was allowed")
		}
	}
	fixed, err := astgraph.LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	key := cache.SymbolSubgraphHash(fixed, impact.Tests)
	store := cache.Cache{Root: filepath.Join(root, ".astrix", "cache")}
	if err = store.Put(key, cache.TestOutcome{Passed: true}); err != nil {
		t.Fatal(err)
	}
	got, hit, err := store.Get(key)
	if err != nil || !hit || !got.Passed {
		t.Fatalf("repeat verification cache: hit=%v result=%+v err=%v", hit, got, err)
	}
}

func TestTC_E2E_02_PythonRepairThroughVerifyCLIAndCache(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	if _, err := exec.Command("python3", "-m", "pytest", "--version").CombinedOutput(); err != nil {
		t.Skip("pytest required")
	}
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("Astrixfile.ax", `stage "verify" { scope = ast.blast_radius }
heal "triage" on failure(stage.verify) { max_attempts = 3 allow when { true } }
`)
	write("calc.py", "def divide(a, b):\n    return a / b\n")
	write("tests/test_calc.py", "from calc import divide\ndef test_divide():\n    assert divide(8, 2) == 4\n")
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline")
	write("calc.py", "def divide(a, b):\n    return a * b\n")
	if out, err := exec.Command("python3", "-m", "pytest", "-q", "tests/test_calc.py").CombinedOutput(); err == nil {
		t.Fatalf("broken fixture passed: %s", out)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "```python\ndef divide(a, b):\n    return a / b\n```"}}}})
	}))
	defer server.Close()
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "astrix")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../cmd/astrix")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	fakebin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(fakebin, 0755); err != nil {
		t.Fatal(err)
	}
	docker := `#!/bin/sh
[ "$1" = run ] || exit 64
shift
root=
while [ "$#" -gt 0 ]; do
 case "$1" in
  --rm|--network=none) shift ;;
  --user) shift 2 ;;
  -v) case "$2" in *:/workspace) root=${2%:/workspace} ;; esac; shift 2 ;;
  -w|-e) shift 2 ;;
  --*) shift ;;
  *) shift; break ;;
 esac
done
[ -n "$root" ] || exit 65
cd "$root" || exit 66
exec "$@"
`
	if err := os.WriteFile(filepath.Join(fakebin, "docker"), []byte(docker), 0755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+fakebin+string(os.PathListSeparator)+os.Getenv("PATH"), "ASTRIX_LLM_ENDPOINT="+server.URL+"/v1/chat/completions", "ASTRIX_LLM_MODEL=fixture", "ASTRIX_TEST_COMMAND=[\"python3\",\"-m\",\"pytest\",\"-q\",\"tests/test_calc.py\"]")
	cmd := exec.Command(binary, "verify", "--fix")
	cmd.Dir = root
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("astrix verify --fix: %v\n%s", err, output)
	} else if !strings.Contains(string(output), "repair patch passed isolated tests") {
		t.Fatalf("repair output: %s", output)
	}
	if requests != 1 {
		t.Fatalf("mock LLM requests=%d, want 1", requests)
	}
	if got, err := os.ReadFile(filepath.Join(root, "calc.py")); err != nil || !strings.Contains(string(got), "return a / b") {
		t.Fatalf("repair not applied: %s, %v", got, err)
	}
	cmd = exec.Command(binary, "verify")
	cmd.Dir = root
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second verify: %v\n%s", err, output)
	} else if !strings.Contains(string(output), "cache hit: 100%; tests executed: 0") {
		t.Fatalf("second verify was not a full cache hit: %s", output)
	}
}

func TestRustClosedLoop_RepairsImplementationAndProtectsInlineTests(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is required for Rust closed-loop test")
	}
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Cargo.toml", "[package]\nname = \"astrix_fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	write("src/lib.rs", "pub fn add(a: i32, b: i32) -> i32 { a-b }\n#[cfg(test)]\nmod tests { use super::*; #[test] fn test_add() { assert_eq!(add(1,2),3); } }\n")
	initialTest := exec.Command("cargo", "test", "--offline")
	initialTest.Dir = root
	if out, err := initialTest.CombinedOutput(); err == nil {
		t.Fatalf("broken Rust fixture unexpectedly passed: %s", out)
	}
	patch := "diff --git a/src/lib.rs b/src/lib.rs\n--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -1,3 +1,3 @@\n-pub fn add(a: i32, b: i32) -> i32 { a-b }\n+pub fn add(a: i32, b: i32) -> i32 { a+b }\n #[cfg(test)]\n mod tests { use super::*; #[test] fn test_add() { assert_eq!(add(1,2),3); } }\n"
	model := staticPatchModel{patch: patch}
	sandbox := cargoPatchSandbox{root: root}
	repaired, err := (&triage.Patcher{Model: model, Sandbox: &sandbox, MaxAttempts: 3}).Repair(context.Background(), triage.FailureContext{SanitizedLog: "assertion failed: add(1, 2) == 3"})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Attempts > 3 {
		t.Fatalf("attempt bound violated: %d", repaired.Attempts)
	}
	if err = apply(root, repaired.Patch); err != nil {
		t.Fatal(err)
	}
	afterTest := exec.Command("cargo", "test", "--offline")
	afterTest.Dir = root
	if out, err := afterTest.CombinedOutput(); err != nil {
		t.Fatalf("Rust tests after repair: %v\n%s", err, out)
	}
	bad := "diff --git a/src/lib.rs b/src/lib.rs\n--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -3 +3 @@\n-#[test] fn test_add() { assert_eq!(add(1,2),3); }\n+#[test] fn test_add() { assert_eq!(add(1,2),4); }\n"
	if err := triage.ValidatePatch(bad, false); err == nil {
		t.Fatal("patch changing inline Rust assertion was allowed")
	}
	if err := triage.ValidatePatch("--- a/math.test.ts\n+++ b/math.test.ts\n@@ -1 +1 @@\n-import { add } from './math.ts';\n+import { add } from './math.ts';\n", false); err == nil {
		t.Fatal("TypeScript test-file patch was allowed")
	}
	if _, err := exec.LookPath("node"); err == nil {
		runTypeScriptRepair(t)
	}
}

func runTypeScriptRepair(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("math.ts", "export function add(a, b) { return a - b; }\n")
	write("math.test.ts", "import { strict as assert } from 'node:assert';\nimport { add } from './math.ts';\nassert.equal(add(1, 2), 3);\n")
	initial := exec.Command("node", "--experimental-strip-types", "math.test.ts")
	initial.Dir = root
	if out, err := initial.CombinedOutput(); err == nil {
		t.Fatalf("buggy TypeScript fixture passed: %s", out)
	}
	patch := "--- a/math.ts\n+++ b/math.ts\n@@ -1 +1 @@\n-export function add(a, b) { return a - b; }\n+export function add(a, b) { return a + b; }\n"
	sandbox := nodePatchSandbox{root: root}
	repaired, err := (&triage.Patcher{Model: staticPatchModel{patch: patch}, Sandbox: &sandbox, MaxAttempts: 3}).Repair(context.Background(), triage.FailureContext{SanitizedLog: "assert.equal(add(1, 2), 3)"})
	if err != nil {
		t.Fatal(err)
	}
	if err = apply(root, repaired.Patch); err != nil {
		t.Fatal(err)
	}
	after := exec.Command("node", "--experimental-strip-types", "math.test.ts")
	after.Dir = root
	if out, err := after.CombinedOutput(); err != nil {
		t.Fatalf("TypeScript test after repair: %v\n%s", err, out)
	}
}

type nodePatchSandbox struct{ root string }

func (s *nodePatchSandbox) ApplyAndTest(ctx context.Context, diff string) error {
	tmp, err := os.MkdirTemp("", "astrix-typescript-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = copyTree(s.root, tmp); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "-")
	cmd.Dir = tmp
	cmd.Stdin = strings.NewReader(diff)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("apply TypeScript patch: %w: %s", e, out)
	}
	test := exec.CommandContext(ctx, "node", "--experimental-strip-types", "math.test.ts")
	test.Dir = tmp
	if out, e := test.CombinedOutput(); e != nil {
		return fmt.Errorf("Node test failed: %w: %s", e, out)
	}
	return nil
}

type staticPatchModel struct{ patch string }

func (m staticPatchModel) ProposePatch(context.Context, string) (string, error) { return m.patch, nil }

type pythonPatchSandbox struct{ root string }

func (s *pythonPatchSandbox) ApplyAndTest(ctx context.Context, diff string) error {
	tmp, err := os.MkdirTemp("", "astrix-polyglot-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = copyTree(s.root, tmp); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "-")
	cmd.Dir = tmp
	cmd.Stdin = strings.NewReader(diff)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("apply patch: %w: %s", e, out)
	}
	test := exec.CommandContext(ctx, "python3", "-m", "pytest", "-q", "tests/test_calc.py")
	test.Dir = tmp
	if out, e := test.CombinedOutput(); e != nil {
		return fmt.Errorf("pytest failed: %w: %s", e, out)
	}
	return nil
}

type cargoPatchSandbox struct{ root string }

func (s *cargoPatchSandbox) ApplyAndTest(ctx context.Context, diff string) error {
	tmp, err := os.MkdirTemp("", "astrix-rust-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = copyTree(s.root, tmp); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "-")
	cmd.Dir = tmp
	cmd.Stdin = strings.NewReader(diff)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("apply Rust patch: %w: %s", e, out)
	}
	test := exec.CommandContext(ctx, "cargo", "test", "--offline")
	test.Dir = tmp
	if out, e := test.CombinedOutput(); e != nil {
		return fmt.Errorf("cargo test failed: %w: %s", e, out)
	}
	return nil
}
