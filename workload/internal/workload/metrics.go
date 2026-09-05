package workload

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Metrics struct {
	application string
	mu          sync.RWMutex
	snapshots   map[string]Snapshot
	server      *http.Server
	listener    net.Listener
}

func NewMetrics(application, address string) (*Metrics, error) {
	metrics := &Metrics{application: application, snapshots: map[string]Snapshot{}}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen metrics: %w", err)
	}
	metrics.listener = listener
	metrics.server = &http.Server{Handler: metrics, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = metrics.server.Serve(listener) }()
	fmt.Printf("metrics: http://%s/metrics\n", listener.Addr())
	return metrics, nil
}

func (m *Metrics) Write(values []Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = make(map[string]Snapshot, len(values))
	for _, value := range values {
		if !value.HasData {
			continue
		}
		m.snapshots[value.Scenario] = value
		fmt.Printf("scenario=%-8s target=%6.1f rps=%6.1f errors=%d dropped=%d(%.1f/s) retries=%d in_flight=%d/%d p50=%6.1fms p99=%6.1fms\n",
			value.Scenario, value.TargetRPS, value.RPS, value.Errors, value.Dropped, value.DroppedRPS,
			value.Retries, value.InFlight, value.WorkerLimit, value.P50, value.P99)
	}
}

func (m *Metrics) Close(ctx context.Context) error { return m.server.Shutdown(ctx) }

func (m *Metrics) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.snapshots))
	for name := range m.snapshots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeSnapshot(writer, m.application, m.snapshots[name])
	}
}

func writeSnapshot(writer io.Writer, application string, value Snapshot) {
	okLabels := fmt.Sprintf("application=\"%s\",scenario=\"%s\",statut=\"ok\"", escape(application), escape(value.Scenario))
	errorLabels := fmt.Sprintf("application=\"%s\",scenario=\"%s\",statut=\"ko\"", escape(application), escape(value.Scenario))
	_, _ = fmt.Fprintf(writer,
		"ydb_workload_rps{%s} %.6f\n"+
			"ydb_workload_target_rps{%s} %.6f\n"+
			"ydb_workload_pct50{%s} %.6f\n"+
			"ydb_workload_pct95{%s} %.6f\n"+
			"ydb_workload_pct99{%s} %.6f\n"+
			"ydb_workload_pmax{%s} %.6f\n"+
			"ydb_workload_retries{%s} %d\n"+
			"ydb_workload_countError{%s} %d\n"+
			"ydb_workload_dropped{%s} %d\n"+
			"ydb_workload_retry_rps{%s} %.6f\n"+
			"ydb_workload_error_rps{%s} %.6f\n"+
			"ydb_workload_dropped_rps{%s} %.6f\n"+
			"ydb_workload_in_flight{%s} %d\n"+
			"ydb_workload_worker_limit{%s} %d\n",
		okLabels, value.RPS, okLabels, value.TargetRPS, okLabels, value.P50, okLabels, value.P95,
		okLabels, value.P99, okLabels, value.Max, okLabels, value.Retries, errorLabels, value.Errors, okLabels, value.Dropped,
		okLabels, value.RetryRPS, errorLabels, value.ErrorRPS, okLabels, value.DroppedRPS,
		okLabels, value.InFlight, okLabels, value.WorkerLimit)
}

func escape(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}
