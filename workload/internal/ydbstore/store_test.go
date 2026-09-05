package ydbstore

import (
	"strings"
	"testing"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

func TestTableDDLUsesNeutralDocumentsSchema(t *testing.T) {
	ddl := TableDDL(config.Config{
		TablePath: config.DefaultTablePath,
		Partitioning: config.Partitioning{
			BaseMinPartitions: 20,
		},
	})
	if !strings.Contains(ddl, "CREATE TABLE `deep_tech_search_documents`") || strings.Contains(ddl, "miracl") {
		t.Fatalf("unexpected DDL: %s", ddl)
	}
	if !strings.Contains(ddl, "AUTO_PARTITIONING_BY_SIZE = ENABLED") ||
		!strings.Contains(ddl, "AUTO_PARTITIONING_PARTITION_SIZE_MB = 2000") {
		t.Fatalf("table DDL must keep the ordinary 2 GB size policy: %s", ddl)
	}
	if !strings.Contains(ddl, "AUTO_PARTITIONING_BY_LOAD = ENABLED") ||
		!strings.Contains(ddl, "AUTO_PARTITIONING_MIN_PARTITIONS_COUNT = 20") {
		t.Fatalf("table DDL must keep load split enabled and preserve prepared capacity: %s", ddl)
	}
	if !strings.Contains(ddl, "text Utf8 NOT NULL") || strings.Contains(ddl, "body Utf8") || strings.Contains(ddl, "search_text") {
		t.Fatalf("table DDL must store one combined text column: %s", ddl)
	}
}

func TestFulltextDDLUsesRussianDocumentText(t *testing.T) {
	ddl := FulltextDDL(config.Config{TablePath: "documents", FulltextIndex: "search"})
	for _, want := range []string{
		"ON (text)",
		"use_filter_lowercase = true",
		"use_filter_snowball = true",
		`language = "russian"`,
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("fulltext DDL does not contain %q: %s", want, ddl)
		}
	}
}

func TestDropFulltextDDLUsesConfiguredNames(t *testing.T) {
	ddl := DropFulltextDDL(config.Config{TablePath: "documents", FulltextIndex: "search"})
	if ddl != "ALTER TABLE `documents` DROP INDEX `search`;" {
		t.Fatalf("unexpected DDL: %s", ddl)
	}
}

func TestVectorDDLUsesCalibratedTree(t *testing.T) {
	ddl := VectorDDL(config.Config{
		TablePath:       "documents",
		VectorIndex:     "vectors",
		VectorDimension: 768,
	})
	for _, want := range []string{
		"clusters = 128",
		"overlap_clusters = 3",
		"vector_dimension = 768",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("vector DDL does not contain %q: %s", want, ddl)
		}
	}
}

func TestDropVectorDDLUsesConfiguredNames(t *testing.T) {
	ddl := DropVectorDDL(config.Config{TablePath: "documents", VectorIndex: "vectors"})
	if ddl != "ALTER TABLE `documents` DROP INDEX `vectors`;" {
		t.Fatalf("unexpected DDL: %s", ddl)
	}
}

func TestPartitionTargets(t *testing.T) {
	targets, err := partitionTargets("documents", "ft_idx", "vec_idx", "vector-level")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != "documents/vec_idx/indexImplLevelTable" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestPartitioningDDLPreservesMinimumPartitionCount(t *testing.T) {
	ddl := partitioningDDL("documents/indexImplTable", "DISABLED", 11)
	if !strings.Contains(ddl, "AUTO_PARTITIONING_MIN_PARTITIONS_COUNT = 11") {
		t.Fatalf("fixed mode must pin the current partition count: %s", ddl)
	}
	if !strings.Contains(ddl, "AUTO_PARTITIONING_BY_LOAD = DISABLED") ||
		!strings.Contains(ddl, "ALTER TABLE `documents/indexImplTable`") {
		t.Fatalf("unexpected partitioning DDL: %s", ddl)
	}
	if !strings.Contains(ddl, "AUTO_PARTITIONING_BY_SIZE = ENABLED") ||
		!strings.Contains(ddl, "AUTO_PARTITIONING_PARTITION_SIZE_MB = 2000") {
		t.Fatalf("partition mode switch must keep the ordinary 2 GB size policy: %s", ddl)
	}
}

func TestAutoPartitioningDDLLeavesMinimumUnchanged(t *testing.T) {
	ddl := partitioningDDL("documents/indexImplTable", "ENABLED", 0)
	if strings.Contains(ddl, "AUTO_PARTITIONING_MIN_PARTITIONS_COUNT") {
		t.Fatalf("auto mode must keep the previously pinned minimum: %s", ddl)
	}
}

func TestElasticMinimumUsesConfiguredCapacityOnlyForHotTargets(t *testing.T) {
	cfg := config.Config{
		TablePath:     "documents",
		FulltextIndex: "ft_idx",
		VectorIndex:   "vec_idx",
		Partitioning: config.Partitioning{
			BaseMinPartitions:     20,
			FulltextMinPartitions: 16,
			VectorMinPartitions:   4,
		},
	}
	tests := []struct {
		target  string
		current uint64
		want    uint64
	}{
		{"documents", 15, 20},
		{"documents", 24, 24},
		{"documents/ft_idx/indexImplDocsTable", 11, 16},
		{"documents/ft_idx/indexImplTable", 3, 3},
		{"documents/ft_idx/indexImplDictTable", 2, 2},
		{"documents/vec_idx/indexImplPostingTable", 3, 4},
		{"documents/vec_idx/indexImplPostingTable", 6, 6},
		{"documents/vec_idx/indexImplLevelTable", 1, 4},
		{"documents/vec_idx/indexImplLevelTable", 6, 6},
	}
	for _, tc := range tests {
		if got := elasticMinimum(cfg, tc.target, tc.current); got != tc.want {
			t.Errorf("elasticMinimum(%q, %d) = %d, want %d", tc.target, tc.current, got, tc.want)
		}
	}
}

func TestTransformFulltextQuery(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		transform string
		want      string
	}{
		{"unchanged", "Когда начался кризис?", "", "Когда начался кризис?"},
		{"stress", "Кари́бский и край", "strip-stress", "Карибский и край"},
		{"question words", "В каком году и где начался кризис?", "ru-question-words", "В году и начался кризис"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transformFulltextQuery(tc.value, tc.transform)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectFulltextQuery(t *testing.T) {
	query := model.Query{Text: "Кари́бский кризис?", LexicalQuery: "Карибский кризис", FulltextQuery: "Карибский кризис 1962"}
	tests := []struct {
		transform string
		want      string
	}{
		{"workload", "Карибский кризис 1962"},
		{"lexical", "Карибский кризис"},
		{"lexical-required-entity", "Карибский кризис"},
		{"lexical-required-1", "+Карибский кризис"},
		{"lexical-required-2", "+Карибский +кризис"},
		{"original", "Карибский кризис?"},
		{"strip-stress", "Карибский кризис?"},
	}
	for _, tc := range tests {
		got, err := selectFulltextQuery(query, tc.transform)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("selectFulltextQuery(%q) = %q, want %q", tc.transform, got, tc.want)
		}
	}
}

func TestRequireEntityOrYearTerm(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"entity", "городе родился Моцарт", "городе родился +Моцарт"},
		{"longest entity", "закончилась Первая Мировая война", "закончилась Первая +Мировая война"},
		{"year", "Аполлон прилетел 1971", "Аполлон прилетел +1971"},
		{"single digit is weak", "полк 1 батареи", "полк 1 батареи"},
		{"sentence start is weak", "Первый салют освобождения города", "Первый салют освобождения города"},
		{"no anchor", "первым русским философом", "первым русским философом"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := requireEntityOrYearTerm(tc.value); got != tc.want {
				t.Fatalf("requireEntityOrYearTerm(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestRequireDiscriminativeTerms(t *testing.T) {
	tests := []struct {
		name  string
		value string
		count int
		want  string
	}{
		{"proper name", "начался Карибский кризис", 1, "начался +Карибский кризис"},
		{"number first", "Аполлон прилетел 1971", 1, "Аполлон прилетел +1971"},
		{"two anchors", "Русь свергла татаро монгольское иго", 2, "+Русь свергла татаро +монгольское иго"},
		{"length tie", "длинный коротко", 1, "+длинный коротко"},
		{"empty", "", 1, ""},
		{"disabled", "один два", 0, "один два"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := requireDiscriminativeTerms(tc.value, tc.count); got != tc.want {
				t.Fatalf("requireDiscriminativeTerms(%q, %d) = %q, want %q", tc.value, tc.count, got, tc.want)
			}
		})
	}
}

func TestProductionFulltextScoreContainsCalibratedParameters(t *testing.T) {
	score := productionFulltextScore("$query")
	for _, want := range []string{`"50%" AS MinimumShouldMatch`, "1.0 AS K1", "0.25 AS B"} {
		if !strings.Contains(score, want) {
			t.Fatalf("production score does not contain %q: %s", want, score)
		}
	}
}

func TestWorkloadFulltextScoreContainsKeywordParameters(t *testing.T) {
	score := workloadFulltextScore("$query")
	for _, want := range []string{`"100%" AS MinimumShouldMatch`, "1.0 AS K1", "0.25 AS B"} {
		if !strings.Contains(score, want) {
			t.Fatalf("workload score does not contain %q: %s", want, score)
		}
	}
}

func TestProductionHybridUsesCalibratedLinearFusion(t *testing.T) {
	if HybridMode != "linear" || !HybridNormalize || HybridFulltextWeight != 0.075 || HybridVectorWeight != 1 ||
		OnlineVectorLimit != 10 || OnlineHybridBranchLimit != 10 || HybridBranchLimit != 30 {
		t.Fatalf(
			"unexpected search calibration: mode=%s normalize=%t weights=%g/%g online=%d/%d quality=%d",
			HybridMode, HybridNormalize, HybridFulltextWeight, HybridVectorWeight,
			OnlineVectorLimit, OnlineHybridBranchLimit, HybridBranchLimit,
		)
	}
}
