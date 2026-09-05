package observer

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/quality"
)

type Backend interface {
	PartitionStats(context.Context) ([]model.PartitionStat, error)
}

type Exporter struct {
	application string
	tablePath   string
	fulltext    string
	vector      string

	mu       sync.RWMutex
	up       bool
	observed bool
	values   []model.PartitionStat
	quality  *quality.Report

	server   *http.Server
	listener net.Listener
}

func NewExporter(cfg config.Config) (*Exporter, error) {
	listener, err := net.Listen("tcp", cfg.ObserverListenAddress())
	if err != nil {
		return nil, fmt.Errorf("listen observer metrics: %w", err)
	}
	exporter := &Exporter{
		application: cfg.Metrics.Application,
		tablePath:   strings.Trim(cfg.TablePath, "/"),
		fulltext:    cfg.FulltextIndex,
		vector:      cfg.VectorIndex,
		listener:    listener,
	}
	exporter.server = &http.Server{Handler: exporter, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = exporter.server.Serve(listener) }()
	fmt.Printf("observer metrics: http://%s/metrics\n", listener.Addr())
	return exporter, nil
}

func (e *Exporter) Write(values []model.PartitionStat, up bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.observed = true
	e.up = up
	e.values = e.values[:0]
	if up {
		e.values = append(e.values, values...)
	}
}

func (e *Exporter) WriteQuality(report quality.Report) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.quality = &report
}

func (e *Exporter) Close(ctx context.Context) error { return e.server.Shutdown(ctx) }

func (e *Exporter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.observed {
		return
	}
	writeMetrics(writer, e.application, e.tablePath, e.fulltext, e.vector, e.values, e.up)
	if e.quality != nil {
		writeQualityMetrics(writer, e.application, *e.quality)
	}
}

func Run(ctx context.Context, cfg config.Config, backend Backend) error {
	interval, _ := cfg.ObserverIntervalDuration()
	exporter, err := NewExporter(cfg)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exporter.Close(closeCtx)
	}()

	var qualityModTime time.Time
	observe := func() {
		values, queryErr := backend.PartitionStats(ctx)
		if queryErr != nil {
			exporter.Write(nil, false)
			log.Printf("partition observer: %v", queryErr)
			return
		}
		exporter.Write(values, true)
		if cfg.Quality.ResultFile != "" {
			info, statErr := os.Stat(cfg.Quality.ResultFile)
			if statErr == nil && info.ModTime() != qualityModTime {
				report, loadErr := quality.LoadReport(cfg.Quality.ResultFile)
				if loadErr != nil {
					log.Printf("quality report: %v", loadErr)
				} else {
					exporter.WriteQuality(report)
					qualityModTime = info.ModTime()
				}
			}
		}
	}
	observe()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			observe()
		case <-ctx.Done():
			return nil
		}
	}
}

func writeQualityMetrics(writer io.Writer, application string, report quality.Report) {
	base := fmt.Sprintf("application=\"%s\",profile=\"%s\"", escape(application), escape(report.Profile))
	_, _ = fmt.Fprintf(writer,
		"search_quality_ann_recall{%s,k=\"%d\"} %.9f\n"+
			"search_quality_evaluated_queries{%s} %d\n"+
			"search_quality_evaluated_timestamp_seconds{%s} %d\n"+
			"search_quality_evaluation_duration_seconds{%s} %.3f\n",
		base, report.TopK, report.ANNRecall,
		base, report.QueryCount,
		base, report.GeneratedAt.Unix(),
		base, float64(report.DurationMS)/1000,
	)
	for _, method := range quality.SortedMethods(report.Metrics) {
		metrics := report.Metrics[method]
		labels := fmt.Sprintf("%s,method=\"%s\"", base, escape(method))
		_, _ = fmt.Fprintf(writer,
			"search_quality_qrel_recall{%s,aggregation=\"micro\",k=\"%d\"} %.9f\n"+
				"search_quality_qrel_recall{%s,aggregation=\"macro\",k=\"%d\"} %.9f\n"+
				"search_quality_hit_rate{%s,k=\"%d\"} %.9f\n"+
				"search_quality_ndcg{%s,k=\"10\"} %.9f\n"+
				"search_quality_mrr{%s,k=\"10\"} %.9f\n",
			labels, report.TopK, metrics.QrelRecallMicro,
			labels, report.TopK, metrics.QrelRecallMacro,
			labels, report.TopK, metrics.HitRate,
			labels, metrics.NDCG,
			labels, metrics.MRR,
		)
	}
}

func writeMetrics(writer io.Writer, application, tablePath, fulltext, vector string, values []model.PartitionStat, up bool) {
	upValue := 0
	if up {
		upValue = 1
	}
	_, _ = fmt.Fprintf(writer, "ydb_partition_observer_up{application=\"%s\"} %d\n", escape(application), upValue)
	if !up {
		return
	}

	sort.Slice(values, func(i, j int) bool { return values[i].Path < values[j].Path })
	for _, value := range values {
		object, ok := objectForPath(value.Path, tablePath, fulltext, vector)
		if !ok {
			continue
		}
		labels := fmt.Sprintf("application=\"%s\",object=\"%s\",path=\"%s\"",
			escape(application), object, escape(value.Path))
		_, _ = fmt.Fprintf(writer,
			"ydb_partition_count{%s} %d\n"+
				"ydb_partition_rows{%s} %d\n"+
				"ydb_partition_size_bytes{%s} %d\n"+
				"ydb_partition_cpu_cores{%s} %.6f\n",
			labels, value.Partitions, labels, value.Rows, labels, value.Size, labels, value.CPU)
		for _, tablet := range value.Tablets {
			tabletLabels := fmt.Sprintf("application=\"%s\",object=\"%s\",path=\"%s\",tablet_id=\"%d\"",
				escape(application), object, escape(value.Path), tablet.ID)
			_, _ = fmt.Fprintf(writer, "ydb_tablet_cpu_cores{%s} %.6f\n", tabletLabels, tablet.CPU)
		}
	}
}

func objectForPath(value, tablePath, fulltext, vector string) (string, bool) {
	clean := strings.Trim(value, "/")
	if clean == tablePath || strings.HasSuffix(clean, "/"+tablePath) {
		return "main", true
	}
	if strings.Contains("/"+clean+"/", "/"+fulltext+"/") {
		return "fulltext", true
	}
	if strings.Contains("/"+clean+"/", "/"+vector+"/") {
		return "vector", true
	}
	return "", false
}

func escape(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}
