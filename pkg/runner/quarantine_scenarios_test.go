package runner

import (
	"context"
	"testing"
	"time"
)

func TestQuarantineRequiresMixedResultsForSameASTHash(t *testing.T) {
	store, err := OpenQuarantine(t.TempDir()+"/history.db", 20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(200, 0)
	for i := 0; i < 20; i++ {
		passed := i != 4 && i != 13
		if err := store.RecordForHash(ctx, "TestFlakyNetwork", "ast-same", passed, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, score, err := store.QuarantinedForHash(ctx, "TestFlakyNetwork", "ast-same", 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if !quarantined || score != 0.1 {
		t.Fatalf("same-hash flaky test quarantined=%v score=%v; want true and 0.1", quarantined, score)
	}
	if names, err := store.QuarantinedTests(ctx, 0.05); err != nil || len(names) != 1 || names[0] != "TestFlakyNetwork" {
		t.Fatalf("quarantine set=%v err=%v", names, err)
	}
	for i := 0; i < 20; i++ {
		if err := store.RecordForHash(ctx, "TestFlakyNetwork", "ast-new-broken", false, base.Add(time.Duration(i+20)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, score, err = store.QuarantinedForHash(ctx, "TestFlakyNetwork", "ast-new-broken", 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined || score != 0 {
		t.Fatalf("consistently failing new AST was misclassified as flaky: quarantined=%v score=%v", quarantined, score)
	}
}
