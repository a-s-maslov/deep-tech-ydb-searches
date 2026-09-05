package diagnostic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type fakeBackend struct {
	profile map[string]model.QueryExecutionStats
	fail    map[string]bool
	warmups int
}

func (f *fakeBackend) FulltextDocIDsVariant(_ context.Context, _ model.Query, _ model.FulltextConfig, _ int) ([]string, int, error) {
	f.warmups++
	return []string{"doc"}, 0, nil
}

func (f *fakeBackend) ProfileFulltextVariant(_ context.Context, query model.Query, _ model.FulltextConfig, _ int) (model.QueryExecutionStats, error) {
	if f.fail[query.QID] {
		return model.QueryExecutionStats{}, errors.New("profile failed")
	}
	return f.profile[query.QID], nil
}

func TestEvaluateFulltextSummarizesPerQueryWork(t *testing.T) {
	backend := &fakeBackend{profile: map[string]model.QueryExecutionStats{
		"q1": {
			ClientDuration: 10 * time.Millisecond, ServerDuration: 8 * time.Millisecond, CPUTime: 4 * time.Millisecond,
			ResultCount: 10, ReadRows: 200, ReadBytes: 1000, ScoredDocumentRows: 100,
			TableAccesses: []model.QueryTableAccess{{Name: "index", Rows: 100, Bytes: 1000}},
		},
		"q2": {
			ClientDuration: 30 * time.Millisecond, ServerDuration: 25 * time.Millisecond, CPUTime: 12 * time.Millisecond,
			ResultCount: 10, ReadRows: 600, ReadBytes: 3000, ScoredDocumentRows: 300,
			TableAccesses: []model.QueryTableAccess{{Name: "index", Rows: 300, Bytes: 3000}},
		},
	}}
	queries := []model.Query{{QID: "q1", Text: "one"}, {QID: "q2", Text: "two"}}
	cfg := model.FulltextConfig{Name: "test", Index: "ft", Column: "text", MinimumShouldMatch: "50%"}
	report, err := EvaluateFulltext(context.Background(), backend, "test", 1000, 2, queries, 10, 1, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if backend.warmups != 2 {
		t.Fatalf("warmups=%d, want 2", backend.warmups)
	}
	if report.Summary.Succeeded != 2 || report.Summary.Failed != 0 {
		t.Fatalf("summary=%+v", report.Summary)
	}
	if report.Summary.ClientP50MS != 20 || report.Summary.ClientP95MS != 29 {
		t.Fatalf("latency summary=%+v", report.Summary)
	}
	if report.Summary.DurationScoredRowsPearson != 1 {
		t.Fatalf("duration/scored rows correlation=%v, want 1", report.Summary.DurationScoredRowsPearson)
	}
	if report.Slowest[0].QID != "q2" || report.Slowest[0].TableAccesses[0].Rows != 300 {
		t.Fatalf("slowest=%+v", report.Slowest[0])
	}
}

func TestEvaluateFulltextKeepsPerQueryFailure(t *testing.T) {
	backend := &fakeBackend{profile: map[string]model.QueryExecutionStats{}, fail: map[string]bool{"q1": true}}
	cfg := model.FulltextConfig{Name: "test", Index: "ft", Column: "text", MinimumShouldMatch: "50%"}
	report, err := EvaluateFulltext(context.Background(), backend, "test", 1000, 1, []model.Query{{QID: "q1", Text: "broken"}}, 10, 1, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Failed != 1 || report.Queries[0].Error == "" {
		t.Fatalf("report=%+v", report)
	}
}
