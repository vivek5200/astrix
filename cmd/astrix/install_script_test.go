package main

// Tests for the POSIX installer scripts (astrix/install.sh and, as a
// workspace smoke test, the sibling axlang/install.sh).
//
// This file is intentionally self-contained: every helper is prefixed with
// "installScript" and depends on no symbol from the rest of package main,
// so it stays independent of concurrent edits to main.go / main_test.go.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installScriptRun executes an installer shell script with the given
// arguments and returns its combined output and exit code.
func installScriptRun(t *testing.T, script string, args ...string) (string, int) {
	t.Helper()

	cmd := exec.Command("sh", append([]string{script}, args...)...)
	// Inherit the environment but pin the locale so the markers we grep
	// for in the output are deterministic.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running %s %v: unexpected error: %v\noutput:\n%s", script, args, err, out)
		}
		return string(out), exitErr.ExitCode()
	}
	return string(out), 0
}

// installScriptPosixSkip skips installer tests on hosts the installers do
// not support (and where a POSIX sh may not even exist).
func installScriptPosixSkip(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("installer supports linux/darwin only; host GOOS is %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("installer supports amd64/arm64 only; host GOARCH is %s", runtime.GOARCH)
	}
}

// TestInstallScriptHelp checks that astrix/install.sh --help exits 0 and
// documents its flags.
func TestInstallScriptHelp(t *testing.T) {
	t.Parallel()

	// Test CWD is astrix/cmd/astrix, so ../../install.sh is astrix/install.sh.
	script := filepath.Join("..", "..", "install.sh")
	out, code := installScriptRun(t, script, "--help")
	if code != 0 {
		t.Fatalf("install.sh --help: exit code %d, want 0\noutput:\n%s", code, out)
	}
	for _, marker := range []string{"--bin-dir", "--dry-run", "--astrix-version", "--version"} {
		if !strings.Contains(out, marker) {
			t.Errorf("install.sh --help output missing %q\noutput:\n%s", marker, out)
		}
	}
}

// TestInstallScriptDryRun checks that astrix/install.sh --dry-run resolves
// the plan locally (OS, arch, bin dir, asset URLs) without any network
// access or side effects.
func TestInstallScriptDryRun(t *testing.T) {
	t.Parallel()
	installScriptPosixSkip(t)

	binDir := filepath.Join(t.TempDir(), "not-created", "bin")
	script := filepath.Join("..", "..", "install.sh")
	out, code := installScriptRun(t, script, "--dry-run", "--bin-dir", binDir)
	if code != 0 {
		t.Fatalf("install.sh --dry-run: exit code %d, want 0\noutput:\n%s", code, out)
	}
	// The plan must mention the detected OS and arch (matching the host's
	// GOOS/GOARCH, which the installer detects via uname), the requested
	// bin dir, and the release asset it would download.
	for _, marker := range []string{"dry-run", runtime.GOOS, runtime.GOARCH, binDir, ".tar.gz"} {
		if !strings.Contains(out, marker) {
			t.Errorf("install.sh --dry-run output missing %q\noutput:\n%s", marker, out)
		}
	}
	if _, err := os.Stat(binDir); !os.IsNotExist(err) {
		t.Errorf("dry-run created or unexpectedly accessed install directory %s (stat err: %v)", binDir, err)
	}
}

// TestInstallScriptDryRunNeverDownloads puts fake downloaders first on PATH.
// The marker must remain absent, proving dry-run is offline as well as
// filesystem-read-only with respect to the install target.
func TestInstallScriptDryRunNeverDownloads(t *testing.T) {
	t.Parallel()
	installScriptPosixSkip(t)

	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "downloader-called")
	for _, name := range []string{"curl", "wget"} {
		body := "#!/bin/sh\nprintf called > \"" + marker + "\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binDir := filepath.Join(root, "install-target")
	script := filepath.Join("..", "..", "install.sh")
	cmd := exec.Command("sh", script, "--dry-run", "--bin-dir", binDir)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+"/usr/bin:/bin", "HOME="+root, "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run failed: %v\noutput:\n%s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("dry-run invoked curl/wget (marker stat err: %v)", err)
	}
	if _, err := os.Stat(binDir); !os.IsNotExist(err) {
		t.Errorf("dry-run created install target %s (stat err: %v)", binDir, err)
	}
}

func TestInstallScriptRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	script := filepath.Join("..", "..", "install.sh")
	for _, args := range [][]string{{"--unknown"}, {"--bin-dir"}, {"--astrix-version"}} {
		out, code := installScriptRun(t, script, args...)
		if code == 0 {
			t.Errorf("install.sh %v unexpectedly succeeded\noutput:\n%s", args, out)
		}
	}
}

// TestInstallScriptAxlangSmoke smoke-tests the sibling axlang/install.sh
// (help + dry-run). It is skipped when astrix is checked out standalone
// without the axlang workspace sibling.
func TestInstallScriptAxlangSmoke(t *testing.T) {
	t.Parallel()
	installScriptPosixSkip(t)

	script := filepath.Join("..", "..", "..", "axlang", "install.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("axlang installer not found at %s (standalone checkout?): %v", script, err)
	}

	// --help
	out, code := installScriptRun(t, script, "--help")
	if code != 0 {
		t.Fatalf("axlang install.sh --help: exit code %d, want 0\noutput:\n%s", code, out)
	}
	for _, marker := range []string{"--bin-dir", "--dry-run", "--axlang-version", "--version"} {
		if !strings.Contains(out, marker) {
			t.Errorf("axlang install.sh --help output missing %q\noutput:\n%s", marker, out)
		}
	}

	// --dry-run
	binDir := t.TempDir()
	out, code = installScriptRun(t, script, "--dry-run", "--bin-dir", binDir)
	if code != 0 {
		t.Fatalf("axlang install.sh --dry-run: exit code %d, want 0\noutput:\n%s", code, out)
	}
	for _, marker := range []string{"dry-run", runtime.GOOS, runtime.GOARCH, binDir, ".tar.gz"} {
		if !strings.Contains(out, marker) {
			t.Errorf("axlang install.sh --dry-run output missing %q\noutput:\n%s", marker, out)
		}
	}
}
