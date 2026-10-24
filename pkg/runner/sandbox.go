package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DockerSandbox tests a patch in an ephemeral workspace copy inside a hardened,
// network-isolated Docker container.
type DockerSandbox struct {
	Root        string
	Image       string
	ModuleCache string
	GoCache     string
	GoRoot      string
	TestCommand []string
}

// ApplyAndTest validates and applies the patch in a disposable copy, then runs
// the configured command without network, Linux capabilities, or privilege gain.
func (s DockerSandbox) ApplyAndTest(ctx context.Context, diff string) error {
	if s.Root == "" {
		return fmt.Errorf("sandbox root is required")
	}
	image := s.Image
	if image == "" {
		image = "golang:1.27"
	}
	temp, err := os.MkdirTemp("", "astrix-sandbox-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = os.Chmod(temp, 0o755); err != nil {
		return fmt.Errorf("prepare sandbox workspace permissions: %w", err)
	}
	if err = copyProject(s.Root, temp); err != nil {
		return fmt.Errorf("copy sandbox workspace: %w", err)
	}
	patchPath := filepath.Join(temp, ".astrix-repair.patch")
	if err = os.WriteFile(patchPath, []byte(diff), 0o600); err != nil {
		return err
	}
	moduleCache, goCache, goRoot := s.ModuleCache, s.GoCache, s.GoRoot
	if moduleCache == "" || goCache == "" || goRoot == "" {
		hostModule, hostGo, hostRoot, goVersion, cacheErr := hostGoEnvironment(ctx)
		if cacheErr != nil {
			return cacheErr
		}
		if moduleCache == "" {
			moduleCache = hostModule
		}
		if goCache == "" {
			// Keep the sandbox cache separate from caches populated by a different
			// Go toolchain. Sharing those directly can make package metadata appear
			// incompatible even though both toolchains use the same source tree.
			goCache = filepath.Join(hostGo, "astrix-sandbox-"+goVersion)
			if err = os.MkdirAll(goCache, 0o755); err != nil {
				return fmt.Errorf("prepare isolated Go build cache: %w", err)
			}
			// The hardened container has all Linux capabilities dropped, including
			// DAC_OVERRIDE. Make this dedicated cache directory writable without
			// granting write access to the host's normal Go cache.
			if err = os.Chmod(goCache, 0o777); err != nil {
				return fmt.Errorf("set isolated Go build cache permissions: %w", err)
			}
		}
		if goRoot == "" {
			goRoot = hostRoot
		}
	}
	if err = applyPatchToCopy(temp, diff); err != nil {
		return fmt.Errorf("patch does not apply cleanly: %w", err)
	}
	command := s.TestCommand
	if len(command) == 0 {
		command = []string{"go", "test", "./..."}
	}
	return runDocker(ctx, temp, image, moduleCache, goCache, goRoot, command...)
}

func hostGoEnvironment(ctx context.Context) (moduleCache, goCache, goRoot, goVersion string, err error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE", "GOCACHE", "GOROOT", "GOVERSION")
	cmd.Dir = os.TempDir()
	cmd.Env = setEnv(os.Environ(), "GOTOOLCHAIN", "local")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", "", "", fmt.Errorf("query host Go environment: %w: %s", err, strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" || strings.TrimSpace(lines[2]) == "" || strings.TrimSpace(lines[3]) == "" {
		return "", "", "", "", fmt.Errorf("go env returned invalid cache/toolchain paths: %q", string(output))
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), strings.TrimSpace(lines[2]), strings.TrimSpace(lines[3]), nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func applyPatchToCopy(root, diff string) error {
	check := exec.Command("git", "apply", "--recount", "--check", "-")
	check.Dir = root
	check.Stdin = strings.NewReader(diff)
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("git apply --check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	apply := exec.Command("git", "apply", "--recount", "-")
	apply.Dir = root
	apply.Stdin = strings.NewReader(diff)
	if output, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runDocker(ctx context.Context, root, image, moduleCache, goCache, goRoot string, command ...string) error {
	args := []string{"run", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "-v", root + ":/workspace", "-w", "/workspace"}
	if moduleCache != "" {
		args = append(args, "-v", moduleCache+":/astrix-modcache:ro", "-e", "GOMODCACHE=/astrix-modcache", "-e", "GOPROXY=off", "-e", "GOSUMDB=off")
	}
	if goCache != "" {
		args = append(args, "-v", goCache+":/astrix-gocache", "-e", "GOCACHE=/astrix-gocache")
	}
	if goRoot != "" {
		args = append(args, "-v", goRoot+":/astrix-go:ro", "-e", "GOROOT=/astrix-go", "-e", "PATH=/astrix-go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	args = append(args, "-e", "GOTOOLCHAIN=local", "-e", "GOFLAGS=-buildvcs=false")
	args = append(args, image)
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(command, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func copyProject(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".astrix", "node_modules", "vendor":
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
		target := filepath.Join(dest, rel)
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
