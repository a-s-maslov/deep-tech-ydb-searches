package workload

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

func TestScenarioSeedIsStableAndScenarioSpecific(t *testing.T) {
	if got, want := scenarioSeed("vector", 7), scenarioSeed("vector", 7); got != want {
		t.Fatalf("scenario seed is not stable: got %d, want %d", got, want)
	}
	if scenarioSeed("vector", 7) == scenarioSeed("hybrid", 7) {
		t.Fatal("vector and hybrid workers must use independent query sequences")
	}
	if scenarioSeed("vector", 7) == scenarioSeed("vector", 8) {
		t.Fatal("different workers must use independent query sequences")
	}
}

func TestVerifiedWriteDocumentPreservesRealDocument(t *testing.T) {
	cfg := config.Config{DML: config.DML{IDStart: 10_000, PoolSize: 4}}
	source := model.Document{DocID: "ru-1", Title: "Сетевая ошибка", Text: "Сетевая ошибка\n\nСервис временно недоступен", Embedding: []byte{1, 2, 3}}
	document, marker := verifiedWriteDocument(cfg, 5, 1, source)
	if document.ID != 10_001 {
		t.Fatalf("id = %d, want 10001", document.ID)
	}
	if document.DocID != "workshop-live-000001" {
		t.Fatalf("docid = %q", document.DocID)
	}
	if !strings.Contains(document.Text, marker) {
		t.Fatalf("text does not contain the verification marker: %q", document.Text)
	}
	if len(document.Embedding) != 3 {
		t.Fatalf("embedding length = %d", len(document.Embedding))
	}
	if document.Title != source.Title || !strings.HasPrefix(document.Text, source.Text+" ") {
		t.Fatalf("source text was not preserved: title=%q text=%q", document.Title, document.Text)
	}
}

func TestAlphabeticTokenIsUniqueAndASCII(t *testing.T) {
	seen := map[string]bool{}
	for value := uint64(0); value < 1000; value++ {
		token := alphabeticToken(value)
		if seen[token] || !isASCII(token) {
			t.Fatalf("invalid token %q for %d", token, value)
		}
		seen[token] = true
	}
}

func TestRunVerifiedWritesUpsertsAndChecksEveryDocument(t *testing.T) {
	backend := &verifiedWriteBackend{markers: map[string]string{}}
	cfg := config.Config{DML: config.DML{
		Mode: "verified", RPS: 200, Workers: 2, IDStart: 10_000, PoolSize: 4,
	}}
	documents := []model.Document{
		{DocID: "1", Title: "one", Text: "one\n\nfirst document", Embedding: []byte{1, 2, 3}},
		{DocID: "2", Title: "two", Text: "two\n\nsecond document", Embedding: []byte{4, 5, 6}},
		{DocID: "3", Title: "three", Text: "three\n\nthird document", Embedding: []byte{7, 8, 9}},
		{DocID: "4", Title: "four", Text: "four\n\nfourth document", Embedding: []byte{10, 11, 12}},
	}
	runner := New(cfg, backend, []model.Query{{Embedding: []byte{1, 2, 3}}}, documents)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	runner.runVerifiedWrites(ctx)
	if got := backend.writes.Load(); got < 3 {
		t.Fatalf("writes = %d, want at least 3", got)
	}
	if got, want := backend.verifications.Load(), backend.writes.Load(); got != want {
		t.Fatalf("verifications = %d, writes = %d", got, want)
	}
	cycle := runner.stats["dml_check"].SnapshotAndReset("dml_check", time.Second, cfg.DML.RPS)
	if !cycle.HasData || cycle.Errors != 0 || cycle.RPS != float64(backend.writes.Load()) {
		t.Fatalf("composite DML snapshot = %+v, writes = %d", cycle, backend.writes.Load())
	}
}

type verifiedWriteBackend struct {
	mu            sync.Mutex
	markers       map[string]string
	writes        atomic.Int64
	verifications atomic.Int64
}

func (*verifiedWriteBackend) Fulltext(context.Context, model.Query) (int, int, error) {
	return 0, 0, nil
}
func (*verifiedWriteBackend) Vector(context.Context, model.Query) (int, int, error) {
	return 0, 0, nil
}
func (*verifiedWriteBackend) Hybrid(context.Context, model.Query) (int, int, error) {
	return 0, 0, nil
}

func (b *verifiedWriteBackend) UpsertDocument(_ context.Context, document model.Document) (int, error) {
	parts := strings.Split(document.Text, " ")
	marker := parts[len(parts)-1]
	b.mu.Lock()
	b.markers[marker] = document.DocID
	b.mu.Unlock()
	b.writes.Add(1)
	return 0, nil
}

func (b *verifiedWriteBackend) VerifyFulltextMarker(_ context.Context, marker, docID string) (int, error) {
	b.mu.Lock()
	got := b.markers[marker]
	b.mu.Unlock()
	if got != docID {
		return 0, fmt.Errorf("marker %q belongs to %q, want %q", marker, got, docID)
	}
	b.verifications.Add(1)
	return 0, nil
}

func isASCII(value string) bool {
	for _, symbol := range []byte(value) {
		if symbol > 127 {
			return false
		}
	}
	return true
}
