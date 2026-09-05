package model

import (
	"strings"
	"time"
)

type Query struct {
	QID            string   `json:"qid"`
	Text           string   `json:"text"`
	LexicalQuery   string   `json:"lexical_query,omitempty"`
	FulltextQuery  string   `json:"fulltext_query,omitempty"`
	Embedding      []byte   `json:"embedding"`
	RelevantDocIDs []string `json:"relevant_docids"`
}

// FulltextWorkloadText returns the compact keyword representation used by the
// standalone full-text load. Older artifacts fall back to lexical_query.
func (q Query) FulltextWorkloadText() string {
	if strings.TrimSpace(q.FulltextQuery) != "" {
		return q.FulltextQuery
	}
	return q.FulltextText()
}

// FulltextText returns the query-side lexical representation. Older format-2
// artifacts did not contain it, so falling back to Text keeps them usable.
func (q Query) FulltextText() string {
	if strings.TrimSpace(q.LexicalQuery) != "" {
		return q.LexicalQuery
	}
	return q.Text
}

// FulltextConfig identifies one explicitly configured offline retrieval run.
// It is separate from the live workload configuration on purpose.
type FulltextConfig struct {
	Name               string  `json:"name"`
	Table              string  `json:"table,omitempty"`
	Index              string  `json:"index"`
	Column             string  `json:"column"`
	MinimumShouldMatch string  `json:"minimum_should_match"`
	QueryTransform     string  `json:"query_transform,omitempty"`
	K1                 float64 `json:"k1,omitempty"`
	B                  float64 `json:"b,omitempty"`
	FilterLengthMin    int     `json:"filter_length_min,omitempty"`
	FilterLengthMax    int     `json:"filter_length_max,omitempty"`
}

// HybridConfig describes one offline HybridRank calibration run.
type HybridConfig struct {
	Name               string  `json:"name"`
	Table              string  `json:"table,omitempty"`
	FulltextIndex      string  `json:"fulltext_index"`
	FulltextColumn     string  `json:"fulltext_column"`
	VectorIndex        string  `json:"vector_index"`
	VectorColumn       string  `json:"vector_column"`
	MinimumShouldMatch string  `json:"minimum_should_match"`
	QueryTransform     string  `json:"query_transform,omitempty"`
	K1                 float64 `json:"k1,omitempty"`
	B                  float64 `json:"b,omitempty"`
	FulltextWeight     float64 `json:"fulltext_weight"`
	VectorWeight       float64 `json:"vector_weight"`
	Mode               string  `json:"mode"`
	RRFK               float64 `json:"rrf_k"`
	Normalize          bool    `json:"normalize,omitempty"`
	BranchLimit        int     `json:"branch_limit"`
}

type Document struct {
	ID        uint64 `json:"id"`
	DocID     string `json:"docid"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Embedding []byte `json:"embedding"`
}

// PartitionStat aggregates rows from YDB's .sys/partition_stats by physical
// table path. It is shared by administrative commands and the observer.
type PartitionStat struct {
	Path       string       `json:"path"`
	Partitions uint64       `json:"partitions"`
	Rows       uint64       `json:"rows"`
	Size       uint64       `json:"size"`
	CPU        float64      `json:"cpu"`
	Tablets    []TabletStat `json:"tablets,omitempty"`
}

// TabletStat keeps the per-tablet CPU value behind an aggregated path.
type TabletStat struct {
	ID  uint64  `json:"id"`
	CPU float64 `json:"cpu"`
}

// QueryTableAccess is the storage work reported by YDB for one physical table.
// Rows is not a logical result count: it is the number of rows read internally
// while executing the query.
type QueryTableAccess struct {
	Name  string `json:"name"`
	Rows  uint64 `json:"rows"`
	Bytes uint64 `json:"bytes"`
}

// QueryExecutionStats contains one profiled execution of a production query.
// ClientDuration includes retries and transport overhead; the remaining values
// come from YDB query statistics for the successful attempt.
type QueryExecutionStats struct {
	ClientDuration     time.Duration      `json:"-"`
	ServerDuration     time.Duration      `json:"-"`
	CPUTime            time.Duration      `json:"-"`
	Retries            int                `json:"retries"`
	ResultCount        int                `json:"result_count"`
	ReadRows           uint64             `json:"read_rows"`
	ReadBytes          uint64             `json:"read_bytes"`
	ScoredDocumentRows uint64             `json:"scored_document_rows"`
	TableAccesses      []QueryTableAccess `json:"table_accesses,omitempty"`
}
