package ydbstore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb_Table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/result/named"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/types"
)

const filteredProbeTableName = "deep_tech_search_filter_probe"

type FilteredProbeResult struct {
	FulltextMatchIDs        []uint64
	FulltextScoreIDs        []uint64
	FulltextScoreSupported  bool
	FulltextPrefixSupported bool
	VectorIDs               []uint64
}

func (s *Store) ProbeFilteredIndexes(ctx context.Context) (result FilteredProbeResult, err error) {
	probePath := path.Join(path.Dir(s.tablePath), filteredProbeTableName)
	dropDDL := fmt.Sprintf("DROP TABLE IF EXISTS %s;", quote(probePath))
	if err := s.ExecScheme(ctx, dropDDL); err != nil {
		return result, fmt.Errorf("remove stale filtered-index probe table: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.adminTimeout)
		defer cancel()
		if cleanupErr := s.ExecScheme(cleanupCtx, dropDDL); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove filtered-index probe table: %w", cleanupErr))
		}
	}()

	createTable := fmt.Sprintf(`CREATE TABLE %s (
    id Uint64 NOT NULL,
	tenant_id Uint64 NOT NULL,
    body Utf8 NOT NULL,
    embedding String NOT NULL,
    PRIMARY KEY (id)
);`, quote(probePath))
	if err := s.ExecScheme(ctx, createTable); err != nil {
		return result, fmt.Errorf("create filtered-index probe table: %w", err)
	}
	if err := s.bulkUpsertFilteredProbe(ctx, probePath); err != nil {
		return result, fmt.Errorf("load filtered-index probe data: %w", err)
	}

	fulltextDDL := fmt.Sprintf(`ALTER TABLE %s
ADD INDEX ft_filtered GLOBAL USING fulltext_relevance
ON (tenant_id, body)
WITH (tokenizer = standard, use_filter_lowercase = true);`, quote(probePath))
	if err := s.ExecScheme(ctx, fulltextDDL); err != nil {
		return result, fmt.Errorf("create filtered fulltext index: %w", err)
	}
	vectorDDL := fmt.Sprintf(`ALTER TABLE %s
ADD INDEX vec_filtered GLOBAL SYNC USING vector_kmeans_tree
ON (tenant_id, embedding) COVER (body)
WITH (
    similarity = "inner_product",
    vector_type = "float",
    vector_dimension = 2,
    levels = 1,
    clusters = 2
);`, quote(probePath))
	if err := s.ExecScheme(ctx, vectorDDL); err != nil {
		return result, fmt.Errorf("create filtered vector index: %w", err)
	}
	if err := s.waitProbeIndexes(ctx, probePath); err != nil {
		return result, err
	}

	result.FulltextMatchIDs, err = s.filteredFulltextMatchProbe(ctx, probePath)
	if err != nil && !isUnsupportedFulltextPrefixError(err) {
		return result, fmt.Errorf("query filtered fulltext index with FulltextMatch: %w", err)
	}
	err = nil
	if len(result.FulltextMatchIDs) > 0 && !sameIDs(result.FulltextMatchIDs, []uint64{1}) {
		return result, fmt.Errorf("unexpected filtered fulltext match ids: %v", result.FulltextMatchIDs)
	}

	// Keep the vector capability check independent from the known relevance
	// prefix limitation below. Tenant leakage must fail the probe.
	result.VectorIDs, err = s.filteredVectorProbe(ctx, probePath)
	if err != nil {
		return result, fmt.Errorf("query filtered vector index: %w", err)
	}
	if !sameIDs(result.VectorIDs, []uint64{1, 2}) {
		return result, fmt.Errorf("unexpected filtered vector ids: %v", result.VectorIDs)
	}

	result.FulltextScoreIDs, err = s.filteredFulltextScoreProbe(ctx, probePath)
	if err == nil {
		result.FulltextScoreSupported = true
	} else if isUnsupportedFulltextPrefixError(err) {
		result.FulltextScoreSupported = false
		err = nil
	} else {
		return result, fmt.Errorf("query filtered fulltext index with FulltextScore: %w", err)
	}
	if result.FulltextScoreSupported && !sameIDs(result.FulltextScoreIDs, []uint64{1}) {
		return result, fmt.Errorf("unexpected filtered fulltext score ids: %v", result.FulltextScoreIDs)
	}
	result.FulltextPrefixSupported = sameIDs(result.FulltextMatchIDs, []uint64{1}) &&
		result.FulltextScoreSupported && sameIDs(result.FulltextScoreIDs, []uint64{1})
	return result, nil
}

func isUnsupportedFulltextPrefixError(err error) bool {
	return err != nil && strings.Contains(
		strings.ToLower(err.Error()),
		"unsupported predicate is used to access index",
	)
}

func (s *Store) bulkUpsertFilteredProbe(ctx context.Context, tablePath string) error {
	type row struct {
		id        uint64
		tenantID  uint64
		body      string
		embedding []float32
	}
	rows := []row{
		{1, 1, "сервис отвечает с таймаутом", []float32{1, 0}},
		{2, 1, "ошибка авторизации пользователя", []float32{0.8, 0.2}},
		{3, 2, "платёжный сервис отвечает с таймаутом", []float32{1, 0}},
		{4, 2, "ошибка списания средств", []float32{0.9, 0.1}},
	}
	values := make([]types.Value, 0, len(rows))
	for _, item := range rows {
		values = append(values, types.StructValue(
			types.StructFieldValue("id", types.Uint64Value(item.id)),
			types.StructFieldValue("tenant_id", types.Uint64Value(item.tenantID)),
			types.StructFieldValue("body", types.UTF8Value(item.body)),
			types.StructFieldValue("embedding", types.BytesValue(float32Vector(item.embedding))),
		))
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	return s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		return session.BulkUpsert(ctx, tablePath, types.ListValue(values...))
	}, table.WithIdempotent())
}

func (s *Store) waitProbeIndexes(ctx context.Context, tablePath string) error {
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		description, err := s.db.Table().DescribeTable(requestCtx, tablePath)
		if err != nil {
			return fmt.Errorf("describe filtered-index probe table: %w", err)
		}
		ready := map[string]bool{"ft_filtered": false, "vec_filtered": false}
		for _, index := range description.Indexes {
			if _, ok := ready[index.Name]; ok {
				ready[index.Name] = index.Status == Ydb_Table.TableIndexDescription_STATUS_READY
			}
		}
		if ready["ft_filtered"] && ready["vec_filtered"] {
			return nil
		}
		select {
		case <-requestCtx.Done():
			return fmt.Errorf("wait for filtered indexes: %w", requestCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) filteredFulltextMatchProbe(ctx context.Context, tablePath string) ([]uint64, error) {
	yql := fmt.Sprintf(`DECLARE $tenant_id AS Uint64;
DECLARE $query AS Utf8;
SELECT id FROM %s VIEW ft_filtered
WHERE tenant_id = $tenant_id AND FulltextMatch(body, $query)
ORDER BY id
LIMIT 10;`, quote(tablePath))
	return s.selectUint64IDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$tenant_id", types.Uint64Value(1)),
		// The probe deliberately has no stemming. Use the exact normalized term
		// from the document; otherwise an empty result only proves morphology,
		// not whether a filtered index is supported.
		table.ValueParam("$query", types.UTF8Value("таймаутом")),
	))
}

func (s *Store) filteredFulltextScoreProbe(ctx context.Context, tablePath string) ([]uint64, error) {
	yql := fmt.Sprintf(`DECLARE $tenant_id AS Uint64;
DECLARE $query AS Utf8;
SELECT id, FulltextScore(body, $query) AS relevance FROM %s VIEW ft_filtered
WHERE tenant_id = $tenant_id AND FulltextScore(body, $query) > 0
ORDER BY relevance DESC
LIMIT 10;`, quote(tablePath))
	return s.selectUint64IDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$tenant_id", types.Uint64Value(1)),
		table.ValueParam("$query", types.UTF8Value("таймаутом")),
	))
}

func (s *Store) filteredVectorProbe(ctx context.Context, tablePath string) ([]uint64, error) {
	yql := fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "2";
DECLARE $tenant_id AS Uint64;
DECLARE $target AS String;
SELECT id FROM %s VIEW vec_filtered
WHERE tenant_id = $tenant_id
ORDER BY Knn::InnerProductSimilarity(embedding, $target) DESC
LIMIT 2;`, quote(tablePath))
	return s.selectUint64IDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$tenant_id", types.Uint64Value(1)),
		table.ValueParam("$target", types.BytesValue(float32Vector([]float32{1, 0}))),
	))
}

func (s *Store) selectUint64IDs(ctx context.Context, yql string, params *table.QueryParameters) ([]uint64, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	ids := make([]uint64, 0, 10)
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		ids = ids[:0]
		_, result, err := session.Execute(ctx, table.OnlineReadOnlyTxControl(), yql, params)
		if err != nil {
			return err
		}
		defer result.Close()
		if err := result.NextResultSetErr(ctx); err != nil {
			return err
		}
		for result.NextRow() {
			var id uint64
			if err := result.ScanNamed(named.Required("id", &id)); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return result.Err()
	}, table.WithIdempotent())
	return ids, err
}

func float32Vector(values []float32) []byte {
	encoded := make([]byte, 4*len(values)+1)
	for index, value := range values {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(value))
	}
	// YDB FloatVector stores the format marker after the float32 payload.
	encoded[len(encoded)-1] = 1
	return encoded
}

func sameIDs(actual, expected []uint64) bool {
	actualCopy := append([]uint64(nil), actual...)
	expectedCopy := append([]uint64(nil), expected...)
	sort.Slice(actualCopy, func(i, j int) bool { return actualCopy[i] < actualCopy[j] })
	sort.Slice(expectedCopy, func(i, j int) bool { return expectedCopy[i] < expectedCopy[j] })
	if len(actualCopy) != len(expectedCopy) {
		return false
	}
	for index := range actualCopy {
		if actualCopy[index] != expectedCopy[index] {
			return false
		}
	}
	return true
}
