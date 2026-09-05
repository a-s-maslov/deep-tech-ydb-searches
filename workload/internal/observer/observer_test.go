package observer

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/quality"
)

func TestExporterClassifiesAndExportsObjects(t *testing.T) {
	exporter := &Exporter{
		application: "demo",
		tablePath:   "deep_tech_search_documents",
		fulltext:    "ft_idx",
		vector:      "vec_idx",
		observed:    true,
		up:          true,
		values: []model.PartitionStat{
			{Path: "/Root/database/deep_tech_search_documents", Partitions: 2, Rows: 10, Size: 100},
			{Path: "/Root/database/deep_tech_search_documents/ft_idx/indexImplTable", Partitions: 3, Rows: 20, Size: 200},
			{Path: "/Root/database/deep_tech_search_documents/vec_idx/indexImplPostingTable", Partitions: 4, Rows: 30, Size: 300, Tablets: []model.TabletStat{{ID: 101, CPU: .25}}},
		},
	}
	recorder := httptest.NewRecorder()
	exporter.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`ydb_partition_observer_up{application="demo"} 1`,
		`ydb_partition_count{application="demo",object="main",path="/Root/database/deep_tech_search_documents"} 2`,
		`ydb_partition_rows{application="demo",object="fulltext",path="/Root/database/deep_tech_search_documents/ft_idx/indexImplTable"} 20`,
		`ydb_tablet_cpu_cores{application="demo",object="vector",path="/Root/database/deep_tech_search_documents/vec_idx/indexImplPostingTable",tablet_id="101"} 0.250000`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metric %q is absent: %s", expected, body)
		}
	}
}

func TestExporterIncludesLastQualityReport(t *testing.T) {
	exporter := &Exporter{application: "demo", observed: true, up: true}
	exporter.WriteQuality(quality.Report{
		FormatVersion: 1,
		GeneratedAt:   time.Unix(1234, 0),
		DurationMS:    2500,
		Profile:       "scale-1m",
		QueryCount:    1252,
		TopK:          30,
		ANNRecall:     0.8125,
		Metrics: map[string]quality.RetrievalMetrics{
			"vector": {QrelRecallMicro: 0.835, QrelRecallMacro: 0.8, HitRate: 0.9, NDCG: 0.7, MRR: 0.6},
		},
	})
	recorder := httptest.NewRecorder()
	exporter.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`search_quality_ann_recall{application="demo",profile="scale-1m",k="30"} 0.812500000`,
		`search_quality_qrel_recall{application="demo",profile="scale-1m",method="vector",aggregation="micro",k="30"} 0.835000000`,
		`search_quality_evaluated_queries{application="demo",profile="scale-1m"} 1252`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("quality metric %q is absent: %s", expected, body)
		}
	}
}

func TestExporterDropsStaleValuesWhenObservationFails(t *testing.T) {
	exporter := &Exporter{application: "demo", observed: true}
	recorder := httptest.NewRecorder()
	exporter.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `ydb_partition_observer_up{application="demo"} 0`) {
		t.Fatalf("failed observer status is absent: %s", body)
	}
	if strings.Contains(body, "ydb_partition_count") {
		t.Fatalf("failed observation must not export stale partition values: %s", body)
	}
}

func TestObjectForPath(t *testing.T) {
	for path, expected := range map[string]string{
		"/local/deep_tech_search_documents":                               "main",
		"/local/deep_tech_search_documents/ft_idx/indexImplTable":         "fulltext",
		"/local/deep_tech_search_documents/vec_idx/indexImplPostingTable": "vector",
	} {
		actual, ok := objectForPath(path, "deep_tech_search_documents", "ft_idx", "vec_idx")
		if !ok || actual != expected {
			t.Fatalf("objectForPath(%q) = %q, %t; want %q, true", path, actual, ok, expected)
		}
	}
}
