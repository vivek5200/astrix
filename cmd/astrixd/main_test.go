package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/vivek5200/astrix/pkg/policy"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidSignature(t *testing.T) {
	body := []byte("payload")
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	if !validSignature("secret", "sha256="+hex.EncodeToString(mac.Sum(nil)), body) {
		t.Fatal("valid signature rejected")
	}
	if validSignature("secret", "sha256=00", body) {
		t.Fatal("invalid signature accepted")
	}
}

func TestWebhookRejectsUnsignedAndSecretBearingRequests(t *testing.T) {
	gate, err := policy.NewGate("package astrix.generated\nimport rego.v1\ndefault allow := true\ndefault deny := false\ndefault require_approval := false\n")
	if err != nil {
		t.Fatal(err)
	}
	handler := webhookHandler(gate, "secret")
	unsigned := httptest.NewRecorder()
	handler.ServeHTTP(unsigned, httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{}`)))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned status=%d", unsigned.Code)
	}
	awsKey := "AKIA" + "1234567890ABCDEF"
	body := `{"intent":{"actor":{"type":"human"},"target":{"env":"dev"},"modified_files":[],"diff":{"modifies_tests":"false"}},"diff":"+` + awsKey + `"}`
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("X-Astrix-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "secret detected") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
