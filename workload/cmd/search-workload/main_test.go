package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

func TestRelevantHits(t *testing.T) {
	if got := relevantHits([]string{"a", "b", "c"}, []string{"b", "x"}); got != 1 {
		t.Fatalf("relevantHits = %d, want 1", got)
	}
}

func TestQueriesForDiagnostic(t *testing.T) {
	input := []model.Query{{Text: "original", LexicalQuery: "lexical"}}
	lexical, err := queriesForDiagnostic(input, "lexical")
	if err != nil || lexical[0].FulltextText() != "lexical" {
		t.Fatalf("lexical diagnostic queries = %+v, err=%v", lexical, err)
	}
	original, err := queriesForDiagnostic(input, "original")
	if err != nil || original[0].FulltextText() != "original" {
		t.Fatalf("original diagnostic queries = %+v, err=%v", original, err)
	}
	if input[0].LexicalQuery != "lexical" {
		t.Fatal("input query was mutated")
	}
}

type fakeCheckBackend struct {
	hybridErr error
}

func (fakeCheckBackend) CountDocuments(context.Context) (uint64, error) { return 50000, nil }
func (fakeCheckBackend) Fulltext(context.Context, model.Query) (int, int, error) {
	return 10, 0, nil
}
func (fakeCheckBackend) VectorDocIDs(context.Context, model.Query) ([]string, int, error) {
	return []string{"relevant", "other"}, 0, nil
}
func (f fakeCheckBackend) HybridDocIDs(context.Context, model.Query) ([]string, int, error) {
	if f.hybridErr != nil {
		return nil, 0, f.hybridErr
	}
	return []string{"relevant"}, 0, nil
}

func TestRunChecksReportsSuccessfulBranchesBeforeHybridFailure(t *testing.T) {
	var output bytes.Buffer
	queries := []model.Query{{QID: "q1", RelevantDocIDs: []string{"relevant"}}}
	err := runChecks(context.Background(), fakeCheckBackend{hybridErr: errors.New("Unknown builtin: HybridRank")}, queries, &output)
	if err == nil || !strings.Contains(err.Error(), "hybrid check") {
		t.Fatalf("error = %v, want hybrid check failure", err)
	}
	for _, want := range []string{
		"count: OK documents=50000",
		"fulltext: OK rows=10 qid=q1",
		"vector: OK rows=2 qrel_hits=1 qid=q1",
		"hybrid: ERROR: Unknown builtin: HybridRank",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}
