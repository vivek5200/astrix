package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vivek5200/astrix/pkg/app"
	"github.com/vivek5200/astrix/pkg/triage"
)

func TestLiveDockerAndLLM_ClosedLoopSelfHealing(t *testing.T) {
	if os.Getenv("ASTRIX_RUN_LIVE_E2E") != "1" {
		t.Skip("live LLM/Docker end-to-end test is opt-in; set ASTRIX_RUN_LIVE_E2E=1 to run it")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate live E2E test source")
	}
	workspace := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	if err := triage.LoadDotEnv(filepath.Join(workspace, ".env")); err != nil {
		t.Fatalf("load workspace .env: %v", err)
	}
	llm := triage.ResolveLLMConfig(os.Getenv, "")
	if llm.APIKey == "" {
		t.Skip("live LLM test skipped: set ASTRIX_LLM_API_KEY, OPENROUTER_API_KEY, or OPENAI_API_KEY in the environment or workspace .env")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("live Docker test skipped: install the Docker CLI and start its daemon")
	}
	if output, err := exec.Command(docker, "info").CombinedOutput(); err != nil {
		t.Skipf("live Docker test skipped: Docker daemon is unavailable (%v): %s", err, strings.TrimSpace(string(output)))
	}

	root := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("Astrixfile.ax", `guardrail "security_boundaries" {
  deny when { diff.touches("internal/auth/**") }
}
stage "verify" { network = "none" }
heal "triage_loop" on failure(stage.verify) {
  model = "qwen/qwen-2.5-coder-32b-instruct"
  max_attempts = 3
  allow when { diff.modifies_tests("false") and true }
}
`)
	writeFixture("go.mod", "module astrix-live-e2e\n\ngo 1.22\n")
	writeFixture("pkg/calc/calc.go", "package calc\n\nfunc Multiply(a, b int) int { return a * b }\n")
	writeFixture("pkg/calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMultiply(t *testing.T) { if got := Multiply(6, 7); got != 42 { t.Fatalf(\"Multiply(6, 7) = %d, want 42\", got) } }\n")
	writeFixture("pkg/other/other.go", "package other\n\nfunc Value() int { return 9 }\n")
	writeFixture("pkg/other/other_test.go", "package other\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 9 { t.Fatal(\"unexpected value\") } }\n")
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=Astrix Live E2E", "-c", "user.email=live-e2e@astrix.local", "commit", "-qm", "working baseline")
	testPath := filepath.Join(root, "pkg/calc/calc_test.go")
	testBefore, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture("pkg/calc/calc.go", "package calc\n\nfunc Multiply(a, b int) int { return a + b }\n")
	diff := git(t, root, "diff", "--unified=0", "HEAD")
	impact, _, err := app.Impact(root, diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Tests) != 1 || impact.Tests[0] != "pkg/calc/calc_test.go::TestMultiply" {
		t.Fatalf("expected only calc test in blast radius, got %+v", impact)
	}
	t.Logf("[1/5] AST Blast Radius: impacted tests %v; pkg/other skipped", impact.Tests)
	failure, err := run(root, "go", "test", "./...")
	if err == nil {
		t.Fatal("deliberately broken calc package unexpectedly passed")
	}
	t.Logf("[2/5] Docker Sandbox Failure: initial test failed as expected (%s)", strings.TrimSpace(failure))

	cliDir, err := os.MkdirTemp("", "astrix-live-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cliDir)
	binary := filepath.Join(cliDir, "astrix")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.Command(goBinary, "build", "-o", binary, "../cmd/astrix")
	build.Dir = filepath.Join(workspace, "astrix", "e2e")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build astrix CLI: %v\n%s", err, output)
	}
	cmd := exec.Command(binary, "verify", "--fix")
	cmd.Dir = root
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live astrix verify --fix failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "repair patch passed isolated tests") {
		t.Fatalf("CLI did not report successful sandboxed repair: %s", output)
	}
	t.Log("[3/5] Live LLM Diff Received: CLI accepted a unified diff from the configured endpoint")
	t.Log("[4/5] OPA Policy Evaluation: the returned ActionIntent passed the embedded policy gate before sandbox application")
	repaired, err := os.ReadFile(filepath.Join(root, "pkg/calc/calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repaired), "return a + b") || !strings.Contains(string(repaired), "return a * b") {
		t.Fatalf("LLM patch did not restore Multiply: %s", repaired)
	}
	testAfter, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(testBefore) != string(testAfter) {
		t.Fatal("LLM repair modified calc_test.go")
	}
	if output, err := run(root, "go", "test", "./..."); err != nil {
		t.Fatalf("post-repair tests failed: %v\n%s", err, output)
	}
	t.Log("[5/5] Docker Sandbox Re-Verification PASS: repair applied, test file unchanged, go test ./... passed")
}
