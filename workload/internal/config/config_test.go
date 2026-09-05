package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadResolvesQueryFileRelativeToConfig(t *testing.T) {
	t.Setenv("WORKLOAD_QUERY_FILE", "")
	t.Setenv("WORKLOAD_FULLTEXT_INDEX", "ft_override")
	t.Setenv("WORKLOAD_VECTOR_INDEX", "vec_override")
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "workload.json")
	data := []byte(`{
  "connection_string": "grpc://localhost:2136/local",
  "anonymous": true,
  "query_file": "../data/queries.json.gz",
  "document_file": "../data/documents.jsonl.gz",
  "table_path": "documents",
  "fulltext_index": "ft_idx",
  "vector_index": "vec_idx",
  "vector_dimension": 768,
  "kmeans_search_top_size": 32,
  "request_timeout": "15s",
  "admin_timeout": "2m",
  "metrics": {"interval": "1s"},
  "workload": {}
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "data", "queries.json.gz")
	if cfg.QueryFile != want {
		t.Fatalf("QueryFile = %q, want %q", cfg.QueryFile, want)
	}
	documentWant := filepath.Join(dir, "data", "documents.jsonl.gz")
	if cfg.DocumentFile != documentWant {
		t.Fatalf("DocumentFile = %q, want %q", cfg.DocumentFile, documentWant)
	}
	if cfg.FulltextIndex != "ft_override" || cfg.VectorIndex != "vec_override" {
		t.Fatalf("index environment overrides were not applied: fulltext=%q vector=%q", cfg.FulltextIndex, cfg.VectorIndex)
	}
}

func TestApplyProfileAndIndependentRamp(t *testing.T) {
	endFulltext := 400.0
	cfg := Config{
		ConnectionString:    "grpc://localhost:2136/local",
		QueryFile:           "queries.json.gz",
		DocumentFile:        "documents.jsonl.gz",
		TablePath:           "documents",
		FulltextIndex:       "ft_idx",
		VectorIndex:         "vec_idx",
		VectorDimension:     768,
		KMeansSearchTopSize: 32,
		RequestTimeout:      "15s",
		AdminTimeout:        "2m",
		Metrics:             Metrics{Interval: "1s"},
		Workload: Workload{
			Fulltext: Scenario{RPS: 10, Workers: 8},
			Vector:   Scenario{RPS: 10, Workers: 8},
			Hybrid:   Scenario{RPS: 10, Workers: 8},
		},
		Profiles: map[string]Workload{
			"fulltext-partition": {
				Fulltext: Scenario{RPS: 10, EndRPS: &endFulltext, Workers: 128},
				Vector:   Scenario{RPS: 10, Workers: 8},
				Hybrid:   Scenario{RPS: 10, Workers: 8},
				Ramp:     Ramp{Duration: "8m"},
			},
		},
	}

	profile, err := cfg.ApplyProfile("fulltext-partition")
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.TargetRPS("fulltext", 4*time.Minute); got != 205 {
		t.Fatalf("fulltext target = %v, want 205", got)
	}
	if got := profile.TargetRPS("vector", 4*time.Minute); got != 10 {
		t.Fatalf("vector target = %v, want stable background 10", got)
	}
	if got := profile.TargetRPS("hybrid", 10*time.Minute); got != 10 {
		t.Fatalf("hybrid target = %v, want stable background 10", got)
	}
}

func TestApplyProfileRejectsUnknownName(t *testing.T) {
	if _, err := (Config{}).ApplyProfile("missing"); err == nil {
		t.Fatal("unknown profile was accepted")
	}
}

func TestApplyDatasetProfileResolvesArtifactsRelativeToCatalog(t *testing.T) {
	dir := t.TempDir()
	catalog := filepath.Join(dir, "config", "datasets.json")
	if err := os.MkdirAll(filepath.Dir(catalog), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"profiles":{"scale-1m":{"size":1000000,"seed":42,"document_file":"../data/documents.jsonl.gz","query_file":"../data/queries.json.gz"}}}`)
	if err := os.WriteFile(catalog, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConnectionString: "grpc://localhost:2136/local", QueryFile: "old-q", DocumentFile: "old-d",
		TablePath: "documents", FulltextIndex: "ft", VectorIndex: "vec", VectorDimension: 768,
		KMeansSearchTopSize: 8, RequestTimeout: "15s", AdminTimeout: "2m",
		Metrics: Metrics{Interval: "1s"},
	}
	profile, err := cfg.ApplyDatasetProfile(catalog, "scale-1m")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "data", "documents.jsonl.gz"); profile.DocumentFile != want {
		t.Fatalf("DocumentFile = %q, want %q", profile.DocumentFile, want)
	}
	if want := filepath.Join(dir, "data", "queries.json.gz"); profile.QueryFile != want {
		t.Fatalf("QueryFile = %q, want %q", profile.QueryFile, want)
	}
}

func TestLoadUsesStableDefaultTablePath(t *testing.T) {
	t.Setenv("WORKLOAD_TABLE_PATH", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "workload.json")
	data := []byte(`{
  "connection_string": "grpc://localhost:2136/local",
  "anonymous": true,
  "query_file": "queries.json.gz",
  "document_file": "documents.jsonl.gz",
  "fulltext_index": "ft_idx",
  "vector_index": "vec_idx",
  "vector_dimension": 768,
  "kmeans_search_top_size": 32,
  "request_timeout": "15s",
  "admin_timeout": "2m",
  "metrics": {"interval": "1s"},
  "workload": {}
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TablePath != DefaultTablePath {
		t.Fatalf("TablePath = %q, want %q", cfg.TablePath, DefaultTablePath)
	}
}

func TestValidateRejectsPartitionMinimumAboveMaximum(t *testing.T) {
	for name, partitioning := range map[string]Partitioning{
		"base":     {BaseMinPartitions: 65},
		"fulltext": {FulltextMinPartitions: 65},
		"vector":   {VectorMinPartitions: 65},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{
				ConnectionString:    "grpc://localhost:2136/local",
				QueryFile:           "queries.json.gz",
				DocumentFile:        "documents.jsonl.gz",
				TablePath:           "documents",
				FulltextIndex:       "ft_idx",
				VectorIndex:         "vec_idx",
				VectorDimension:     768,
				KMeansSearchTopSize: 8,
				RequestTimeout:      "15s",
				AdminTimeout:        "2m",
				Metrics:             Metrics{Interval: "1s"},
				Partitioning:        partitioning,
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("partition minimum above maximum was accepted")
			}
		})
	}
}

func TestLoadSessionPoolSizeFromEnvironment(t *testing.T) {
	t.Setenv("WORKLOAD_SESSION_POOL_SIZE", "256")
	t.Setenv("WORKLOAD_SESSION_POOL_USAGE_LIMIT", "100")
	dir := t.TempDir()
	path := filepath.Join(dir, "workload.json")
	data := []byte(`{
  "connection_string": "grpc://localhost:2136/local",
  "anonymous": true,
  "query_file": "queries.json.gz",
  "document_file": "documents.jsonl.gz",
  "table_path": "documents",
  "fulltext_index": "ft_idx",
  "vector_index": "vec_idx",
  "vector_dimension": 768,
  "kmeans_search_top_size": 8,
  "request_timeout": "15s",
  "admin_timeout": "2m",
  "metrics": {"interval": "1s"},
  "workload": {}
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionPoolSize != 256 {
		t.Fatalf("SessionPoolSize = %d, want 256", cfg.SessionPoolSize)
	}
	if cfg.SessionPoolUsage != 100 {
		t.Fatalf("SessionPoolUsage = %d, want 100", cfg.SessionPoolUsage)
	}
}

func TestObserverDefaultsAndInterval(t *testing.T) {
	cfg := Config{}
	if got := cfg.ObserverListenAddress(); got != "0.0.0.0:9092" {
		t.Fatalf("ObserverListenAddress = %q, want %q", got, "0.0.0.0:9092")
	}
	if got, err := cfg.ObserverIntervalDuration(); err != nil || got != 5*time.Second {
		t.Fatalf("ObserverIntervalDuration = %v, %v, want 5s", got, err)
	}

	cfg.Observer = Observer{ListenAddress: "127.0.0.1:19092", Interval: "2s"}
	if got := cfg.ObserverListenAddress(); got != "127.0.0.1:19092" {
		t.Fatalf("ObserverListenAddress = %q", got)
	}
	if got, err := cfg.ObserverIntervalDuration(); err != nil || got != 2*time.Second {
		t.Fatalf("ObserverIntervalDuration = %v, %v, want 2s", got, err)
	}
}

func TestValidateVerifiedDML(t *testing.T) {
	cfg := Config{DML: DML{Mode: "verified", RPS: 10, Workers: 16, IDStart: 9_000_000_000, PoolSize: 50_000}}
	if err := cfg.validateDML(); err != nil {
		t.Fatalf("valid DML config rejected: %v", err)
	}
	cfg.DML.PoolSize = 0
	if err := cfg.validateDML(); err == nil {
		t.Fatal("zero DML pool was accepted")
	}
}
