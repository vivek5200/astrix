// Command astrixd serves the authenticated CI/webhook policy ingress.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vivek5200/astrix/pkg/app"
	"github.com/vivek5200/astrix/pkg/ingress"
	"github.com/vivek5200/astrix/pkg/policy"
)

type webhookRequest struct {
	Intent policy.ActionIntent `json:"intent"`
	Diff   string              `json:"diff,omitempty"`
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secret := os.Getenv("ASTRIX_WEBHOOK_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "ASTRIX_WEBHOOK_SECRET is required")
		os.Exit(2)
	}
	path := filepath.Join(root, "Astrixfile.ax")
	if _, err = os.Stat(path); err != nil {
		path = filepath.Join(filepath.Dir(root), "Astrixfile.ax")
	}
	gate, err := app.PolicyGate(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("POST /webhook", webhookHandler(gate, secret))
	addr := os.Getenv("ASTRIXD_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	fmt.Fprintf(os.Stderr, "astrixd listening on %s\n", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func webhookHandler(gate *policy.Gate, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		if !validSignature(secret, r.Header.Get("X-Astrix-Signature"), body) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		var request webhookRequest
		if err = json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid webhook JSON", http.StatusBadRequest)
			return
		}
		if findings := ingress.ScanUnifiedDiffSecrets(request.Diff); len(findings) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": false, "reason": "secret detected", "findings": findings})
			return
		}
		decision, err := gate.Evaluate(r.Context(), request.Intent)
		if err != nil {
			http.Error(w, "policy evaluation failed closed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !decision.Allowed {
			w.WriteHeader(http.StatusForbidden)
		}
		_ = json.NewEncoder(w).Encode(decision)
	})
}

func validSignature(secret, signature string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}
