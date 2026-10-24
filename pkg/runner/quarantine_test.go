package runner

import (
	"context"
	"testing"
	"time"
)

func TestQuarantineUsesRollingOutcomeTransitions(t *testing.T) {
	store, err := OpenQuarantine(t.TempDir()+"/tests.db", 20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(100, 0)
	for i, passed := range []bool{true, true, false, true, true} {
		if err := store.Record(ctx, "TestFlaky", passed, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, score, err := store.Quarantined(ctx, "TestFlaky", 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if !quarantined || score != 0.2 {
		t.Fatalf("quarantined=%v score=%v", quarantined, score)
	}
	if err := store.Record(ctx, "TestStable", true, base); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "TestStable", true, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if names, err := store.QuarantinedTests(ctx, 0.05); err != nil || len(names) != 1 || names[0] != "TestFlaky" {
		t.Fatalf("quarantined tests=%v err=%v", names, err)
	}
}
