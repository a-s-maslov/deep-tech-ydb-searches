package quality

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

const reportFormatVersion = 1

type Backend interface {
	FulltextDocIDsLimit(context.Context, model.Query, int) ([]string, int, error)
	VectorDocIDsLimit(context.Context, model.Query, int) ([]string, int, error)
	HybridDocIDsLimit(context.Context, model.Query, int) ([]string, int, error)
}

// FulltextVariantBackend executes one explicitly described full-text branch.
// It is kept separate from Backend so offline experiments cannot silently
// change the workload and hybrid queries used by the workshop.
type FulltextVariantBackend interface {
	FulltextDocIDsVariant(context.Context, model.Query, model.FulltextConfig, int) ([]string, int, error)
}

type HybridVariantBackend interface {
	HybridDocIDsVariant(context.Context, model.Query, model.HybridConfig, int) ([]string, int, error)
}

type FulltextQueryResult struct {
	QID    string   `json:"qid"`
	DocIDs []string `json:"docids"`
}

type FulltextReport struct {
	FormatVersion int                   `json:"format_version"`
	GeneratedAt   time.Time             `json:"generated_at"`
	DurationMS    int64                 `json:"duration_ms"`
	Profile       string                `json:"profile"`
	QueryCount    int                   `json:"query_count"`
	Failures      int                   `json:"failures"`
	FailureSample []string              `json:"failure_sample,omitempty"`
	TopK          int                   `json:"top_k"`
	Config        model.FulltextConfig  `json:"config"`
	Metrics       RetrievalMetrics      `json:"metrics"`
	Results       []FulltextQueryResult `json:"results"`
}

type HybridReport struct {
	FormatVersion int                   `json:"format_version"`
	GeneratedAt   time.Time             `json:"generated_at"`
	DurationMS    int64                 `json:"duration_ms"`
	Profile       string                `json:"profile"`
	QueryCount    int                   `json:"query_count"`
	Failures      int                   `json:"failures"`
	FailureSample []string              `json:"failure_sample,omitempty"`
	TopK          int                   `json:"top_k"`
	Config        model.HybridConfig    `json:"config"`
	Metrics       RetrievalMetrics      `json:"metrics"`
	Results       []FulltextQueryResult `json:"results"`
}

type ExactQuery struct {
	QID    string    `json:"qid"`
	DocIDs []string  `json:"docids"`
	Scores []float64 `json:"scores,omitempty"`
}

type ExactArtifact struct {
	FormatVersion         int          `json:"format_version"`
	Profile               string       `json:"profile"`
	TopK                  int          `json:"top_k"`
	Metric                string       `json:"metric"`
	Documents             int          `json:"documents"`
	Queries               int          `json:"queries"`
	DatasetManifestSHA256 string       `json:"dataset_manifest_sha256"`
	QueryArtifactSHA256   string       `json:"query_artifact_sha256"`
	Results               []ExactQuery `json:"results"`
}

type RetrievalMetrics struct {
	QrelRecallMicro float64 `json:"qrel_recall_micro"`
	QrelRecallMacro float64 `json:"qrel_recall_macro"`
	HitRate         float64 `json:"hit_rate"`
	NDCG            float64 `json:"ndcg"`
	MRR             float64 `json:"mrr"`
}

type QueryResult struct {
	QID      string   `json:"qid"`
	Fulltext []string `json:"fulltext"`
	Vector   []string `json:"vector"`
	Hybrid   []string `json:"hybrid"`
}

type IndexConfig struct {
	Levels          int `json:"levels"`
	Clusters        int `json:"clusters"`
	OverlapClusters int `json:"overlap_clusters"`
	SearchTopSize   int `json:"search_top_size"`
}

type Report struct {
	FormatVersion int                         `json:"format_version"`
	GeneratedAt   time.Time                   `json:"generated_at"`
	DurationMS    int64                       `json:"duration_ms"`
	Profile       string                      `json:"profile"`
	Documents     int                         `json:"documents"`
	QueryCount    int                         `json:"query_count"`
	TopK          int                         `json:"top_k"`
	Metric        string                      `json:"metric"`
	ExactSHA256   string                      `json:"exact_artifact_sha256"`
	Index         IndexConfig                 `json:"index"`
	ANNRecall     float64                     `json:"ann_recall"`
	Metrics       map[string]RetrievalMetrics `json:"metrics"`
	Results       []QueryResult               `json:"results"`
}

func LoadExact(path string) (ExactArtifact, error) {
	var artifact ExactArtifact
	if err := readGZIPJSON(path, &artifact); err != nil {
		return artifact, err
	}
	if artifact.FormatVersion != reportFormatVersion || artifact.TopK < 1 || artifact.Metric != "inner_product" {
		return artifact, fmt.Errorf("invalid exact artifact contract")
	}
	if artifact.Queries != len(artifact.Results) {
		return artifact, fmt.Errorf("exact artifact query count mismatch: declared=%d actual=%d", artifact.Queries, len(artifact.Results))
	}
	seen := make(map[string]struct{}, len(artifact.Results))
	for _, result := range artifact.Results {
		if result.QID == "" || len(result.DocIDs) != artifact.TopK {
			return artifact, fmt.Errorf("invalid exact result for qid=%q", result.QID)
		}
		if _, exists := seen[result.QID]; exists {
			return artifact, fmt.Errorf("duplicate exact qid=%q", result.QID)
		}
		seen[result.QID] = struct{}{}
	}
	return artifact, nil
}

func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func Evaluate(ctx context.Context, backend Backend, queries []model.Query, exact ExactArtifact, workers int, index IndexConfig, exactSHA string) (Report, error) {
	started := time.Now()
	if workers < 1 {
		return Report{}, fmt.Errorf("quality workers must be positive")
	}
	exactByQID := make(map[string][]string, len(exact.Results))
	for _, result := range exact.Results {
		exactByQID[result.QID] = result.DocIDs
	}
	for _, query := range queries {
		if len(query.RelevantDocIDs) == 0 {
			return Report{}, fmt.Errorf("qid %s has no relevance judgments", query.QID)
		}
		if _, exists := exactByQID[query.QID]; !exists {
			return Report{}, fmt.Errorf("qid %s is absent from exact artifact", query.QID)
		}
	}

	results := make([]QueryResult, len(queries))
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
			query := queries[position]
			fulltext, _, fulltextErr := backend.FulltextDocIDsLimit(ctx, query, exact.TopK)
			vector, _, vectorErr := backend.VectorDocIDsLimit(ctx, query, exact.TopK)
			hybrid, _, hybridErr := backend.HybridDocIDsLimit(ctx, query, exact.TopK)
			if err := errors.Join(fulltextErr, vectorErr, hybridErr); err != nil {
				mu.Lock()
				problems = append(problems, fmt.Errorf("qid %s: %w", query.QID, err))
				mu.Unlock()
				continue
			}
			results[position] = QueryResult{QID: query.QID, Fulltext: fulltext, Vector: vector, Hybrid: hybrid}
		}
	}
	for range workers {
		wg.Add(1)
		go worker()
	}
	for position := range queries {
		jobs <- position
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if err := errors.Join(problems...); err != nil {
		return Report{}, err
	}

	runs := map[string][][]string{
		"exact":    make([][]string, len(queries)),
		"fulltext": make([][]string, len(queries)),
		"vector":   make([][]string, len(queries)),
		"hybrid":   make([][]string, len(queries)),
	}
	annRecall := 0.0
	for position, query := range queries {
		exactIDs := exactByQID[query.QID]
		runs["exact"][position] = exactIDs
		runs["fulltext"][position] = results[position].Fulltext
		runs["vector"][position] = results[position].Vector
		runs["hybrid"][position] = results[position].Hybrid
		annRecall += float64(intersection(results[position].Vector, exactIDs)) / float64(exact.TopK)
	}
	metrics := make(map[string]RetrievalMetrics, len(runs))
	for method, run := range runs {
		metrics[method] = retrievalMetrics(queries, run, 10)
	}
	return Report{
		FormatVersion: reportFormatVersion,
		GeneratedAt:   time.Now().UTC(),
		DurationMS:    time.Since(started).Milliseconds(),
		Profile:       exact.Profile,
		Documents:     exact.Documents,
		QueryCount:    len(queries),
		TopK:          exact.TopK,
		Metric:        exact.Metric,
		ExactSHA256:   exactSHA,
		Index:         index,
		ANNRecall:     annRecall / float64(len(queries)),
		Metrics:       metrics,
		Results:       results,
	}, nil
}

func EvaluateFulltext(ctx context.Context, backend FulltextVariantBackend, queries []model.Query, topK, workers int, cfg model.FulltextConfig) (FulltextReport, error) {
	started := time.Now()
	if topK < 1 || workers < 1 {
		return FulltextReport{}, fmt.Errorf("fulltext top-k and workers must be positive")
	}
	if cfg.Name == "" || cfg.Index == "" || cfg.Column == "" || cfg.MinimumShouldMatch == "" {
		return FulltextReport{}, fmt.Errorf("fulltext experiment name, index, column and minimum_should_match are required")
	}
	for _, query := range queries {
		if len(query.RelevantDocIDs) == 0 {
			return FulltextReport{}, fmt.Errorf("qid %s has no relevance judgments", query.QID)
		}
	}

	results := make([]FulltextQueryResult, len(queries))
	run := make([][]string, len(queries))
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
			query := queries[position]
			ids, _, err := backend.FulltextDocIDsVariant(ctx, query, cfg, topK)
			if err != nil {
				mu.Lock()
				problems = append(problems, fmt.Errorf("qid %s: %w", query.QID, err))
				mu.Unlock()
				continue
			}
			results[position] = FulltextQueryResult{QID: query.QID, DocIDs: ids}
			run[position] = ids
		}
	}
	for range workers {
		wg.Add(1)
		go worker()
	}
	for position := range queries {
		jobs <- position
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return FulltextReport{}, err
	}
	failureSample := make([]string, 0, min(10, len(problems)))
	for _, problem := range problems[:min(10, len(problems))] {
		failureSample = append(failureSample, problem.Error())
	}
	return FulltextReport{
		FormatVersion: reportFormatVersion,
		GeneratedAt:   time.Now().UTC(),
		DurationMS:    time.Since(started).Milliseconds(),
		QueryCount:    len(queries),
		Failures:      len(problems),
		FailureSample: failureSample,
		TopK:          topK,
		Config:        cfg,
		Metrics:       retrievalMetrics(queries, run, 10),
		Results:       results,
	}, nil
}

func EvaluateHybrid(ctx context.Context, backend HybridVariantBackend, queries []model.Query, topK, workers int, cfg model.HybridConfig) (HybridReport, error) {
	started := time.Now()
	if topK < 1 || workers < 1 {
		return HybridReport{}, fmt.Errorf("hybrid top-k and workers must be positive")
	}
	if cfg.Name == "" || cfg.FulltextIndex == "" || cfg.VectorIndex == "" {
		return HybridReport{}, fmt.Errorf("hybrid experiment name and indexes are required")
	}
	for _, query := range queries {
		if len(query.RelevantDocIDs) == 0 {
			return HybridReport{}, fmt.Errorf("qid %s has no relevance judgments", query.QID)
		}
	}

	results := make([]FulltextQueryResult, len(queries))
	run := make([][]string, len(queries))
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
			query := queries[position]
			ids, _, err := backend.HybridDocIDsVariant(ctx, query, cfg, topK)
			if err != nil {
				mu.Lock()
				problems = append(problems, fmt.Errorf("qid %s: %w", query.QID, err))
				mu.Unlock()
				continue
			}
			results[position] = FulltextQueryResult{QID: query.QID, DocIDs: ids}
			run[position] = ids
		}
	}
	for range workers {
		wg.Add(1)
		go worker()
	}
	for position := range queries {
		jobs <- position
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return HybridReport{}, err
	}
	failureSample := make([]string, 0, min(10, len(problems)))
	for _, problem := range problems[:min(10, len(problems))] {
		failureSample = append(failureSample, problem.Error())
	}
	return HybridReport{
		FormatVersion: reportFormatVersion,
		GeneratedAt:   time.Now().UTC(),
		DurationMS:    time.Since(started).Milliseconds(),
		QueryCount:    len(queries),
		Failures:      len(problems),
		FailureSample: failureSample,
		TopK:          topK,
		Config:        cfg,
		Metrics:       retrievalMetrics(queries, run, 10),
		Results:       results,
	}, nil
}

func retrievalMetrics(queries []model.Query, run [][]string, ndcgK int) RetrievalMetrics {
	var found, relevant int
	var macroRecall, hitQueries, ndcg, mrr float64
	for position, query := range queries {
		wanted := make(map[string]struct{}, len(query.RelevantDocIDs))
		for _, id := range query.RelevantDocIDs {
			wanted[id] = struct{}{}
		}
		hits := intersection(run[position], query.RelevantDocIDs)
		found += hits
		relevant += len(wanted)
		macroRecall += float64(hits) / float64(len(wanted))
		if hits > 0 {
			hitQueries++
		}
		limit := min(ndcgK, len(run[position]))
		dcg := 0.0
		firstRank := 0
		for rank := 0; rank < limit; rank++ {
			if _, ok := wanted[run[position][rank]]; !ok {
				continue
			}
			dcg += 1 / math.Log2(float64(rank+2))
			if firstRank == 0 {
				firstRank = rank + 1
			}
		}
		ideal := 0.0
		for rank := 0; rank < min(ndcgK, len(wanted)); rank++ {
			ideal += 1 / math.Log2(float64(rank+2))
		}
		if ideal > 0 {
			ndcg += dcg / ideal
		}
		if firstRank > 0 {
			mrr += 1 / float64(firstRank)
		}
	}
	count := float64(len(queries))
	return RetrievalMetrics{
		QrelRecallMicro: float64(found) / float64(relevant),
		QrelRecallMacro: macroRecall / count,
		HitRate:         hitQueries / count,
		NDCG:            ndcg / count,
		MRR:             mrr / count,
	}
}

func intersection(left, right []string) int {
	set := make(map[string]struct{}, len(right))
	for _, value := range right {
		set[value] = struct{}{}
	}
	seen := make(map[string]struct{}, len(left))
	hits := 0
	for _, value := range left {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		if _, ok := set[value]; ok {
			hits++
		}
	}
	return hits
}

func WriteReport(path string, report Report) error {
	return writeGZIPJSON(path, report)
}

func WriteFulltextReport(path string, report FulltextReport) error {
	return writeGZIPJSON(path, report)
}

func WriteHybridReport(path string, report HybridReport) error {
	return writeGZIPJSON(path, report)
}

func writeGZIPJSON(path string, value any) error {
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
	encodeErr := encoder.Encode(value)
	closeErr := stream.Close()
	fileErr := file.Close()
	if err := errors.Join(encodeErr, closeErr, fileErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err == nil {
		return nil
	}
	// Windows cannot atomically replace an existing target. Evaluation is an
	// offline command, so fall back to remove+rename there; Unix keeps the
	// atomic replace above.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporary, path)
}

func LoadReport(path string) (Report, error) {
	var report Report
	if err := readGZIPJSON(path, &report); err != nil {
		return report, err
	}
	if report.FormatVersion != reportFormatVersion || report.QueryCount < 1 || report.TopK < 1 {
		return report, fmt.Errorf("invalid quality report contract")
	}
	return report, nil
}

func readGZIPJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	stream, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer stream.Close()
	return json.NewDecoder(stream).Decode(target)
}

func SortedMethods(metrics map[string]RetrievalMetrics) []string {
	methods := make([]string, 0, len(metrics))
	for method := range metrics {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}
