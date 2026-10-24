package ingress

import (
	"strings"
	"testing"
)

func TestAdversarialCredentialMatrixIsDetectedAndRedacted(t *testing.T) {
	awsKey := "AKIA" + "1234567890ABCDEF"
	githubToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB"
	pemHeader := "-----BEGIN " + "RSA PRIVATE KEY" + "-----"
	secrets := []struct {
		name, value string
	}{
		{"aws", awsKey},
		{"github", githubToken},
		{"openrouter", "sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"pem", pemHeader},
		{"entropy", "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHw=="},
	}
	for _, secret := range secrets {
		t.Run(secret.name, func(t *testing.T) {
			diff := "diff --git a/config.txt b/config.txt\n--- a/config.txt\n+++ b/config.txt\n@@ -1 +1 @@\n-old\n+credential=" + secret.value + "\n"
			findings := ScanUnifiedDiffSecrets(diff)
			if len(findings) == 0 {
				t.Fatalf("secret was not detected: %q", secret.name)
			}
			for _, finding := range findings {
				if finding.Value != "[REDACTED]" {
					t.Errorf("finding exposed secret value: %+v", finding)
				}
				encoded := finding.Kind + finding.Value
				if strings.Contains(encoded, secret.value) {
					t.Errorf("finding contains secret material: %+v", finding)
				}
			}
		})
	}
}

func TestSecretScanOnlyExaminesAddedDiffLines(t *testing.T) {
	secret := "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB"
	diff := "--- a/config.txt\n+++ b/config.txt\n " + secret + "\n-old=" + secret + "\n+safe=value\n"
	if findings := ScanUnifiedDiffSecrets(diff); len(findings) != 0 {
		t.Fatalf("unchanged/deleted secret triggered ingress: %+v", findings)
	}
}
