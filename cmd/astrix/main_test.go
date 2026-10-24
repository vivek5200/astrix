package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseGraphArgs(t *testing.T) {
	tests := []struct {
		args               []string
		path, diff, symbol string
	}{
		{args: []string{"."}, path: "."},
		{args: []string{"src", "--symbol", "Add"}, path: "src", symbol: "Add"},
		{args: []string{".", "--diff", "change.diff"}, path: ".", diff: "change.diff"},
	}
	for _, test := range tests {
		path, diff, symbol, err := parseGraphArgs(test.args)
		if err != nil {
			t.Fatalf("parseGraphArgs(%v): %v", test.args, err)
		}
		if path != test.path || diff != test.diff || symbol != test.symbol {
			t.Errorf("parseGraphArgs(%v)=(%q,%q,%q)", test.args, path, diff, symbol)
		}
	}
	if _, _, _, err := parseGraphArgs([]string{".", "--diff", "x", "--symbol", "Add"}); err == nil {
		t.Fatal("expected mutually exclusive graph options to fail")
	}
}

func TestProjectTestCommandDetectsNodeAndSupportsCustomArgv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	command, err := projectTestCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("projectTestCommand()=%v, want %v", command, want)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", `["./ci/check.sh","--ci"]`)
	command, err = projectTestCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./ci/check.sh", "--ci"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("custom projectTestCommand()=%v, want %v", command, want)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", `"npm test"`)
	if _, err := projectTestCommand(root); err == nil {
		t.Fatal("invalid string command should be rejected; expected a JSON argv array")
	}
}
