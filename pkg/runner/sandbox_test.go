package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommandUsesGo(t *testing.T) {
	for _, test := range []struct {
		command []string
		want    bool
	}{
		{[]string{"go", "test", "./..."}, true},
		{[]string{"./go", "test"}, true},
		{[]string{"make", "test"}, false},
		{[]string{"npm", "test"}, false},
	} {
		if got := commandUsesGo(test.command); got != test.want {
			t.Errorf("commandUsesGo(%v)=%v, want %v", test.command, got, test.want)
		}
	}
}

func TestDependencyMountsFindsOfflinePackageTrees(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"node_modules/pkg/index.js", "services/api/vendor/lib.go", ".git/objects/ignored"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mounts, err := dependencyMounts(root)
	if err != nil {
		t.Fatal(err)
	}
	var destinations []string
	for _, mount := range mounts {
		destinations = append(destinations, mount.destination)
	}
	want := []string{"node_modules", "services/api/vendor"}
	if !reflect.DeepEqual(destinations, want) {
		t.Fatalf("dependency mounts=%v, want %v", destinations, want)
	}
}

func TestRealDockerSandbox_NetworkIsolation(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed; install Docker to run the container sandbox integration test")
	}
	if output, infoErr := exec.Command(docker, "info").CombinedOutput(); infoErr != nil {
		t.Skipf("Docker daemon is unavailable (%v); start Docker and rerun this test: %s", infoErr, output)
	}
	root := t.TempDir()
	writeSandboxFixture(t, root, "go.mod", "module sandbox.local/astrix\n\ngo 1.23\n")
	writeSandboxFixture(t, root, "pkg/calc/calc.go", "package calc\n\nfunc Value() int { return 1 }\n")
	writeSandboxFixture(t, root, "pkg/calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 2 { t.Fatalf(\"Value() = %d, want 2\", Value()) } }\n")
	writeSandboxFixture(t, root, "pkg/network/network_test.go", `package network

import (
	"net"
	"testing"
	"time"
)

func TestOutboundConnectionIsBlocked(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "1.1.1.1:53", 2*time.Second)
	if err == nil { _ = conn.Close(); t.Fatal("outbound connection succeeded in the --network=none sandbox") }
}
`)
	patch := "diff --git a/pkg/calc/calc.go b/pkg/calc/calc.go\n--- a/pkg/calc/calc.go\n+++ b/pkg/calc/calc.go\n@@ -3 +3 @@\n-func Value() int { return 1 }\n+func Value() int { return 2 }\n"
	image := os.Getenv("ASTRIX_SANDBOX_IMAGE")
	if image == "" {
		image = "golang:1.27"
		if err := exec.Command(docker, "image", "inspect", image).Run(); err != nil {
			// Prefer an already cached general-purpose Linux image when registry
			// access is unavailable; the host Go toolchain is mounted by the sandbox.
			fallback := "nvidia/cuda:12.6.0-base-ubuntu24.04"
			if exec.Command(docker, "image", "inspect", fallback).Run() == nil {
				image = fallback
			}
		}
	}
	t.Logf("sandbox image=%s", image)
	sandbox := DockerSandbox{Root: root, Image: image, GoRoot: os.Getenv("ASTRIX_GO_ROOT"), TestCommand: []string{"sh", "-c", "CGO_ENABLED=0 go test ./..."}}
	if err := sandbox.ApplyAndTest(context.Background(), patch); err != nil {
		t.Fatalf("real Docker sandbox failed: %v", err)
	}
}

func TestRealDockerSandbox_NonGoProjectAndOfflineDependencies(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed")
	}
	if output, infoErr := exec.Command(docker, "info").CombinedOutput(); infoErr != nil {
		t.Skipf("Docker daemon is unavailable (%v): %s", infoErr, output)
	}
	image := "nvidia/cuda:12.6.0-base-ubuntu24.04"
	if err := exec.Command(docker, "image", "inspect", image).Run(); err != nil {
		t.Skipf("non-Go sandbox image %s is not cached", image)
	}
	root := t.TempDir()
	writeSandboxFixture(t, root, "package.json", `{"scripts":{"test":"node test.js"}}`)
	writeSandboxFixture(t, root, "app.js", "function value() { return 1; }\n")
	writeSandboxFixture(t, root, "node_modules/local-fixture/index.js", "module.exports = true;\n")
	patch := "diff --git a/app.js b/app.js\n--- a/app.js\n+++ b/app.js\n@@ -1 +1 @@\n-function value() { return 1; }\n+function value() { return 2; }\n"
	sandbox := DockerSandbox{Root: root, Image: image, TestCommand: []string{"sh", "-c", "grep -q 'return 2' app.js && test -f node_modules/local-fixture/index.js"}}
	if err := sandbox.ApplyAndTest(context.Background(), patch); err != nil {
		t.Fatalf("non-Go offline sandbox failed: %v", err)
	}
}

func writeSandboxFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
