// Package canary watches progressive traffic shifts and rolls back on SLO breaches.
package canary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vivek5200/axlang/ast"
	"github.com/vivek5200/axlang/token"
)

// Snapshot is a point-in-time view of deployment health metrics.
type Snapshot struct {
	P95Latency  time.Duration
	HTTP5xxRate float64
}

// MetricsSource supplies canary health metrics.
type MetricsSource interface {
	Sample(context.Context) (Snapshot, error)
}

// TrafficController shifts traffic and can restore the previous stable version.
type TrafficController interface {
	Shift(context.Context, float64) error
	Rollback(context.Context) error
}

// PrometheusSource reads latency and 5xx metrics from the Prometheus query API.
type PrometheusSource struct {
	BaseURL      string
	P95Query     string
	HTTP5xxQuery string
	Client       *http.Client
}

// Sample executes configured PromQL queries. Latency results are seconds; rate
// results are fractions between 0 and 1.
func (p PrometheusSource) Sample(ctx context.Context) (Snapshot, error) {
	if p.BaseURL == "" || p.P95Query == "" || p.HTTP5xxQuery == "" {
		return Snapshot{}, fmt.Errorf("Prometheus URL and both queries are required")
	}
	latency, err := p.query(ctx, p.P95Query)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query p95 latency: %w", err)
	}
	rate, err := p.query(ctx, p.HTTP5xxQuery)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query http 5xx rate: %w", err)
	}
	return Snapshot{P95Latency: time.Duration(latency * float64(time.Second)), HTTP5xxRate: rate}, nil
}

func (p PrometheusSource) query(ctx context.Context, expression string) (float64, error) {
	endpoint, err := url.Parse(p.BaseURL)
	if err != nil {
		return 0, err
	}
	endpoint.Path = endpoint.Path + "/api/v1/query"
	query := endpoint.Query()
	query.Set("query", expression)
	endpoint.RawQuery = query.Encode()
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("Prometheus returned %s", resp.Status)
	}
	var payload struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, err
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("Prometheus query failed: %s", payload.Error)
	}
	if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) < 2 {
		return 0, fmt.Errorf("Prometheus query returned no sample")
	}
	var value string
	if err = json.Unmarshal(payload.Data.Result[0].Value[1], &value); err != nil {
		var number json.Number
		if err = json.Unmarshal(payload.Data.Result[0].Value[1], &number); err != nil {
			return 0, err
		}
		value = number.String()
	}
	return strconv.ParseFloat(value, 64)
}

// HTTPController calls a deployment service to change traffic weight or roll back.
type HTTPController struct {
	BaseURL      string
	ShiftPath    string
	RollbackPath string
	Token        string
	Client       *http.Client
}

func (h HTTPController) Shift(ctx context.Context, percent float64) error {
	return h.post(ctx, h.ShiftPath, map[string]any{"percent": percent})
}
func (h HTTPController) Rollback(ctx context.Context) error {
	return h.post(ctx, h.RollbackPath, map[string]any{})
}
func (h HTTPController) post(ctx context.Context, path string, payload any) error {
	if h.BaseURL == "" || path == "" {
		return fmt.Errorf("deployment controller URL and path are required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target, err := url.JoinPath(h.BaseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("deployment controller returned %s", resp.Status)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// Config defines the release progression and SLO boundaries.
type Config struct {
	Steps          []float64
	Every          time.Duration
	MaxP95Latency  time.Duration
	MaxHTTP5xxRate float64
}

// ConfigFromDeploy derives canary steps, interval, and SLO limits from an AxLang deploy.
func ConfigFromDeploy(deploy *ast.DeployDecl) (Config, error) {
	if deploy == nil {
		return Config{}, fmt.Errorf("deploy declaration is required")
	}
	cfg := Config{}
	for _, field := range deploy.Fields {
		if field.Name == "steps" {
			list, ok := field.Value.(*ast.ListExpr)
			if !ok {
				return Config{}, fmt.Errorf("steps must be a percentage list")
			}
			for _, value := range list.Elements {
				literal, ok := value.(*ast.LiteralExpr)
				if !ok || literal.Kind != token.Percent {
					return Config{}, fmt.Errorf("steps must contain only percentages")
				}
				number, err := strconv.ParseFloat(literal.Value, 64)
				if err != nil {
					return Config{}, err
				}
				cfg.Steps = append(cfg.Steps, number)
			}
			if field.Every != nil {
				duration, err := time.ParseDuration(field.Every.Text)
				if err != nil {
					return Config{}, err
				}
				cfg.Every = duration
			}
		}
	}
	if err := readSLO(deploy.Rollback, &cfg); err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readSLO(expr ast.Expr, cfg *Config) error {
	switch node := expr.(type) {
	case *ast.BinaryExpr:
		if node.Operator == token.Or || node.Operator == token.And {
			if err := readSLO(node.Left, cfg); err != nil {
				return err
			}
			return readSLO(node.Right, cfg)
		}
		if node.Operator != token.Greater && node.Operator != token.GreaterEqual {
			return nil
		}
		metric, ok := node.Left.(*ast.IdentExpr)
		if !ok {
			return nil
		}
		limit, ok := node.Right.(*ast.LiteralExpr)
		if !ok {
			return nil
		}
		switch metric.Name {
		case "metrics.p95_latency":
			if limit.Kind != token.Duration {
				return fmt.Errorf("p95_latency rollback limit must be a duration")
			}
			duration, err := time.ParseDuration(limit.Text)
			if err != nil {
				return err
			}
			cfg.MaxP95Latency = duration
		case "metrics.http_5xx_rate":
			if limit.Kind != token.Percent {
				return fmt.Errorf("http_5xx_rate rollback limit must be a percentage")
			}
			value, err := strconv.ParseFloat(limit.Value, 64)
			if err != nil {
				return err
			}
			cfg.MaxHTTP5xxRate = value / 100
		}
	}
	return nil
}

// Watcher observes metrics after each canary step and stops on the first violation.
type Watcher struct {
	Metrics MetricsSource
	Traffic TrafficController
}

// Run performs each traffic step, waits between steps, and rolls back on unsafe or
// unknown health state. A successful return means the final step reached 100%.
func (w *Watcher) Run(ctx context.Context, cfg Config) error {
	if w == nil || w.Metrics == nil || w.Traffic == nil {
		return fmt.Errorf("canary metrics and traffic controller are required")
	}
	if err := validate(cfg); err != nil {
		return err
	}
	for i, step := range cfg.Steps {
		if err := w.Traffic.Shift(ctx, step); err != nil {
			return w.rollback(ctx, fmt.Errorf("shift traffic to %g%%: %w", step, err))
		}
		snapshot, err := w.Metrics.Sample(ctx)
		if err != nil {
			return w.rollback(ctx, fmt.Errorf("read canary metrics: %w", err))
		}
		if snapshot.P95Latency > cfg.MaxP95Latency || snapshot.HTTP5xxRate > cfg.MaxHTTP5xxRate {
			return w.rollback(ctx, fmt.Errorf("canary SLO breached at %g%%: p95=%s, http_5xx_rate=%g%%", step, snapshot.P95Latency, snapshot.HTTP5xxRate*100))
		}
		if i < len(cfg.Steps)-1 && cfg.Every > 0 {
			timer := time.NewTimer(cfg.Every)
			select {
			case <-ctx.Done():
				timer.Stop()
				return w.rollback(ctx, ctx.Err())
			case <-timer.C:
			}
		}
	}
	return nil
}

func (w *Watcher) rollback(ctx context.Context, cause error) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.Traffic.Rollback(rollbackCtx); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, err)
	}
	return cause
}
func validate(c Config) error {
	if len(c.Steps) == 0 {
		return fmt.Errorf("canary steps are required")
	}
	prev := 0.0
	for _, step := range c.Steps {
		if step <= prev || step > 100 {
			return fmt.Errorf("canary steps must be strictly increasing percentages between 0 and 100")
		}
		prev = step
	}
	if prev != 100 {
		return fmt.Errorf("canary steps must end at 100%%")
	}
	if c.MaxP95Latency <= 0 {
		return fmt.Errorf("p95 latency limit must be positive")
	}
	if c.MaxHTTP5xxRate < 0 || c.MaxHTTP5xxRate > 1 {
		return fmt.Errorf("http 5xx rate limit must be between 0 and 1")
	}
	return nil
}
