package diagnostic

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

const formatVersion = 2

type FulltextBackend interface {
	FulltextDocIDsVariant(context.Context, model.Query, model.FulltextConfig, int) ([]string, int, error)
	ProfileFulltextVariant(context.Context, model.Query, model.FulltextConfig, int) (model.QueryExecutionStats, error)
}

type TableAccess struct {
	Name  string `json:"name"`
	Rows  uint64 `json:"rows"`
	Bytes uint64 `json:"bytes"`
}

type QueryObservation struct {
	QID                string        `json:"qid"`
	Text               string        `json:"text"`
	LexicalQuery       string        `json:"lexical_query"`
	ClientDurationMS   float64       `json:"client_duration_ms"`
	ServerDurationMS   float64       `json:"server_duration_ms"`
	CPUTimeMS          float64       `json:"cpu_time_ms"`
	Retries            int           `json:"retries"`
	ResultCount        int           `json:"result_count"`
	ReadRows           uint64        `json:"read_rows"`
	ReadBytes          uint64        `json:"read_bytes"`
	ScoredDocumentRows uint64        `json:"scored_document_rows"`
	TableAccesses      []TableAccess `json:"table_accesses,omitempty"`
	Error              string        `json:"error,omitempty"`
}

type Summary struct {
	Succeeded                 int     `json:"succeeded"`
	Failed                    int     `json:"failed"`
	ClientP50MS               float64 `json:"client_p50_ms"`
	ClientP95MS               float64 `json:"client_p95_ms"`
	ClientP99MS               float64 `json:"client_p99_ms"`
	ClientMaxMS               float64 `json:"client_max_ms"`
	ServerP95MS               float64 `json:"server_p95_ms"`
	CPUTimeP95MS              float64 `json:"cpu_time_p95_ms"`
	ScoredRowsP50             float64 `json:"scored_document_rows_p50"`
	ScoredRowsP95             float64 `json:"scored_document_rows_p95"`
	ScoredRowsMax             uint64  `json:"scored_document_rows_max"`
	DurationScoredRowsPearson float64 `json:"duration_scored_document_rows_pearson"`
	DurationCPUPearson        float64 `json:"duration_cpu_pearson"`
}

type Report struct {
	FormatVersion       int                  `json:"format_version"`
	GeneratedAt         time.Time            `json:"generated_at"`
	DurationMS          int64                `json:"duration_ms"`
	Profile             string               `json:"profile"`
	Documents           uint64               `json:"documents"`
	Partitions          uint64               `json:"fulltext_docs_partitions"`
	QueryCount          int                  `json:"query_count"`
	TopK                int                  `json:"top_k"`
	Workers             int                  `json:"workers"`
	Warmup              bool                 `json:"warmup"`
	QueryRepresentation string               `json:"query_representation,omitempty"`
	Config              model.FulltextConfig `json:"config"`
	Summary             Summary              `json:"summary"`
	Slowest             []QueryObservation   `json:"slowest"`
	Queries             []QueryObservation   `json:"queries"`
}

func EvaluateFulltext(ctx context.Context, backend FulltextBackend, profile string, documents, partitions uint64, queries []model.Query, topK, workers int, warmup bool, cfg model.FulltextConfig) (Report, error) {
	started := time.Now()
	if topK < 1 || workers < 1 {
		return Report{}, fmt.Errorf("diagnostic top-k and workers must be positive")
	}
	if len(queries) == 0 {
		return Report{}, fmt.Errorf("diagnostic requires at least one query")
	}
	if warmup {
		if err := runConcurrent(ctx, len(queries), workers, func(position int) error {
			_, _, err := backend.FulltextDocIDsVariant(ctx, queries[position], cfg, topK)
			return err
		}); err != nil {
			return Report{}, fmt.Errorf("fulltext diagnostic warmup: %w", err)
		}
	}

	observations := make([]QueryObservation, len(queries))
	if err := runConcurrent(ctx, len(queries), workers, func(position int) error {
		query := queries[position]
		stats, err := backend.ProfileFulltextVariant(ctx, query, cfg, topK)
		observation := QueryObservation{QID: query.QID, Text: query.Text, LexicalQuery: query.FulltextText()}
		if err != nil {
			observation.Error = err.Error()
			observations[position] = observation
			return nil
		}
		observation.ClientDurationMS = milliseconds(stats.ClientDuration)
		observation.ServerDurationMS = milliseconds(stats.ServerDuration)
		observation.CPUTimeMS = milliseconds(stats.CPUTime)
		observation.Retries = stats.Retries
		observation.ResultCount = stats.ResultCount
		observation.ReadRows = stats.ReadRows
		observation.ReadBytes = stats.ReadBytes
		observation.ScoredDocumentRows = stats.ScoredDocumentRows
		observation.TableAccesses = make([]TableAccess, len(stats.TableAccesses))
		for i, access := range stats.TableAccesses {
			observation.TableAccesses[i] = TableAccess{Name: access.Name, Rows: access.Rows, Bytes: access.Bytes}
		}
		observations[position] = observation
		return nil
	}); err != nil {
		return Report{}, err
	}

	summary := summarize(observations)
	slowest := append([]QueryObservation(nil), observations...)
	sort.Slice(slowest, func(i, j int) bool {
		return slowest[i].ClientDurationMS > slowest[j].ClientDurationMS
	})
	if len(slowest) > 20 {
		slowest = slowest[:20]
	}
	return Report{
		FormatVersion: formatVersion,
		GeneratedAt:   time.Now().UTC(),
		DurationMS:    time.Since(started).Milliseconds(),
		Profile:       profile,
		Documents:     documents,
		Partitions:    partitions,
		QueryCount:    len(queries),
		TopK:          topK,
		Workers:       workers,
		Warmup:        warmup,
		Config:        cfg,
		Summary:       summary,
		Slowest:       slowest,
		Queries:       observations,
	}, nil
}

func WriteReport(path string, report Report) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	stream := gzip.NewWriter(file)
	encoder := json.NewEncoder(stream)
	encoder.SetEscapeHTML(false)
	err = errors.Join(encoder.Encode(report), stream.Close(), file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporary, path)
}

func runConcurrent(ctx context.Context, count, workers int, run func(int) error) error {
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var problems []error
	worker := func() {
		defer wg.Done()
		for position := range jobs {
			if ctx.Err() != nil {
				continue
			}
			if err := run(position); err != nil {
				mu.Lock()
				problems = append(problems, err)
				mu.Unlock()
			}
		}
	}
	for range workers {
		wg.Add(1)
		go worker()
	}
	for position := range count {
		jobs <- position
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(problems...)
}

func summarize(observations []QueryObservation) Summary {
	client := make([]float64, 0, len(observations))
	server := make([]float64, 0, len(observations))
	cpu := make([]float64, 0, len(observations))
	scoredRows := make([]float64, 0, len(observations))
	failed := 0
	var maxRows uint64
	for _, observation := range observations {
		if observation.Error != "" {
			failed++
			continue
		}
		client = append(client, observation.ClientDurationMS)
		server = append(server, observation.ServerDurationMS)
		cpu = append(cpu, observation.CPUTimeMS)
		scoredRows = append(scoredRows, float64(observation.ScoredDocumentRows))
		maxRows = max(maxRows, observation.ScoredDocumentRows)
	}
	return Summary{
		Succeeded:                 len(client),
		Failed:                    failed,
		ClientP50MS:               percentile(client, .50),
		ClientP95MS:               percentile(client, .95),
		ClientP99MS:               percentile(client, .99),
		ClientMaxMS:               percentile(client, 1),
		ServerP95MS:               percentile(server, .95),
		CPUTimeP95MS:              percentile(cpu, .95),
		ScoredRowsP50:             percentile(scoredRows, .50),
		ScoredRowsP95:             percentile(scoredRows, .95),
		ScoredRowsMax:             maxRows,
		DurationScoredRowsPearson: pearson(client, scoredRows),
		DurationCPUPearson:        pearson(client, cpu),
	}
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	return ordered[lower] + (ordered[upper]-ordered[lower])*(position-float64(lower))
}

func pearson(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var leftMean, rightMean float64
	for i := range left {
		leftMean += left[i]
		rightMean += right[i]
	}
	leftMean /= float64(len(left))
	rightMean /= float64(len(right))
	var covariance, leftVariance, rightVariance float64
	for i := range left {
		leftDelta := left[i] - leftMean
		rightDelta := right[i] - rightMean
		covariance += leftDelta * rightDelta
		leftVariance += leftDelta * leftDelta
		rightVariance += rightDelta * rightDelta
	}
	if leftVariance == 0 || rightVariance == 0 {
		return 0
	}
	return covariance / math.Sqrt(leftVariance*rightVariance)
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}
