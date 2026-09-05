package corpus

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

func TestLoadDocumentPoolKeepsTextAndEmbeddingFromSameRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.jsonl.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	stream := gzip.NewWriter(file)
	encoder := json.NewEncoder(stream)
	for index := 0; index < 5; index++ {
		document := model.Document{
			ID: uint64(index), DocID: string(rune('a' + index)),
			Title: "title", Text: "title\n\nbody", Embedding: []byte{byte(index), 1},
		}
		if err := encoder.Encode(document); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	documents, err := LoadDocumentPool(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 3 {
		t.Fatalf("documents = %d, want 3", len(documents))
	}
	for index, document := range documents {
		if document.DocID != string(rune('a'+index)) || document.Embedding[0] != byte(index) {
			t.Fatalf("document %d mixed artifact rows: %+v", index, document)
		}
	}
}

func TestLoadDocumentPoolRejectsInsufficientArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.jsonl.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	stream := gzip.NewWriter(file)
	if err := json.NewEncoder(stream).Encode(model.Document{DocID: "one", Text: "title\n\nbody", Embedding: []byte{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadDocumentPool(path, 2); err == nil {
		t.Fatal("insufficient artifact was accepted")
	}
}
