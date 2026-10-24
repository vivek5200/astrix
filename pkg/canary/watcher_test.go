package canary

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vivek5200/axlang/ast"
	"github.com/vivek5200/axlang/parser"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type metricsStub struct {
	snapshot Snapshot
	err      error
}

func (m metricsStub) Sample(context.Context) (Snapshot, error) { return m.snapshot, m.err }

type trafficStub struct {
	shifts     []float64
	rolledBack bool
}

func (t *trafficStub) Shift(_ context.Context, step float64) error {
	t.shifts = append(t.shifts, step)
	return nil
}
func (t *trafficStub) Rollback(context.Context) error { t.rolledBack = true; return nil }

func TestWatcherCompletesProgressiveSteps(t *testing.T) {
	traffic := &trafficStub{}
	w := Watcher{Metrics: metricsStub{snapshot: Snapshot{P95Latency: 100 * time.Millisecond, HTTP5xxRate: 0.001}}, Traffic: traffic}
	err := w.Run(context.Background(), Config{Steps: []float64{1, 5, 25, 100}, MaxP95Latency: 250 * time.Millisecond, MaxHTTP5xxRate: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic.shifts) != 4 || traffic.shifts[3] != 100 || traffic.rolledBack {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestConfigFromDeployAST(t *testing.T) {
	file, diags := parser.New("Astrixfile.ax", `stage "s" {} deploy "d" after(stage.s) { steps = [1%, 5%, 100%] every 2m rollback on { metrics.p95_latency > 250ms or metrics.http_5xx_rate > 0.5% } }`).Parse()
	if len(diags) > 0 {
		t.Fatalf("parse: %#v", diags)
	}
	deploy := file.Decls[1].(*ast.DeployDecl)
	cfg, err := ConfigFromDeploy(deploy)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Steps) != 3 || cfg.Steps[2] != 100 || cfg.Every != 2*time.Minute || cfg.MaxP95Latency != 250*time.Millisecond || cfg.MaxHTTP5xxRate != 0.005 {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestWatcherRollsBackOnSLOBreachOrUnknownMetrics(t *testing.T) {
	for _, source := range []MetricsSource{metricsStub{snapshot: Snapshot{P95Latency: 300 * time.Millisecond}}, metricsStub{err: errors.New("metrics unavailable")}} {
		traffic := &trafficStub{}
		w := Watcher{Metrics: source, Traffic: traffic}
		err := w.Run(context.Background(), Config{Steps: []float64{1, 100}, MaxP95Latency: 250 * time.Millisecond, MaxHTTP5xxRate: 0.01})
		if err == nil || !traffic.rolledBack {
			t.Fatalf("err=%v rollback=%v", err, traffic.rolledBack)
		}
	}
}

func TestPrometheusSourceAndHTTPController(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{map[string]any{"value": []any{123, "0.25"}}}}})
	}))
	defer server.Close()
	source := PrometheusSource{BaseURL: server.URL, P95Query: "histogram_quantile", HTTP5xxQuery: "rate(errors)"}
	snapshot, err := source.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.P95Latency != 250*time.Millisecond || snapshot.HTTP5xxRate != 0.25 || len(queries) != 2 {
		t.Fatalf("snapshot=%+v queries=%v", snapshot, queries)
	}

	var auth string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/shift" {
			t.Errorf("path=%s", r.URL.Path)
		}
	}))
	defer controller.Close()
	traffic := HTTPController{BaseURL: controller.URL, ShiftPath: "/shift", Token: "secret"}
	if err := traffic.Shift(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("auth=%q", auth)
	}
}
