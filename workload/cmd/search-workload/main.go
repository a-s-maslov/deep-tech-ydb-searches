package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/corpus"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/diagnostic"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/observer"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/quality"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/workload"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/ydbstore"
)

func main() {
	configPath := flag.String("config", "config.json", "workload JSON configuration")
	datasetConfig := flag.String("dataset-config", "config/datasets.json", "dataset profile catalog")
	datasetProfile := flag.String("dataset-profile", "", "named dataset profile")
	drop := flag.Bool("drop", false, "drop the table before init")
	batchSize := flag.Int("batch-size", 250, "document load batch size")
	waitTimeout := flag.Duration("wait-timeout", 15*time.Minute, "index readiness timeout")
	partitionScope := flag.String("scope", "all", "partition scope: base, fulltext, vector, vector-level or all")
	profile := flag.String("profile", "", "named workload profile from the configuration")
	qualityOutput := flag.String("quality-output", "", "output for a fulltext-quality experiment")
	qualityName := flag.String("quality-name", "", "name of a fulltext-quality experiment")
	qualityTable := flag.String("quality-table", "", "optional table used by a fulltext-quality experiment")
	qualityIndex := flag.String("quality-index", "", "fulltext index used by a fulltext-quality experiment")
	qualityColumn := flag.String("quality-column", "text", "text column used by a fulltext-quality experiment")
	qualityMSM := flag.String("quality-msm", "50%", "MinimumShouldMatch used by a fulltext-quality experiment")
	qualityQueryTransform := flag.String("quality-query-transform", "lexical", "query representation: lexical, lexical-required-entity, lexical-required-1, lexical-required-2, original, none, strip-stress or ru-question-words")
	qualityK1 := flag.Float64("quality-k1", 0, "optional BM25 K1 used by a fulltext-quality experiment")
	qualityB := flag.Float64("quality-b", 0, "optional BM25 B used by a fulltext-quality experiment")
	qualitySnowball := flag.Bool("quality-snowball", false, "enable Russian Snowball when creating a fulltext-quality index")
	qualityFilterLengthMin := flag.Int("quality-filter-length-min", 0, "optional minimum token length for a fulltext-quality index")
	qualityFilterLengthMax := flag.Int("quality-filter-length-max", 0, "optional maximum token length for a fulltext-quality index")
	qualityVectorIndex := flag.String("quality-vector-index", "", "vector index used by a hybrid-quality experiment")
	qualityVectorColumn := flag.String("quality-vector-column", "embedding", "vector column used by a hybrid-quality experiment")
	qualityFulltextWeight := flag.Float64("quality-fulltext-weight", 1, "fulltext branch weight for a hybrid-quality experiment")
	qualityVectorWeight := flag.Float64("quality-vector-weight", 1, "vector branch weight for a hybrid-quality experiment")
	qualityHybridMode := flag.String("quality-hybrid-mode", "rrf", "hybrid fusion mode: rrf or linear")
	qualityRRFK := flag.Float64("quality-rrf-k", 60, "RRF K for a hybrid-quality experiment")
	qualityLinearNormalize := flag.Bool("quality-linear-normalize", true, "min-max normalize branches in linear hybrid mode")
	qualityBranchLimit := flag.Int("quality-branch-limit", 30, "candidate count per branch for a hybrid-quality experiment")
	qualityWorkers := flag.Int("quality-workers", 0, "worker override for a fulltext-quality experiment")
	qualityQueryLimit := flag.Int("quality-query-limit", 0, "evenly sampled query count for a fulltext-quality experiment (0 means all)")
	qualityTopK := flag.Int("quality-top-k", 30, "result depth for a fulltext-quality experiment")
	diagnosticOutput := flag.String("diagnostic-output", "", "gzip JSON output for a fulltext latency diagnostic")
	diagnosticWorkers := flag.Int("diagnostic-workers", 1, "parallel workers for a fulltext latency diagnostic")
	diagnosticQueryLimit := flag.Int("diagnostic-query-limit", 0, "evenly sampled diagnostic query count (0 means all)")
	diagnosticQueryRepresentation := flag.String("diagnostic-query-representation", "lexical", "fulltext diagnostic query representation: workload, lexical, lexical-required-entity, lexical-required-1, lexical-required-2 or original")
	diagnosticMSM := flag.String("diagnostic-msm", ydbstore.FulltextMinimumShouldMatch, "MinimumShouldMatch used by the fulltext latency diagnostic")
	diagnosticWarmup := flag.Bool("diagnostic-warmup", true, "warm each fulltext query before profiling it")
	demoOutput := flag.String("demo-output", ".runtime/demo-yql", "directory for generated browser-demo YQL files")
	demoQueryID := flag.String("demo-query-id", "0", "query id used by generated browser-demo YQL files")
	flag.Parse()
	mode := "run"
	if flag.NArg() > 0 {
		mode = flag.Arg(0)
	}
	fulltextQuality := model.FulltextConfig{
		Name: *qualityName, Table: *qualityTable, Index: *qualityIndex, Column: *qualityColumn, MinimumShouldMatch: *qualityMSM,
		QueryTransform: *qualityQueryTransform, K1: *qualityK1, B: *qualityB,
		FilterLengthMin: *qualityFilterLengthMin, FilterLengthMax: *qualityFilterLengthMax,
	}
	hybridQuality := model.HybridConfig{
		Name: *qualityName, Table: *qualityTable,
		FulltextIndex: *qualityIndex, FulltextColumn: *qualityColumn,
		VectorIndex: *qualityVectorIndex, VectorColumn: *qualityVectorColumn,
		MinimumShouldMatch: *qualityMSM, QueryTransform: *qualityQueryTransform,
		K1: *qualityK1, B: *qualityB,
		FulltextWeight: *qualityFulltextWeight, VectorWeight: *qualityVectorWeight,
		Mode: *qualityHybridMode, RRFK: *qualityRRFK, Normalize: *qualityLinearNormalize,
		BranchLimit: *qualityBranchLimit,
	}
	if err := execute(*configPath, *datasetConfig, *datasetProfile, *profile, mode, *drop, *batchSize, *waitTimeout, *partitionScope, *qualityOutput, *qualityWorkers, *qualityQueryLimit, *qualityTopK, *qualitySnowball, fulltextQuality, hybridQuality, *diagnosticOutput, *diagnosticWorkers, *diagnosticQueryLimit, *diagnosticQueryRepresentation, *diagnosticMSM, *diagnosticWarmup, *demoOutput, *demoQueryID); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func execute(path, datasetPath, datasetProfile, profile, mode string, drop bool, batchSize int, waitTimeout time.Duration, partitionScope, qualityOutput string, qualityWorkers, qualityQueryLimit, qualityTopK int, qualitySnowball bool, fulltextQuality model.FulltextConfig, hybridQuality model.HybridConfig, diagnosticOutput string, diagnosticWorkers, diagnosticQueryLimit int, diagnosticQueryRepresentation, diagnosticMSM string, diagnosticWarmup bool, demoOutput, demoQueryID string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	cfg, err = cfg.ApplyDatasetProfile(datasetPath, datasetProfile)
	if err != nil {
		return err
	}
	if datasetProfile != "" {
		fmt.Printf("dataset_profile=%s\n", datasetProfile)
	}
	cfg, err = cfg.ApplyProfile(profile)
	if err != nil {
		return err
	}
	if profile != "" {
		fmt.Printf("workload_profile=%s\n", profile)
	}
	if mode == "demo-scripts" {
		artifact, err := corpus.Load(cfg.QueryFile, cfg.VectorDimension)
		if err != nil {
			return err
		}
		selected, err := findQuery(artifact.Queries, demoQueryID)
		if err != nil {
			return err
		}
		parent := filepath.Dir(demoOutput)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create demo output parent: %w", err)
		}
		temporary, err := os.MkdirTemp(parent, ".demo-yql-")
		if err != nil {
			return fmt.Errorf("create temporary demo output directory: %w", err)
		}
		defer os.RemoveAll(temporary)
		for _, script := range ydbstore.DemoScripts(cfg, *selected) {
			output := filepath.Join(temporary, script.Name)
			if err := os.WriteFile(output, []byte(script.Content), 0o644); err != nil {
				return fmt.Errorf("write demo script %s: %w", output, err)
			}
		}
		if err := os.RemoveAll(demoOutput); err != nil {
			return fmt.Errorf("replace demo output directory: %w", err)
		}
		if err := os.Rename(temporary, demoOutput); err != nil {
			return fmt.Errorf("publish demo output directory: %w", err)
		}
		for _, script := range ydbstore.DemoScripts(cfg, *selected) {
			fmt.Printf("demo_script=%s\n", filepath.Join(demoOutput, script.Name))
		}
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := ydbstore.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}()
	switch mode {
	case "drop":
		return store.Drop(ctx)
	case "init":
		if drop {
			if err := store.Drop(ctx); err != nil {
				return err
			}
		}
		return store.Init(ctx)
	case "load":
		loaded := 0
		_, err := corpus.ReadDocuments(cfg.DocumentFile, batchSize, func(batch []model.Document) error {
			if err := store.BulkUpsert(ctx, batch); err != nil {
				return err
			}
			loaded += len(batch)
			if loaded%5000 == 0 {
				fmt.Printf("loaded=%d\n", loaded)
			}
			return nil
		})
		fmt.Printf("loaded_total=%d\n", loaded)
		return err
	case "clean-dml":
		if !cfg.DMLEnabled() {
			return fmt.Errorf("clean-dml requires dml.mode=verified")
		}
		if batchSize < 1 {
			return fmt.Errorf("batch-size must be positive")
		}
		end := cfg.DML.IDStart + cfg.DML.PoolSize
		for start := cfg.DML.IDStart; start < end; {
			batchEnd := min(start+uint64(batchSize), end)
			if err := store.DeleteDocumentsRange(ctx, start, batchEnd); err != nil {
				return fmt.Errorf("delete DML range [%d, %d): %w", start, batchEnd, err)
			}
			start = batchEnd
			if (start-cfg.DML.IDStart)%5000 == 0 || start == end {
				fmt.Printf("cleaned=%d\n", start-cfg.DML.IDStart)
			}
		}
		return nil
	case "indexes":
		return store.CreateIndexes(ctx)
	case "wait":
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		return store.WaitIndexes(waitCtx, 5*time.Second)
	case "reset-fulltext":
		if err := store.ResetFulltextIndex(ctx); err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		return store.WaitIndexes(waitCtx, 5*time.Second)
	case "reset-vector":
		if err := store.ResetVectorIndex(ctx); err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		return store.WaitIndexes(waitCtx, 5*time.Second)
	case "quality-index-create":
		if fulltextQuality.Index == "" {
			return fmt.Errorf("quality-index-create requires -quality-index")
		}
		if err := store.CreateFulltextVariantIndex(ctx, fulltextQuality, qualitySnowball); err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		return store.WaitIndex(waitCtx, fulltextQuality.Table, fulltextQuality.Index, 5*time.Second)
	case "quality-index-drop":
		if fulltextQuality.Index == "" {
			return fmt.Errorf("quality-index-drop requires -quality-index")
		}
		if fulltextQuality.Index == cfg.FulltextIndex {
			return fmt.Errorf("refusing to drop production index %q; use reset-fulltext explicitly", cfg.FulltextIndex)
		}
		return store.DropFulltextVariantIndex(ctx, fulltextQuality)
	case "partitions":
		stats, err := store.PartitionStats(ctx)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	case "count":
		count, err := store.CountDocuments(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("documents=%d\n", count)
		return nil
	case "version":
		version, err := store.ServerVersion(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("server_version=%s\n", version)
		return nil
	case "partition-fixed":
		return store.SetPartitioning(ctx, "fixed", partitionScope)
	case "partition-auto":
		return store.SetPartitioning(ctx, "auto", partitionScope)
	case "partition-elastic":
		return store.SetPartitioning(ctx, "elastic", partitionScope)
	case "probe-filtered":
		result, err := store.ProbeFilteredIndexes(ctx)
		if err != nil {
			return err
		}
		if result.FulltextPrefixSupported {
			fmt.Printf("filtered fulltext relevance index: OK match_ids=%v score_ids=%v\n", result.FulltextMatchIDs, result.FulltextScoreIDs)
		} else {
			fmt.Printf("filtered fulltext relevance index: UNSUPPORTED in this build (match_ids=%v score_supported=%t)\n", result.FulltextMatchIDs, result.FulltextScoreSupported)
		}
		fmt.Printf("filtered vector index: OK ids=%v\n", result.VectorIDs)
		return nil
	case "observe":
		return observer.Run(ctx, cfg, store)
	}
	artifact, err := corpus.Load(cfg.QueryFile, cfg.VectorDimension)
	if err != nil {
		return err
	}
	fmt.Printf("profile=%s queries=%d\n", artifact.Profile, len(artifact.Queries))
	if mode == "demo-check" {
		selected, err := findQuery(artifact.Queries, demoQueryID)
		if err != nil {
			return err
		}
		checks, err := store.CheckDemoQueries(ctx, *selected)
		for _, check := range checks {
			fmt.Printf("demo_query=%s rows=%d\n", check.Name, check.Rows)
		}
		return err
	}
	if mode == "diagnose-fulltext" {
		if diagnosticOutput == "" {
			return fmt.Errorf("diagnose-fulltext requires -diagnostic-output")
		}
		queries, err := sampledQueries(artifact.Queries, diagnosticQueryLimit)
		if err != nil {
			return err
		}
		queries, err = queriesForDiagnostic(queries, diagnosticQueryRepresentation)
		if err != nil {
			return err
		}
		documents, err := store.CountDocuments(ctx)
		if err != nil {
			return fmt.Errorf("count documents before fulltext diagnostic: %w", err)
		}
		partitionStats, err := store.PartitionStats(ctx)
		if err != nil {
			return fmt.Errorf("read partitions before fulltext diagnostic: %w", err)
		}
		fulltextPartitions := uint64(0)
		fulltextDocsSuffix := "/" + cfg.FulltextIndex + "/indexImplDocsTable"
		for _, stat := range partitionStats {
			if strings.HasSuffix(stat.Path, fulltextDocsSuffix) {
				fulltextPartitions = stat.Partitions
				break
			}
		}
		if fulltextPartitions == 0 {
			return fmt.Errorf("fulltext diagnostic could not find %s", fulltextDocsSuffix)
		}
		diagnosticConfig := model.FulltextConfig{
			Name:               "latency-" + diagnosticQueryRepresentation,
			Index:              cfg.FulltextIndex,
			Column:             "text",
			MinimumShouldMatch: diagnosticMSM,
			QueryTransform:     "lexical",
			K1:                 ydbstore.FulltextK1,
			B:                  ydbstore.FulltextB,
		}
		report, err := diagnostic.EvaluateFulltext(
			ctx, store, artifact.Profile, documents, fulltextPartitions, queries, 10, diagnosticWorkers, diagnosticWarmup, diagnosticConfig,
		)
		if err != nil {
			return err
		}
		report.QueryRepresentation = diagnosticQueryRepresentation
		if err := diagnostic.WriteReport(diagnosticOutput, report); err != nil {
			return fmt.Errorf("write fulltext diagnostic report: %w", err)
		}
		summary := struct {
			Output     string                        `json:"output"`
			Profile    string                        `json:"profile"`
			Documents  uint64                        `json:"documents"`
			Partitions uint64                        `json:"fulltext_docs_partitions"`
			Queries    int                           `json:"queries"`
			Summary    diagnostic.Summary            `json:"summary"`
			Slowest    []diagnostic.QueryObservation `json:"slowest"`
		}{diagnosticOutput, report.Profile, report.Documents, report.Partitions, report.QueryCount, report.Summary, report.Slowest}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	if mode == "quality" {
		if cfg.Quality.ExactFile == "" || cfg.Quality.ResultFile == "" {
			return fmt.Errorf("quality requires exact_file and result_file from the selected dataset profile")
		}
		exact, err := requireCleanDataset(ctx, store, cfg.Quality.ExactFile)
		if err != nil {
			return err
		}
		if exact.Profile != artifact.Profile {
			return fmt.Errorf("quality profile mismatch: queries=%s exact=%s", artifact.Profile, exact.Profile)
		}
		exactSHA, err := quality.FileSHA256(cfg.Quality.ExactFile)
		if err != nil {
			return fmt.Errorf("hash exact reference: %w", err)
		}
		report, err := quality.Evaluate(ctx, store, artifact.Queries, exact, cfg.QualityWorkers(), quality.IndexConfig{
			Levels: ydbstore.VectorIndexLevels, Clusters: ydbstore.VectorIndexClusters,
			OverlapClusters: ydbstore.VectorIndexOverlapClusters, SearchTopSize: cfg.KMeansSearchTopSize,
		}, exactSHA)
		if err != nil {
			return err
		}
		if err := quality.WriteReport(cfg.Quality.ResultFile, report); err != nil {
			return fmt.Errorf("write quality report: %w", err)
		}
		summary := struct {
			Output    string                              `json:"output"`
			Profile   string                              `json:"profile"`
			Queries   int                                 `json:"queries"`
			ANNRecall float64                             `json:"ann_recall"`
			Metrics   map[string]quality.RetrievalMetrics `json:"metrics"`
		}{cfg.Quality.ResultFile, report.Profile, report.QueryCount, report.ANNRecall, report.Metrics}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	if mode == "quality-fulltext" {
		if qualityOutput == "" {
			return fmt.Errorf("quality-fulltext requires -quality-output")
		}
		if fulltextQuality.Index == "" {
			fulltextQuality.Index = cfg.FulltextIndex
		}
		if qualityTopK < 1 {
			return fmt.Errorf("quality-top-k must be positive")
		}
		if _, err := requireCleanDataset(ctx, store, cfg.Quality.ExactFile); err != nil {
			return err
		}
		workers := cfg.QualityWorkers()
		if qualityWorkers > 0 {
			workers = qualityWorkers
		}
		queries, err := sampledQueries(artifact.Queries, qualityQueryLimit)
		if err != nil {
			return err
		}
		report, err := quality.EvaluateFulltext(ctx, store, queries, qualityTopK, workers, fulltextQuality)
		if err != nil {
			return err
		}
		report.Profile = artifact.Profile
		if err := quality.WriteFulltextReport(qualityOutput, report); err != nil {
			return fmt.Errorf("write fulltext quality report: %w", err)
		}
		summary := struct {
			Output   string                   `json:"output"`
			Config   model.FulltextConfig     `json:"config"`
			Metrics  quality.RetrievalMetrics `json:"metrics"`
			Failures int                      `json:"failures"`
		}{qualityOutput, report.Config, report.Metrics, report.Failures}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	if mode == "quality-hybrid" {
		if qualityOutput == "" {
			return fmt.Errorf("quality-hybrid requires -quality-output")
		}
		if hybridQuality.FulltextIndex == "" {
			hybridQuality.FulltextIndex = cfg.FulltextIndex
		}
		if hybridQuality.VectorIndex == "" {
			hybridQuality.VectorIndex = cfg.VectorIndex
		}
		if qualityTopK < 1 {
			return fmt.Errorf("quality-top-k must be positive")
		}
		if _, err := requireCleanDataset(ctx, store, cfg.Quality.ExactFile); err != nil {
			return err
		}
		workers := cfg.QualityWorkers()
		if qualityWorkers > 0 {
			workers = qualityWorkers
		}
		queries, err := sampledQueries(artifact.Queries, qualityQueryLimit)
		if err != nil {
			return err
		}
		report, err := quality.EvaluateHybrid(ctx, store, queries, qualityTopK, workers, hybridQuality)
		if err != nil {
			return err
		}
		report.Profile = artifact.Profile
		if err := quality.WriteHybridReport(qualityOutput, report); err != nil {
			return fmt.Errorf("write hybrid quality report: %w", err)
		}
		summary := struct {
			Output   string                   `json:"output"`
			Config   model.HybridConfig       `json:"config"`
			Metrics  quality.RetrievalMetrics `json:"metrics"`
			Failures int                      `json:"failures"`
		}{qualityOutput, report.Config, report.Metrics, report.Failures}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	if mode == "check" {
		return runChecks(ctx, store, artifact.Queries, os.Stdout)
	}
	if mode == "probe-hybrid" {
		if len(artifact.Queries) == 0 {
			return fmt.Errorf("query artifact is empty")
		}
		query := artifact.Queries[0]
		ids, retries, err := store.HybridDocIDs(ctx, query)
		if err != nil {
			return err
		}
		fmt.Printf("hybrid_full_query=OK qid=%s results=%d retries=%d\n", query.QID, len(ids), retries)
		return nil
	}
	if mode == "probe-search" {
		if len(artifact.Queries) == 0 {
			return fmt.Errorf("query artifact is empty")
		}
		report, probeErr := store.ProbeSearchCapabilities(ctx, artifact.Queries[0])
		fmt.Printf("server_version=%s\n", report.Version)
		for _, check := range report.Checks {
			status := "OK"
			detail := check.Detail
			if check.Err != nil {
				status = "FAIL"
				detail = check.Err.Error()
			}
			fmt.Printf("[%s] %s: %s\n", status, check.Name, detail)
		}
		return probeErr
	}
	if mode != "run" {
		return fmt.Errorf("unknown command %q", mode)
	}
	var writeDocuments []model.Document
	if cfg.DMLEnabled() {
		writeDocuments, err = corpus.LoadDocumentPool(cfg.DocumentFile, int(cfg.DML.PoolSize))
		if err != nil {
			return fmt.Errorf("load dml document pool: %w", err)
		}
		fmt.Printf("dml_documents=%d\n", len(writeDocuments))
	}
	return workload.New(cfg, store, artifact.Queries, writeDocuments).Run(ctx)
}

func findQuery(queries []model.Query, queryID string) (*model.Query, error) {
	for index := range queries {
		if queries[index].QID == queryID {
			return &queries[index], nil
		}
	}
	return nil, fmt.Errorf("demo query id %q not found", queryID)
}

func sampledQueries(queries []model.Query, limit int) ([]model.Query, error) {
	if limit < 0 {
		return nil, fmt.Errorf("quality-query-limit must not be negative")
	}
	if limit == 0 || limit >= len(queries) {
		return queries, nil
	}
	if limit < 1 {
		return nil, fmt.Errorf("quality-query-limit must be positive or zero")
	}
	result := make([]model.Query, limit)
	for position := range limit {
		result[position] = queries[position*len(queries)/limit]
	}
	return result, nil
}

func queriesForDiagnostic(queries []model.Query, representation string) ([]model.Query, error) {
	result := append([]model.Query(nil), queries...)
	for index := range result {
		queryText, err := ydbstore.SelectFulltextQuery(result[index], representation)
		if err != nil {
			return nil, err
		}
		result[index].LexicalQuery = queryText
	}
	return result, nil
}

func requireCleanDataset(ctx context.Context, store *ydbstore.Store, exactPath string) (quality.ExactArtifact, error) {
	if exactPath == "" {
		return quality.ExactArtifact{}, fmt.Errorf("quality evaluation requires exact_file from the selected dataset profile")
	}
	exact, err := quality.LoadExact(exactPath)
	if err != nil {
		return quality.ExactArtifact{}, fmt.Errorf("load exact reference: %w", err)
	}
	documents, err := store.CountDocuments(ctx)
	if err != nil {
		return quality.ExactArtifact{}, fmt.Errorf("count documents before quality evaluation: %w", err)
	}
	if documents != uint64(exact.Documents) {
		return quality.ExactArtifact{}, fmt.Errorf(
			"quality requires a clean dataset: table has %d rows, exact artifact has %d; run clean-dml or reactivate the dataset before evaluation",
			documents, exact.Documents,
		)
	}
	return exact, nil
}

func relevantHits(actual, relevant []string) int {
	wanted := make(map[string]struct{}, len(relevant))
	for _, id := range relevant {
		wanted[id] = struct{}{}
	}
	hits := 0
	for _, id := range actual {
		if _, ok := wanted[id]; ok {
			hits++
		}
	}
	return hits
}
