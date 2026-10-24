package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ProjectProfile contains a detected offline sandbox configuration.
type ProjectProfile struct {
	Image             string         `json:"image"`
	Dockerfile        string         `json:"dockerfile,omitempty"`
	TestCommand       []string       `json:"test_command"`
	Mounts            []ProfileMount `json:"mounts,omitempty"`
	Ecosystem         string         `json:"ecosystem"`
	CommandOverridden bool           `json:"command_overridden,omitempty"`
}

// ProfileMount describes a bind mount relative to the copied workspace or host.
type ProfileMount struct{ Source, Destination, Mode string }

// DetectProjectProfile selects a conventional sandbox from repository manifests.
func DetectProjectProfile(repoDir string) (ProjectProfile, error) {
	if repoDir == "" {
		return ProjectProfile{}, fmt.Errorf("repository directory is required")
	}
	root, err := filepath.Abs(repoDir)
	if err != nil {
		return ProjectProfile{}, err
	}
	if _, err = os.Stat(root); err != nil {
		return ProjectProfile{}, err
	}
	custom := filepath.Join(root, ".astrix", "Dockerfile")
	if _, err := os.Stat(custom); err != nil {
		custom = filepath.Join(root, "Dockerfile")
	}
	if data, readErr := os.ReadFile(custom); readErr == nil {
		hash := sha256.Sum256(data)
		return ProjectProfile{Image: "astrix-sandbox:" + hex.EncodeToString(hash[:]), Dockerfile: custom, TestCommand: envOr([]string{"go", "test", "./..."}), Ecosystem: "custom"}, nil
	}
	home, _ := os.UserHomeDir()
	exists := func(name string) bool { _, e := os.Stat(filepath.Join(root, name)); return e == nil }
	profile := ProjectProfile{Ecosystem: "", TestCommand: []string{"sh", "-lc", "true"}}
	switch {
	case exists("go.mod"):
		profile = ProjectProfile{Ecosystem: "go", Image: "golang:1.23-alpine", TestCommand: []string{"go", "test", "./..."}}
		if mod, cache, e := hostGoEnvironmentForProfile(root); e == nil {
			if _, e := os.Stat(mod); e == nil {
				profile.Mounts = append(profile.Mounts, ProfileMount{mod, "/astrix-modcache", "ro"})
			}
			if _, e := os.Stat(cache); e == nil {
				profile.Mounts = append(profile.Mounts, ProfileMount{cache, "/astrix-gocache", "rw"})
			}
		}
	case exists("package.json"):
		profile = ProjectProfile{Ecosystem: "node", Image: "node:22-alpine", TestCommand: []string{"npm", "test", "--", "--run"}}
		packageData, _ := os.ReadFile(filepath.Join(root, "package.json"))
		if strings.Contains(strings.ToLower(string(packageData)), "jest") {
			profile.TestCommand = []string{"npm", "test", "--", "--runInBand"}
		}
		if exists("pnpm-lock.yaml") {
			profile.TestCommand = []string{"pnpm", "test", "--run"}
			addHostMount(&profile, home, ".local/share/pnpm/store", "/root/.local/share/pnpm/store")
		} else if exists("yarn.lock") {
			profile.TestCommand = []string{"yarn", "test", "--runInBand"}
			addHostMount(&profile, home, ".cache/yarn", "/root/.cache/yarn")
		} else {
			addHostMount(&profile, home, ".npm", "/root/.npm")
		}
		if exists("node_modules") {
			profile.Mounts = append(profile.Mounts, ProfileMount{filepath.Join(root, "node_modules"), "/workspace/node_modules", "ro"})
		}
	case exists("pyproject.toml") || exists("requirements.txt"):
		profile = ProjectProfile{Ecosystem: "python", Image: "python:3.12-slim", TestCommand: []string{"pytest"}}
		addHostMount(&profile, home, ".cache/pip", "/root/.cache/pip")
		addHostMount(&profile, home, ".cache/uv", "/root/.cache/uv")
		for _, v := range []string{".venv", "venv"} {
			if exists(v) {
				profile.Mounts = append(profile.Mounts, ProfileMount{filepath.Join(root, v), "/workspace/" + v, "ro"})
			}
		}
	case exists("Cargo.toml"):
		profile = ProjectProfile{Ecosystem: "rust", Image: "rust:1.82-slim", TestCommand: []string{"cargo", "test", "--offline"}}
		addHostMount(&profile, home, ".cargo/registry", "/root/.cargo/registry")
		addHostMount(&profile, home, ".cargo/git", "/root/.cargo/git")
	case exists("pom.xml"):
		profile = ProjectProfile{Ecosystem: "maven", Image: "eclipse-temurin:21-jdk", TestCommand: []string{"mvn", "test", "-o"}}
		addHostMount(&profile, home, ".m2/repository", "/root/.m2/repository")
	case exists("build.gradle") || exists("build.gradle.kts"):
		profile = ProjectProfile{Ecosystem: "gradle", Image: "eclipse-temurin:21-jdk", TestCommand: []string{"gradle", "test", "--offline"}}
		addHostMount(&profile, home, ".gradle/caches", "/root/.gradle/caches")
	case exists("Gemfile"):
		profile = ProjectProfile{Ecosystem: "ruby", Image: "ruby:3.3-slim", TestCommand: []string{"bundle", "exec", "rspec"}}
	case exists("CMakeLists.txt") || exists("Makefile"):
		profile = ProjectProfile{Ecosystem: "c-cpp", Image: "gcc:14", TestCommand: []string{"make", "test"}}
	default:
		return ProjectProfile{}, fmt.Errorf("no supported project manifest found in %s", root)
	}
	if command := strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")); command != "" {
		profile.TestCommand = strings.Fields(command)
	}
	return profile, nil
}

func addHostMount(p *ProjectProfile, home, rel, dest string) {
	path := filepath.Join(home, rel)
	if home != "" {
		if _, e := os.Stat(path); e == nil {
			p.Mounts = append(p.Mounts, ProfileMount{path, dest, "ro"})
		}
	}
}
func hostGoEnvironmentForProfile(root string) (string, string, error) {
	cmd := exec.Command("go", "env", "GOMODCACHE", "GOCACHE", "GOROOT", "GOVERSION")
	cmd.Dir = os.TempDir()
	cmd.Env = setEnv(os.Environ(), "GOTOOLCHAIN", "local")
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 4 {
		return "", "", fmt.Errorf("invalid go env output")
	}
	return lines[0], lines[1], nil
}

// ScopedTestCommand returns only mapped tests when available, or the full suite.
func (p ProjectProfile) ScopedTestCommand(tests []string) []string {
	if p.CommandOverridden {
		return append([]string(nil), p.TestCommand...)
	}
	if len(tests) == 0 {
		return append([]string(nil), p.TestCommand...)
	}
	clean := make([]string, 0, len(tests))
	for _, t := range tests {
		if filepath.IsAbs(t) || strings.Contains(t, "..") {
			continue
		}
		clean = append(clean, filepath.ToSlash(t))
	}
	sort.Strings(clean)
	if len(clean) == 0 {
		return append([]string(nil), p.TestCommand...)
	}
	switch p.Ecosystem {
	case "go":
		packages := map[string]bool{}
		for _, test := range clean {
			dir := filepath.ToSlash(filepath.Dir(test))
			if dir == "." {
				dir = "."
			}
			packages["./"+strings.TrimPrefix(dir, "./")] = true
		}
		out := []string{"go", "test"}
		for pkg := range packages {
			out = append(out, pkg)
		}
		sort.Strings(out[2:])
		return out
	case "python":
		return append([]string{"pytest"}, clean...)
	case "rust":
		return append([]string{"cargo", "test", "--offline"}, clean...)
	case "node":
		return append(append([]string(nil), p.TestCommand...), clean...)
	case "maven":
		names := make([]string, 0, len(clean))
		for _, t := range clean {
			n := strings.TrimSuffix(filepath.Base(t), filepath.Ext(t))
			names = append(names, n)
		}
		return []string{"mvn", "test", "-o", "-Dtest=" + strings.Join(names, ",")}
	case "gradle":
		out := []string{"gradle", "test", "--offline"}
		for _, t := range clean {
			out = append(out, "--tests", strings.TrimSuffix(filepath.Base(t), filepath.Ext(t)))
		}
		return out
	default:
		return append(append([]string(nil), p.TestCommand...), clean...)
	}
}

func envOr(fallback []string) []string {
	if s := strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")); s != "" {
		return strings.Fields(s)
	}
	return fallback
}
