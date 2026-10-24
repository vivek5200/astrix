package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTC_PROF_02_ProfilePrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ax := filepath.Join(root, "Astrixfile.ax")
	source := `stage "verify" {
 image = "custom:configured"
 command = "pytest tests/test_a.py"
 mounts = [".venv:/workspace/.venv:ro"]
}
`
	if err := os.WriteFile(ax, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTRIX_SANDBOX_IMAGE", "")
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	p, err := ResolveProjectProfile(root, ax)
	if err != nil {
		t.Fatal(err)
	}
	if p.Image != "custom:configured" || !reflect.DeepEqual(p.TestCommand, []string{"pytest", "tests/test_a.py"}) || len(p.Mounts) != 1 {
		t.Fatalf(".ax override profile=%+v", p)
	}
	t.Setenv("ASTRIX_SANDBOX_IMAGE", "cli:latest")
	t.Setenv("ASTRIX_TEST_COMMAND", "pytest tests/test_b.py")
	p, err = ResolveProjectProfile(root, ax)
	if err != nil {
		t.Fatal(err)
	}
	if p.Image != "cli:latest" || !reflect.DeepEqual(p.TestCommand, []string{"pytest", "tests/test_b.py"}) {
		t.Fatalf("environment override profile=%+v", p)
	}
}
