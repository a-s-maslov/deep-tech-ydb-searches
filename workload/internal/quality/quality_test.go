package quality

import (
	"context"
	"testing"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type fakeBackend struct {
	fulltext map[string][]string
	vector   map[string][]string
	hybrid   map[string][]string
}

func (f fakeBackend) FulltextDocIDsLimit(_ context.Context, query model.Query, _ int) ([]string, int, error) {
	return f.fulltext[query.QID], 0, nil
}
func (f fakeBackend) VectorDocIDsLimit(_ context.Context, query model.Query, _ int) ([]string, int, error) {
	return f.vector[query.QID], 0, nil
}
func (f fakeBackend) HybridDocIDsLimit(_ context.Context, query model.Query, _ int) ([]string, int, error) {
	return f.hybrid[query.QID], 0, nil
}

func TestEvaluateSeparatesANNAndQrelRecall(t *testing.T) {
	queries := []model.Query{
		{QID: "q1", RelevantDocIDs: []string{"a"}},
		{QID: "q2", RelevantDocIDs: []string{"d"}},
	}
	exact := ExactArtifact{
		FormatVersion: 1, Profile: "test", TopK: 2, Metric: "inner_product", Documents: 4, Queries: 2,
		Results: []ExactQuery{{QID: "q1", DocIDs: []string{"a", "b"}}, {QID: "q2", DocIDs: []string{"c", "d"}}},
	}
	backend := fakeBackend{
		fulltext: map[string][]string{"q1": {"a", "x"}, "q2": {"x", "y"}},
		vector:   map[string][]string{"q1": {"a", "x"}, "q2": {"c", "d"}},
		hybrid:   map[string][]string{"q1": {"a", "x"}, "q2": {"d", "x"}},
	}
	report, err := Evaluate(context.Background(), backend, queries, exact, 2, IndexConfig{}, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if report.ANNRecall != 0.75 {
		t.Fatalf("ANN recall=%v, want 0.75", report.ANNRecall)
	}
	if report.Metrics["vector"].QrelRecallMicro != 1 {
		t.Fatalf("vector qrel recall=%v, want 1", report.Metrics["vector"].QrelRecallMicro)
	}
	if report.Metrics["fulltext"].QrelRecallMicro != 0.5 {
		t.Fatalf("fulltext qrel recall=%v, want 0.5", report.Metrics["fulltext"].QrelRecallMicro)
	}
	if report.Metrics["hybrid"].NDCG != 1 {
		t.Fatalf("hybrid nDCG=%v, want 1", report.Metrics["hybrid"].NDCG)
	}
}

func TestReportRoundTrip(t *testing.T) {
	path := t.TempDir() + "/quality.json.gz"
	if err := WriteReport(path, Report{FormatVersion: 1, Profile: "old", QueryCount: 1, TopK: 10}); err != nil {
		t.Fatal(err)
	}
	want := Report{FormatVersion: 1, Profile: "test", QueryCount: 2, TopK: 30}
	if err := WriteReport(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != want.Profile || got.QueryCount != want.QueryCount {
		t.Fatalf("round trip=%+v", got)
	}
}
