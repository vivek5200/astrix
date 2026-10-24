package ingress

import "testing"

func TestParseUnifiedDiffRangesAndRename(t *testing.T) {
	diff := "diff --git a/pkg/a.go b/pkg/a.go\nindex 111..222 100644\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1,2 +1,3 @@\n a\n+b\n c\n@@ -10 +11,2 @@\n-x\n+y\n+z\n"
	changes, err := ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "pkg/a.go" {
		t.Fatalf("changes=%#v", changes)
	}
	want := []LineRange{{Start: 1, End: 3}, {Start: 11, End: 12}}
	if len(changes[0].Ranges) != len(want) {
		t.Fatalf("ranges=%#v", changes[0].Ranges)
	}
	for i, r := range want {
		if changes[0].Ranges[i] != r {
			t.Errorf("range %d=%+v want %+v", i, changes[0].Ranges[i], r)
		}
	}
}

func TestScanSecrets(t *testing.T) {
	awsKey := "AKIA" + "1234567890ABCDEF"
	source := []byte("AWS=" + awsKey + "\npassword = \"this-is-a-long-secret-value-123\"\nordinary text\n")
	findings := ScanSecrets(source)
	if len(findings) < 2 {
		t.Fatalf("findings=%#v", findings)
	}
	if findings[0].Kind != "aws_access_key" || findings[0].Line != 1 {
		t.Errorf("first finding=%+v", findings[0])
	}
}

func TestScanUnifiedDiffSecretsChecksOnlyAddedLines(t *testing.T) {
	secret := `long-super-secret-credential-value-abcdef123456789`
	diff := "--- a/config.txt\n+++ b/config.txt\n+token = \"" + secret + "\"\n-token = \"" + secret + "\"\n"
	findings := ScanUnifiedDiffSecrets(diff)
	if len(findings) == 0 || findings[0].Line != 1 {
		t.Fatalf("findings=%#v", findings)
	}
}
