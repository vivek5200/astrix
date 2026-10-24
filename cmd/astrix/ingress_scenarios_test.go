package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifySecretIngressUsesExitCodeTwoAndRedacts(t *testing.T) {
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("init", "-q")
	runGit("add", ".")
	runGit("-c", "user.name=Astrix Test", "-c", "user.email=test@astrix.local", "commit", "-qm", "baseline")
	const secret = "AKIA" + "1234567890ABCDEF"
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("key="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	if code := run([]string{"verify"}, stdout, stderr); code != 2 {
		t.Fatalf("verify exit code=%d; want 2", code)
	}
	if _, err := stderr.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), secret) || !strings.Contains(string(output), "[REDACTED]") {
		t.Fatalf("ingress output leaked or failed to redact secret: %s", output)
	}
}
