package corpus

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

var errDocumentPoolFilled = errors.New("document pool filled")

// LoadDocumentPool reads a bounded set of complete documents for the DML
// stream. Text and embedding always come from the same artifact row.
func LoadDocumentPool(path string, limit int) ([]model.Document, error) {
	if limit < 1 {
		return nil, fmt.Errorf("document pool limit must be positive")
	}
	documents := make([]model.Document, 0, limit)
	_, err := ReadDocuments(path, min(limit, 250), func(batch []model.Document) error {
		remaining := limit - len(documents)
		if remaining < len(batch) {
			batch = batch[:remaining]
		}
		documents = append(documents, batch...)
		if len(documents) == limit {
			return errDocumentPoolFilled
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDocumentPoolFilled) {
		return nil, err
	}
	if len(documents) != limit {
		return nil, fmt.Errorf("document artifact contains %d rows, need %d for dml pool", len(documents), limit)
	}
	return documents, nil
}

func ReadDocuments(path string, batchSize int, consume func([]model.Document) error) (int, error) {
	if batchSize < 1 {
		return 0, fmt.Errorf("batch size must be positive")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open document artifact: %w", err)
	}
	defer file.Close()
	stream, err := gzip.NewReader(file)
	if err != nil {
		return 0, fmt.Errorf("open document artifact gzip: %w", err)
	}
	defer stream.Close()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	batch := make([]model.Document, 0, batchSize)
	total := 0
	for scanner.Scan() {
		var document model.Document
		if err := json.Unmarshal(scanner.Bytes(), &document); err != nil {
			return total, fmt.Errorf("decode document %d: %w", total, err)
		}
		if document.DocID == "" || document.Text == "" || len(document.Embedding) < 2 {
			return total, fmt.Errorf("invalid document %d", total)
		}
		batch = append(batch, document)
		total++
		if len(batch) == batchSize {
			if err := consume(batch); err != nil {
				return total, err
			}
			batch = batch[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return total, fmt.Errorf("read document artifact: %w", err)
	}
	if len(batch) > 0 {
		if err := consume(batch); err != nil {
			return total, err
		}
	}
	return total, nil
}
