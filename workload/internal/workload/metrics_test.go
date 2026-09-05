package workload

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsOmitsEmptyIntervalAndKeepsChaosLabels(t *testing.T) {
	m := &Metrics{application: "demo", snapshots: map[string]Snapshot{}}
	m.Write([]Snapshot{{Scenario: "vector", HasData: true, RPS: 2, P99: 12}})
	r := httptest.NewRecorder()
	m.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	body := r.Body.String()
	if !strings.Contains(body, `statut="ok"`) || !strings.Contains(body, `statut="ko"`) {
		t.Fatalf("chaos-md compatible labels are absent: %s", body)
	}
	for _, metric := range []string{
		"ydb_workload_error_rps", "ydb_workload_retry_rps", "ydb_workload_dropped_rps",
		"ydb_workload_in_flight", "ydb_workload_worker_limit",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("diagnostic metric %s is absent: %s", metric, body)
		}
	}
	m.Write([]Snapshot{{Scenario: "vector"}})
	r = httptest.NewRecorder()
	m.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	if r.Body.Len() != 0 {
		t.Fatalf("empty interval must not expose stale metrics: %s", r.Body.String())
	}
}

func TestSnapshotReportsRatesAndCurrentConcurrency(t *testing.T) {
	stats := &Stats{}
	stats.ConfigureConcurrency(4)
	stats.Begin()
	stats.Record(time.Millisecond, 2, nil)
	stats.Drop()

	snapshot := stats.SnapshotAndReset("vector", 2*time.Second, 10)
	if snapshot.RPS != .5 || snapshot.RetryRPS != 1 || snapshot.DroppedRPS != .5 {
		t.Fatalf("unexpected rates: %+v", snapshot)
	}
	if snapshot.InFlight != 1 || snapshot.WorkerLimit != 4 {
		t.Fatalf("unexpected concurrency: %+v", snapshot)
	}

	stats.End()
	snapshot = stats.SnapshotAndReset("vector", time.Second, 10)
	if snapshot.InFlight != 0 {
		t.Fatalf("in-flight requests did not return to zero: %+v", snapshot)
	}
}
