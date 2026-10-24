package canary

import (
	"context"
	"testing"
	"time"
)

type sequenceMetrics struct {
	snapshots []Snapshot
	index     int
}

func (s *sequenceMetrics) Sample(context.Context) (Snapshot, error) {
	if s.index >= len(s.snapshots) {
		return Snapshot{}, nil
	}
	value := s.snapshots[s.index]
	s.index++
	return value, nil
}

func TestAdversarialCanaryPromotesOrImmediatelyRollsBack(t *testing.T) {
	config := Config{Steps: []float64{1, 5, 25, 100}, MaxP95Latency: 250 * time.Millisecond, MaxHTTP5xxRate: 0.005}
	good := Snapshot{P95Latency: 180 * time.Millisecond, HTTP5xxRate: 0.001}
	t.Run("healthy reaches 100 percent", func(t *testing.T) {
		traffic := &trafficStub{}
		metrics := &sequenceMetrics{snapshots: []Snapshot{good, good, good, good}}
		if err := (&Watcher{Metrics: metrics, Traffic: traffic}).Run(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		if len(traffic.shifts) != 4 || traffic.shifts[3] != 100 || traffic.rolledBack {
			t.Fatalf("healthy progression=%+v", traffic)
		}
	})
	t.Run("latency breach at 25 percent rolls back", func(t *testing.T) {
		traffic := &trafficStub{}
		metrics := &sequenceMetrics{snapshots: []Snapshot{good, good, {P95Latency: 310 * time.Millisecond, HTTP5xxRate: 0.001}}}
		err := (&Watcher{Metrics: metrics, Traffic: traffic}).Run(context.Background(), config)
		if err == nil || !traffic.rolledBack || len(traffic.shifts) != 3 || traffic.shifts[2] != 25 {
			t.Fatalf("latency breach err=%v traffic=%+v", err, traffic)
		}
	})
	t.Run("5xx breach at 5 percent rolls back", func(t *testing.T) {
		traffic := &trafficStub{}
		metrics := &sequenceMetrics{snapshots: []Snapshot{good, {P95Latency: 180 * time.Millisecond, HTTP5xxRate: 0.008}}}
		err := (&Watcher{Metrics: metrics, Traffic: traffic}).Run(context.Background(), config)
		if err == nil || !traffic.rolledBack || len(traffic.shifts) != 2 || traffic.shifts[1] != 5 {
			t.Fatalf("5xx breach err=%v traffic=%+v", err, traffic)
		}
	})
}
