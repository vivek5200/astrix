package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTC_PROF_01_ManifestMatrix(t *testing.T) {
	fixtures := []struct{ name, files, ecosystem, image string }{
		{"go", "go.mod", "go", "golang:1.23-alpine"}, {"pnpm", "package.json,pnpm-lock.yaml", "node", "node:22-alpine"}, {"npm", "package.json,package-lock.json", "node", "node:22-alpine"}, {"python", "pyproject.toml,.venv", "python", "python:3.12-slim"}, {"rust", "Cargo.toml", "rust", "rust:1.82-slim"}, {"maven", "pom.xml", "maven", "eclipse-temurin:21-jdk"}, {"gradle", "build.gradle", "gradle", "eclipse-temurin:21-jdk"}, {"ruby", "Gemfile", "ruby", "ruby:3.3-slim"}, {"cpp", "CMakeLists.txt", "c-cpp", "gcc:14"},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range strings.Split(tc.files, ",") {
				p := filepath.Join(root, file)
				if strings.HasSuffix(file, ".venv") {
					if e := os.MkdirAll(p, 0o755); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.WriteFile(p, []byte("{}"), 0o600); e != nil {
						t.Fatal(e)
					}
				}
			}
			profile, err := DetectProjectProfile(root)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Ecosystem != tc.ecosystem || profile.Image != tc.image {
				t.Fatalf("profile=%+v", profile)
			}
		})
	}
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".astrix"), 0o755)
	dockerfile := filepath.Join(root, ".astrix", "Dockerfile")
	_ = os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o600)
	profile, err := DetectProjectProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Dockerfile != dockerfile || !strings.HasPrefix(profile.Image, "astrix-sandbox:") {
		t.Fatalf("custom profile=%+v", profile)
	}
}

func TestTC_PROF_03_ScopedPythonCommandAndFullFallback(t *testing.T) {
	p := ProjectProfile{Ecosystem: "python", TestCommand: []string{"pytest"}}
	want := []string{"pytest", "tests/test_a.py", "tests/test_b.py"}
	got := p.ScopedTestCommand([]string{"tests/test_b.py", "tests/test_a.py"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped=%v want %v", got, want)
	}
	if got := p.ScopedTestCommand(nil); !reflect.DeepEqual(got, []string{"pytest"}) {
		t.Fatalf("fallback=%v", got)
	}
	if got := p.ScopedTestCommand([]string{"../../etc/passwd"}); !reflect.DeepEqual(got, []string{"pytest"}) {
		t.Fatalf("unsafe scoped fallback=%v", got)
	}
}

func TestProfilesIncludeExistingOfflineCachesAndDependencies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	for _, dir := range []string{".npm", ".local/share/pnpm/store", ".cache/yarn", ".cache/pip", ".cache/uv", ".cargo/registry", ".cargo/git", ".m2/repository", ".gradle/caches"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fixture := func(name string, files map[string]string) ProjectProfile {
		t.Helper()
		root := filepath.Join(t.TempDir(), name)
		for file, contents := range files {
			path := filepath.Join(root, file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		profile, err := DetectProjectProfile(root)
		if err != nil {
			t.Fatal(err)
		}
		return profile
	}
	npm := fixture("npm", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`, "package-lock.json": "{}", "node_modules/local/index.js": "module.exports=1"})
	if !hasProfileMount(npm, "/root/.npm") || !hasProfileMount(npm, "/workspace/node_modules") || !strings.Contains(strings.Join(npm.TestCommand, " "), "runInBand") {
		t.Fatalf("npm offline profile=%+v", npm)
	}
	pnpm := fixture("pnpm", map[string]string{"package.json": "{}", "pnpm-lock.yaml": "lockfileVersion: '9.0'", "node_modules/local/index.js": "module.exports=1"})
	if !hasProfileMount(pnpm, "/root/.local/share/pnpm/store") || !hasProfileMount(pnpm, "/workspace/node_modules") {
		t.Fatalf("pnpm mounts=%+v", pnpm.Mounts)
	}
	py := fixture("python", map[string]string{"pyproject.toml": "[project]\nname='fixture'\n", ".venv/pyvenv.cfg": "home=/usr/bin\n"})
	for _, dest := range []string{"/root/.cache/pip", "/root/.cache/uv", "/workspace/.venv"} {
		if !hasProfileMount(py, dest) {
			t.Errorf("Python mount %s missing: %+v", dest, py.Mounts)
		}
	}
	rust := fixture("rust", map[string]string{"Cargo.toml": "[package]\nname='fixture'\nversion='0.1.0'\n"})
	for _, dest := range []string{"/root/.cargo/registry", "/root/.cargo/git"} {
		if !hasProfileMount(rust, dest) {
			t.Errorf("Rust mount %s missing", dest)
		}
	}
	maven := fixture("maven", map[string]string{"pom.xml": "<project/>"})
	if !hasProfileMount(maven, "/root/.m2/repository") {
		t.Errorf("Maven cache missing: %+v", maven.Mounts)
	}
	gradle := fixture("gradle", map[string]string{"build.gradle": "plugins {}"})
	if !hasProfileMount(gradle, "/root/.gradle/caches") {
		t.Errorf("Gradle cache missing: %+v", gradle.Mounts)
	}
}

func hasProfileMount(p ProjectProfile, dest string) bool {
	for _, m := range p.Mounts {
		if m.Destination == dest {
			return true
		}
	}
	return false
}
