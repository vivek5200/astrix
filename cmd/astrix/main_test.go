package main

import "testing"

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
