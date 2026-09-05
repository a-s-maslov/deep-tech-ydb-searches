package corpus

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type Artifact struct {
	FormatVersion int           `json:"format_version"`
	Profile       string        `json:"profile"`
	Vector        Vector        `json:"vector"`
	Queries       []model.Query `json:"queries"`
}

type Vector struct {
	Dimension int    `json:"dimension"`
	Metric    string `json:"metric"`
}

func Load(path string, expectedDimension int) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("open query artifact: %w", err)
	}
	defer file.Close()
	stream, err := gzip.NewReader(file)
	if err != nil {
		return Artifact{}, fmt.Errorf("open query artifact gzip: %w", err)
	}
	defer stream.Close()
	var artifact Artifact
	if err := json.NewDecoder(stream).Decode(&artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode query artifact: %w", err)
	}
	if artifact.FormatVersion != 2 {
		return Artifact{}, fmt.Errorf("unsupported query artifact format %d", artifact.FormatVersion)
	}
	if artifact.Vector.Dimension != expectedDimension || artifact.Vector.Metric != "inner_product" {
		return Artifact{}, fmt.Errorf("query vector contract mismatch: %d/%s", artifact.Vector.Dimension, artifact.Vector.Metric)
	}
	if len(artifact.Queries) == 0 {
		return Artifact{}, fmt.Errorf("query artifact is empty")
	}
	expectedBytes := expectedDimension*4 + 1
	for index, query := range artifact.Queries {
		if query.QID == "" || query.Text == "" || len(query.Embedding) != expectedBytes || query.Embedding[expectedBytes-1] != 1 || len(query.RelevantDocIDs) == 0 {
			return Artifact{}, fmt.Errorf("invalid query at position %d", index)
		}
	}
	return artifact, nil
}
