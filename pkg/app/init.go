package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vivek5200/astrix/pkg/runner"
	"github.com/vivek5200/axlang/formatter"
	"github.com/vivek5200/axlang/parser"
	"github.com/vivek5200/axlang/sema"
)

// InitProject generates a starter Astrixfile.ax in dir based on the languages
// and sandbox profile detected for the repository. An existing Astrixfile.ax is
// never overwritten unless force is set. The generated policy is validated with
// the real AxLang parser and semantic checker, then written in canonical
// formatter output, and a short confirmation is printed to stdout.
func InitProject(dir string, force bool, stdout io.Writer) error {
	target, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(target); statErr != nil {
		return statErr
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", target)
	}
	path := filepath.Join(target, "Astrixfile.ax")
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
	}
	languages := DetectLanguages(target)
	profile, profileErr := runner.DetectProjectProfile(target)
	canonical, err := canonicalAstrixfile(path, scaffoldAstrixfile(sanitizeWorkspaceName(filepath.Base(target)), languages, profile))
	if err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if !force && os.IsExist(err) {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
		}
		return err
	}
	if _, err = file.Write([]byte(canonical)); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	ecosystem, image := "unknown", "none detected"
	if profileErr == nil && profile.Image != "" {
		ecosystem, image = profile.Ecosystem, profile.Image
	}
	summary := strings.Join(languages, ", ")
	if summary == "" {
		summary = "none detected"
	}
	fmt.Fprintf(stdout, "created %s (languages: %s; ecosystem: %s; sandbox image: %s)\n", path, summary, ecosystem, image)
	return nil
}

// DetectLanguages scans one project directory for ecosystem markers and returns
// the detected languages in a fixed, deterministic priority order.
func DetectLanguages(dir string) []string {
	manifests := map[string]bool{}
	active := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable subtrees should not prevent a useful best-effort init
		}
		if entry.IsDir() {
			if path != dir && ignoredLanguageScanDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if path == filepath.Join(dir, name) {
			manifests[name] = true
		}
		if strings.HasSuffix(strings.ToLower(name), ".csproj") {
			active["csharp"] = true
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".go":
			active["go"] = true
		case ".ts", ".tsx", ".mts", ".cts":
			active["typescript"] = true
		case ".js", ".jsx", ".mjs", ".cjs":
			active["javascript"] = true
		case ".py", ".pyi":
			active["python"] = true
		case ".rs":
			active["rust"] = true
		case ".java", ".kt", ".kts":
			active["java"] = true
		case ".cs":
			active["csharp"] = true
		case ".rb":
			active["ruby"] = true
		case ".php":
			active["php"] = true
		case ".c", ".h":
			active["c"] = true
		case ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx":
			active["cpp"] = true
		case ".swift":
			active["swift"] = true
		case ".zig":
			active["zig"] = true
		}
		return nil
	})
	if err != nil {
		return nil
	}
	// Manifests can identify a project even when it has not checked in source yet.
	if manifests["go.mod"] {
		active["go"] = true
	}
	if manifests["tsconfig.json"] {
		active["typescript"] = true
	}
	if manifests["package.json"] && !active["typescript"] && !active["javascript"] {
		active["javascript"] = true
	}
	if manifests["requirements.txt"] || manifests["pyproject.toml"] {
		active["python"] = true
	}
	if manifests["Cargo.toml"] {
		active["rust"] = true
	}
	if manifests["pom.xml"] || manifests["build.gradle"] || manifests["build.gradle.kts"] {
		active["java"] = true
	}
	if manifests["Gemfile"] {
		active["ruby"] = true
	}
	if manifests["composer.json"] {
		active["php"] = true
	}
	if manifests["CMakeLists.txt"] || manifests["Makefile"] {
		active["c"] = true
	}
	var languages []string
	if active["go"] {
		languages = append(languages, "go")
	}
	for _, language := range []string{"typescript", "javascript", "python", "rust", "java", "csharp", "ruby", "php", "c", "cpp", "swift", "zig"} {
		if active[language] {
			languages = append(languages, language)
		}
	}
	return languages
}

func ignoredLanguageScanDir(name string) bool {
	switch name {
	case ".git", ".astrix", "node_modules", "vendor", "target", "dist", "build", ".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

// scaffoldAstrixfile renders the starter policy source. Only valid AxLang
// constructs are emitted; image and command come from the detected sandbox
// profile and are omitted when unknown.
func scaffoldAstrixfile(name string, languages []string, profile runner.ProjectProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workspace %q {\n", name)
	quoted := make([]string, len(languages))
	for i, language := range languages {
		quoted[i] = fmt.Sprintf("%q", language)
	}
	fmt.Fprintf(&b, "  languages = [%s]\n", strings.Join(quoted, ", "))
	b.WriteString("  cache     = \".astrix/cache\"\n")
	b.WriteString("}\n\n")
	b.WriteString("guardrail \"security_boundaries\" {\n")
	b.WriteString("  deny when {\n")
	b.WriteString("    diff.modifies_tests(\"true\")\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")
	b.WriteString("stage \"verify\" {\n")
	b.WriteString("  scope   = ast.blast_radius\n")
	b.WriteString("  network = \"none\"\n")
	if profile.Image != "" {
		fmt.Fprintf(&b, "  image   = %q\n", profile.Image)
	}
	if command := strings.Join(profile.TestCommand, " "); command != "" && !isPlaceholderTestCommand(profile.TestCommand) {
		fmt.Fprintf(&b, "  command = %q\n", command)
	}
	b.WriteString("}\n\n")
	b.WriteString("heal \"heal\" on failure(stage.verify) {\n")
	b.WriteString("  max_attempts = 3\n")
	b.WriteString("  allow when {\n")
	b.WriteString("    actor.type == \"autonomous_llm\" and diff.modifies_tests(\"false\")\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// isPlaceholderTestCommand reports whether the profile fell back to the no-op
// command used for unrecognized repositories, in which case no command field
// should be generated.
func isPlaceholderTestCommand(command []string) bool {
	return len(command) == 3 && command[0] == "sh" && command[1] == "-lc" && command[2] == "true"
}

// canonicalAstrixfile round-trips source through the AxLang parser, semantic
// checker and formatter so the written file is valid and canonically laid out.
func canonicalAstrixfile(path, source string) (string, error) {
	canonical, err := validateAstrixfileSource(path, source)
	if err != nil {
		return "", err
	}
	// The canonical text itself must parse and check: what gets written is
	// exactly what downstream tooling will read.
	return validateAstrixfileSource(path, canonical)
}

func validateAstrixfileSource(path, source string) (string, error) {
	file, diags := parser.New(path, source).Parse()
	if len(diags) > 0 {
		return "", fmt.Errorf("generated Astrixfile does not parse:\n%s", formatDiagnostics(diags, source))
	}
	if diags := sema.Check(file); len(diags) > 0 {
		return "", fmt.Errorf("generated Astrixfile fails semantic checks:\n%s", formatDiagnostics(diags, source))
	}
	return formatter.Format(file), nil
}

// sanitizeWorkspaceName reduces a directory base name to a safe identifier.
func sanitizeWorkspaceName(raw string) string {
	var b strings.Builder
	dashed := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
			dashed = false
		default:
			if !dashed {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		name = "project"
	}
	return name
}
