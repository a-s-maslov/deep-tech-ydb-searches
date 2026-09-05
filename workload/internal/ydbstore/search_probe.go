package ydbstore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb_Table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/types"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

const searchProbeTableName = "deep_tech_search_capability_probe"
const searchTextDMLProbeTableName = "deep_tech_search_text_dml_probe"
const searchDMLProbeTableName = "deep_tech_search_dml_probe"

type SearchCapabilityCheck struct {
	Name   string
	Detail string
	Err    error
}

type SearchCapabilityReport struct {
	Version string
	Checks  []SearchCapabilityCheck
}

func (r SearchCapabilityReport) Error() error {
	var result error
	for _, check := range r.Checks {
		if check.Err != nil {
			result = errors.Join(result, fmt.Errorf("%s: %w", check.Name, check.Err))
		}
	}
	return result
}

func (s *Store) ProbeSearchCapabilities(ctx context.Context, query model.Query) (report SearchCapabilityReport, err error) {
	report.Version, err = s.ServerVersion(ctx)
	if err != nil {
		return report, fmt.Errorf("read server version: %w", err)
	}

	probePath := path.Join(path.Dir(s.tablePath), searchProbeTableName)
	dropDDL := fmt.Sprintf("DROP TABLE IF EXISTS %s;", quote(probePath))
	if err := s.ExecScheme(ctx, dropDDL); err != nil {
		return report, fmt.Errorf("remove stale search probe table: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.adminTimeout)
		defer cancel()
		if cleanupErr := s.ExecScheme(cleanupCtx, dropDDL); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove search probe table: %w", cleanupErr))
		}
	}()

	if err := s.createSearchProbe(ctx, probePath); err != nil {
		return report, err
	}
	s.runFulltextCapabilityChecks(ctx, probePath, &report)
	s.runHybridCapabilityChecks(ctx, query, &report)
	return report, report.Error()
}

func (s *Store) createSearchProbe(ctx context.Context, probePath string) error {
	createTable := fmt.Sprintf(`CREATE TABLE %s (
    id Uint64 NOT NULL,
    tenant_id Uint64 NOT NULL,
    title Utf8 NOT NULL,
    body Utf8 NOT NULL,
    embedding String NOT NULL,
    PRIMARY KEY (id)
);`, quote(probePath))
	if err := s.ExecScheme(ctx, createTable); err != nil {
		return fmt.Errorf("create search probe table: %w", err)
	}
	if err := s.bulkUpsertSearchProbe(ctx, probePath); err != nil {
		return fmt.Errorf("load search probe data: %w", err)
	}

	indexes := []struct {
		name string
		ddl  string
	}{
		{"ft_plain", fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_plain GLOBAL USING fulltext_plain
ON (body) WITH (tokenizer=standard, use_filter_lowercase=true);`, quote(probePath))},
		{"ft_rel", fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_rel GLOBAL USING fulltext_relevance
ON (body) COVER (title, tenant_id) WITH (tokenizer=standard, use_filter_lowercase=true);`, quote(probePath))},
		{"ft_stem", fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_stem GLOBAL USING fulltext_plain
ON (body) WITH (tokenizer=standard, use_filter_lowercase=true, use_filter_snowball=true, language="russian");`, quote(probePath))},
		{"ft_ngram", fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_ngram GLOBAL USING fulltext_plain
ON (body) WITH (tokenizer=standard, use_filter_lowercase=true, use_filter_ngram=true,
filter_ngram_min_length=3, filter_ngram_max_length=6);`, quote(probePath))},
		{"ft_filtered", fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_filtered GLOBAL USING fulltext_relevance
ON (tenant_id, body) WITH (tokenizer=standard, use_filter_lowercase=true);`, quote(probePath))},
	}
	for _, index := range indexes {
		if err := s.ExecScheme(ctx, index.ddl); err != nil {
			return fmt.Errorf("create search probe index %s: %w", index.name, err)
		}
	}
	if err := s.waitNamedIndexes(ctx, probePath, []string{"ft_plain", "ft_rel", "ft_stem", "ft_ngram", "ft_filtered"}); err != nil {
		return err
	}
	return nil
}

func (s *Store) bulkUpsertSearchProbe(ctx context.Context, tablePath string) error {
	type row struct {
		id        uint64
		tenantID  uint64
		title     string
		body      string
		embedding []float32
	}
	rows := []row{
		{1, 1, "Таймаут", "Сервис отвечает с таймаутом подключения", []float32{1, 0}},
		{2, 1, "Подключение", "База вернула ошибку подключения", []float32{0.95, 0.05}},
		{3, 1, "Недоступность", "Сервис временно недоступен", []float32{0.9, 0.1}},
		{4, 2, "Платёж", "Платёжный сервис отвечает с таймаутом", []float32{1, 0}},
		{5, 1, "Машина", "Машины работают стабильно", []float32{0, 1}},
		{6, 1, "Модель", "Обучение модели завершено", []float32{0.1, 0.9}},
		{7, 1, "Авторизация", "Авторизация пользователя успешна", []float32{0.2, 0.8}},
		{8, 2, "Списание", "Ошибка списания платежа", []float32{0.85, 0.15}},
		{10, 1, "Connection", "Service connection error", []float32{0.8, 0.2}},
		{11, 1, "Payment", "Payment service connection error", []float32{0.75, 0.25}},
		{12, 1, "Unavailable", "Service temporarily unavailable", []float32{0.7, 0.3}},
	}
	values := make([]types.Value, 0, len(rows))
	for _, item := range rows {
		values = append(values, types.StructValue(
			types.StructFieldValue("id", types.Uint64Value(item.id)),
			types.StructFieldValue("tenant_id", types.Uint64Value(item.tenantID)),
			types.StructFieldValue("title", types.UTF8Value(item.title)),
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

func (s *Store) waitNamedIndexes(ctx context.Context, tablePath string, names []string) error {
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		description, err := s.db.Table().DescribeTable(requestCtx, tablePath)
		if err != nil {
			return fmt.Errorf("describe search probe table: %w", err)
		}
		ready := make(map[string]bool, len(names))
		for _, name := range names {
			ready[name] = false
		}
		for _, index := range description.Indexes {
			if _, ok := ready[index.Name]; ok {
				ready[index.Name] = index.Status == Ydb_Table.TableIndexDescription_STATUS_READY
			}
		}
		allReady := true
		for _, name := range names {
			allReady = allReady && ready[name]
		}
		if allReady {
			return nil
		}
		select {
		case <-requestCtx.Done():
			return fmt.Errorf("wait for search probe indexes: %w", requestCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) runFulltextCapabilityChecks(ctx context.Context, tablePath string, report *SearchCapabilityReport) {
	type queryCheck struct {
		name     string
		yql      string
		params   *table.QueryParameters
		expected []uint64
	}
	checks := []queryCheck{
		{
			name:     "fulltext plain: keywords/AND",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_plain WHERE FulltextMatch(body, "сервис таймаутом") ORDER BY id;`, quote(tablePath)),
			expected: []uint64{1, 4},
		},
		{
			name: "fulltext plain: Query +required/-excluded",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_plain WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte("+сервис -платёжный"))),
			),
			expected: []uint64{1, 3},
		},
		{
			name: "fulltext plain: exact phrase",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_plain WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte(`"ошибку подключения"`))),
			),
			expected: []uint64{2},
		},
		{
			name: "fulltext plain: Query +required/-excluded (English)",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_plain WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte("+service -payment"))),
			),
			expected: []uint64{10, 12},
		},
		{
			name: "fulltext relevance: Query +required/-excluded (English)",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_rel WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte("+service -payment"))),
			),
			expected: []uint64{10, 12},
		},
		{
			name: "fulltext plain: exact phrase (English)",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_plain WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte(`"connection error"`))),
			),
			expected: []uint64{10, 11},
		},
		{
			name: "fulltext relevance: exact phrase (English)",
			yql:  fmt.Sprintf(`DECLARE $query AS String; SELECT id FROM %s VIEW ft_rel WHERE FulltextMatch(body, $query, "Query" AS Mode) ORDER BY id;`, quote(tablePath)),
			params: table.NewQueryParameters(
				table.ValueParam("$query", types.BytesValue([]byte(`"connection error"`))),
			),
			expected: []uint64{10, 11},
		},
		{
			name: "fulltext relevance: BM25 + OR/minimum should match",
			yql: fmt.Sprintf(`SELECT id, FulltextScore(body, "ошибка подключения", "Or" AS DefaultOperator, "50%%" AS MinimumShouldMatch) AS relevance
FROM %s VIEW ft_rel
WHERE FulltextScore(body, "ошибка подключения", "Or" AS DefaultOperator, "50%%" AS MinimumShouldMatch) > 0
ORDER BY relevance DESC;`, quote(tablePath)),
			expected: []uint64{2, 8, 1},
		},
		{
			name:     "fulltext stemming: russian snowball",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_stem WHERE FulltextMatch(body, "работали") ORDER BY id;`, quote(tablePath)),
			expected: []uint64{5},
		},
		{
			name:     "fulltext ngram: wildcard",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_ngram WHERE FulltextMatch(body, "%%ключен%%", "Wildcard" AS Mode) ORDER BY id;`, quote(tablePath)),
			expected: []uint64{1, 2},
		},
		{
			name:     "fulltext ngram: LIKE",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_ngram WHERE body LIKE "%%ключен%%" ORDER BY id;`, quote(tablePath)),
			expected: []uint64{1, 2},
		},
		{
			name:     "fulltext ngram: ILIKE",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_ngram WHERE body ILIKE "%%КЛЮЧЕН%%" ORDER BY id;`, quote(tablePath)),
			expected: []uint64{1, 2},
		},
		{
			name:     "fulltext filtered: FulltextMatch",
			yql:      fmt.Sprintf(`SELECT id FROM %s VIEW ft_filtered WHERE tenant_id = 1 AND FulltextMatch(body, "таймаутом") ORDER BY id;`, quote(tablePath)),
			expected: []uint64{1},
		},
		{
			name: "fulltext filtered: FulltextScore",
			yql: fmt.Sprintf(`SELECT id, FulltextScore(body, "таймаутом") AS relevance FROM %s VIEW ft_filtered
WHERE tenant_id = 1 AND FulltextScore(body, "таймаутом") > 0 ORDER BY relevance DESC;`, quote(tablePath)),
			expected: []uint64{1},
		},
	}
	for _, check := range checks {
		ids, err := s.selectUint64IDs(ctx, check.yql, check.params)
		if err == nil && !sameIDs(ids, check.expected) {
			err = fmt.Errorf("ids=%v, expected=%v", ids, check.expected)
		}
		report.Checks = append(report.Checks, SearchCapabilityCheck{Name: check.name, Detail: fmt.Sprintf("ids=%v", ids), Err: err})
	}

	plainScoreYQL := fmt.Sprintf(`SELECT id, FulltextScore(body, "ошибка") AS relevance FROM %s VIEW ft_plain
WHERE FulltextScore(body, "ошибка") > 0 ORDER BY relevance DESC;`, quote(tablePath))
	plainScoreIDs, plainScoreErr := s.selectUint64IDs(ctx, plainScoreYQL, nil)
	report.Checks = append(report.Checks, SearchCapabilityCheck{
		Name:   "fulltext plain: FulltextScore behavior",
		Detail: fmt.Sprintf("query accepted, ids=%v (documented ranking index is fulltext_relevance)", plainScoreIDs),
		Err:    plainScoreErr,
	})

	s.runExpectedFulltextFailures(ctx, tablePath, report)
	s.runTextDMLChecks(ctx, tablePath, report)
	s.runIndexedDMLChecks(ctx, tablePath, report)
	s.runPlanCheck(ctx, "fulltext parameterized plan uses ft_rel", fmt.Sprintf(`DECLARE $query AS Utf8;
SELECT id, FulltextScore(body, $query) AS relevance
FROM %s VIEW ft_rel
WHERE FulltextScore(body, $query) > 0
ORDER BY relevance DESC LIMIT 3;`, quote(tablePath)), []string{"ft_rel"}, report)
}

func (s *Store) runExpectedFulltextFailures(ctx context.Context, tablePath string, report *SearchCapabilityReport) {
	checks := []struct {
		name string
		yql  string
	}{
		{"fulltext guard: search without VIEW is rejected", fmt.Sprintf(`SELECT id FROM %s WHERE FulltextMatch(body, "ошибка");`, quote(tablePath))},
		{"fulltext guard: filtered index requires prefix equality", fmt.Sprintf(`SELECT id FROM %s VIEW ft_filtered WHERE FulltextMatch(body, "таймаутом");`, quote(tablePath))},
	}
	for _, check := range checks {
		_, queryErr := s.selectUint64IDs(ctx, check.yql, nil)
		var err error
		if queryErr == nil {
			err = fmt.Errorf("query unexpectedly succeeded")
		}
		report.Checks = append(report.Checks, SearchCapabilityCheck{Name: check.name, Detail: "expected rejection observed", Err: err})
	}
}

func (s *Store) runTextDMLChecks(ctx context.Context, parentPath string, report *SearchCapabilityReport) {
	tablePath := path.Join(path.Dir(parentPath), searchTextDMLProbeTableName)
	dropDDL := fmt.Sprintf("DROP TABLE IF EXISTS %s;", quote(tablePath))
	err := s.ExecScheme(ctx, dropDDL)
	if err == nil {
		err = s.ExecScheme(ctx, fmt.Sprintf(`CREATE TABLE %s (
    id Uint64 NOT NULL,
    body Utf8 NOT NULL,
    PRIMARY KEY (id)
);`, quote(tablePath)))
	}
	upsert := fmt.Sprintf(`DECLARE $id AS Uint64; DECLARE $body AS Utf8;
UPSERT INTO %s (id, body) VALUES ($id, $body);`, quote(tablePath))
	write := func(id uint64, body string) error {
		return s.execData(ctx, upsert, table.NewQueryParameters(
			table.ValueParam("$id", types.Uint64Value(id)),
			table.ValueParam("$body", types.UTF8Value(body)),
		))
	}
	contains := func(term string, want bool) error {
		yql := fmt.Sprintf(`DECLARE $term AS Utf8;
SELECT id FROM %s VIEW ft_dml WHERE FulltextMatch(body, $term) ORDER BY id;`, quote(tablePath))
		params := table.NewQueryParameters(table.ValueParam("$term", types.UTF8Value(term)))
		ids, queryErr := s.selectUint64IDs(ctx, yql, params)
		if queryErr != nil {
			return queryErr
		}
		found := false
		for _, id := range ids {
			found = found || id == 2
		}
		if found != want {
			return fmt.Errorf("id=2 found=%t, expected=%t, ids=%v", found, want, ids)
		}
		return nil
	}
	if err == nil {
		err = write(1, "контрольная строка")
	}
	if err == nil {
		err = s.ExecScheme(ctx, fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_dml GLOBAL USING fulltext_relevance
ON (body) WITH (tokenizer=standard, use_filter_lowercase=true);`, quote(tablePath)))
	}
	if err == nil {
		err = s.waitNamedIndexes(ctx, tablePath, []string{"ft_dml"})
	}
	if err == nil {
		err = write(2, "syncmarker error")
	}
	if err == nil {
		if stepErr := contains("syncmarker", true); stepErr != nil {
			err = fmt.Errorf("query ASCII row after insert: %w", stepErr)
		}
	}
	if err == nil {
		err = write(2, "синхронныймаркер ошибка")
	}
	if err == nil {
		if stepErr := contains("синхронныймаркер", true); stepErr != nil {
			err = fmt.Errorf("query UTF-8 row after update: %w", stepErr)
		}
	}
	if err == nil {
		err = contains("syncmarker", false)
	}
	if err == nil {
		err = s.execData(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = 2;`, quote(tablePath)), nil)
	}
	if err == nil {
		err = contains("синхронныймаркер", false)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), s.adminTimeout)
	cleanupErr := s.ExecScheme(cleanupCtx, dropDDL)
	cancel()
	if cleanupErr != nil {
		err = errors.Join(err, fmt.Errorf("remove text DML probe table: %w", cleanupErr))
	}
	report.Checks = append(report.Checks, SearchCapabilityCheck{Name: "fulltext-only DML: insert/update/delete", Detail: "fulltext index followed all row changes", Err: err})
}

func (s *Store) runIndexedDMLChecks(ctx context.Context, parentPath string, report *SearchCapabilityReport) {
	tablePath := path.Join(path.Dir(parentPath), searchDMLProbeTableName)
	dropDDL := fmt.Sprintf("DROP TABLE IF EXISTS %s;", quote(tablePath))
	err := s.ExecScheme(ctx, dropDDL)
	if err == nil {
		err = s.ExecScheme(ctx, fmt.Sprintf(`CREATE TABLE %s (
    id Uint64 NOT NULL,
    body Utf8 NOT NULL,
    embedding String NOT NULL,
    PRIMARY KEY (id)
);`, quote(tablePath)))
	}
	upsert := fmt.Sprintf(`DECLARE $id AS Uint64; DECLARE $body AS Utf8; DECLARE $embedding AS String;
UPSERT INTO %s (id, body, embedding) VALUES ($id, $body, $embedding);`, quote(tablePath))
	params := func(id uint64, body string, embedding []float32) *table.QueryParameters {
		return table.NewQueryParameters(
			table.ValueParam("$id", types.Uint64Value(id)),
			table.ValueParam("$body", types.UTF8Value(body)),
			table.ValueParam("$embedding", types.BytesValue(float32Vector(embedding))),
		)
	}
	checkFulltextID := func(term string, want bool) error {
		yql := fmt.Sprintf(`DECLARE $term AS Utf8;
SELECT id FROM %s VIEW ft_dml WHERE FulltextMatch(body, $term) ORDER BY id;`, quote(tablePath))
		params := table.NewQueryParameters(table.ValueParam("$term", types.UTF8Value(term)))
		ids, err := s.selectUint64IDs(ctx, yql, params)
		if err != nil {
			return err
		}
		found := false
		for _, id := range ids {
			found = found || id == 2
		}
		if found != want {
			return fmt.Errorf("id=2 found=%t, expected=%t, ids=%v", found, want, ids)
		}
		return nil
	}
	if err == nil {
		if stepErr := s.execData(ctx, upsert, params(1, "контрольная строка", []float32{0, 1})); stepErr != nil {
			err = fmt.Errorf("insert before indexes: %w", stepErr)
		}
	}
	if err == nil {
		if stepErr := s.ExecScheme(ctx, fmt.Sprintf(`ALTER TABLE %s ADD INDEX ft_dml GLOBAL USING fulltext_relevance
ON (body) WITH (tokenizer=standard, use_filter_lowercase=true);`, quote(tablePath))); stepErr != nil {
			err = fmt.Errorf("create fulltext index: %w", stepErr)
		}
	}
	if err == nil {
		err = s.waitNamedIndexes(ctx, tablePath, []string{"ft_dml"})
	}
	if err == nil {
		if stepErr := s.execData(ctx, upsert, params(2, "syncmarker error", []float32{1, 0})); stepErr != nil {
			err = fmt.Errorf("insert with compact fulltext index: %w", stepErr)
		}
	}
	if err == nil {
		if stepErr := checkFulltextID("syncmarker", true); stepErr != nil {
			err = fmt.Errorf("query after insert with compact fulltext index: %w", stepErr)
		}
	}
	if err == nil {
		if stepErr := s.ExecScheme(ctx, fmt.Sprintf(`ALTER TABLE %s ADD INDEX vec_dml GLOBAL SYNC USING vector_kmeans_tree
ON (embedding) WITH (similarity="inner_product", vector_type="float", vector_dimension=2, levels=1, clusters=2);`, quote(tablePath))); stepErr != nil {
			err = fmt.Errorf("create vector index: %w", stepErr)
		}
	}
	if err == nil {
		err = s.waitNamedIndexes(ctx, tablePath, []string{"ft_dml", "vec_dml"})
	}
	if err == nil {
		yql := fmt.Sprintf(`DECLARE $target AS String; SELECT id FROM %s VIEW vec_dml ORDER BY Knn::InnerProductSimilarity(embedding, $target) DESC LIMIT 1;`, quote(tablePath))
		ids, queryErr := s.selectUint64IDs(ctx, yql, table.NewQueryParameters(
			table.ValueParam("$target", types.BytesValue(float32Vector([]float32{1, 0}))),
		))
		if queryErr != nil {
			err = queryErr
		} else if !sameIDs(ids, []uint64{2}) {
			err = fmt.Errorf("vector index returned ids=%v, expected=[2]", ids)
		}
	}
	if err == nil {
		if stepErr := s.execData(ctx, upsert, params(2, "updatedmarker problem", []float32{0.9, 0.1})); stepErr != nil {
			err = fmt.Errorf("update with fulltext and vector indexes: %w", stepErr)
		}
	}
	if err == nil {
		err = checkFulltextID("syncmarker", false)
	}
	if err == nil {
		err = checkFulltextID("updatedmarker", true)
	}
	if err == nil {
		if stepErr := s.execData(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = 2;`, quote(tablePath)), nil); stepErr != nil {
			err = fmt.Errorf("delete with fulltext and vector indexes: %w", stepErr)
		}
	}
	if err == nil {
		err = checkFulltextID("updatedmarker", false)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), s.adminTimeout)
	cleanupErr := s.ExecScheme(cleanupCtx, dropDDL)
	cancel()
	if cleanupErr != nil {
		err = errors.Join(err, fmt.Errorf("remove DML probe table: %w", cleanupErr))
	}
	report.Checks = append(report.Checks, SearchCapabilityCheck{Name: "indexed DML: insert/update/delete", Detail: "fulltext and vector indexes followed all row changes", Err: err})
}

func (s *Store) execData(ctx context.Context, yql string, params *table.QueryParameters) error {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		_, result, err := session.Execute(ctx, table.SerializableReadWriteTxControl(table.CommitTx()), yql, params)
		if err != nil {
			return err
		}
		defer result.Close()
		return result.Err()
	})
}

func (s *Store) runHybridCapabilityChecks(ctx context.Context, query model.Query, report *SearchCapabilityReport) {
	base := func(options string, parameterLimit bool) (string, *table.QueryParameters) {
		limitDecl := ""
		limit := "10"
		params := table.NewQueryParameters(
			table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextText()))),
			table.ValueParam("$target", types.BytesValue(query.Embedding)),
		)
		if parameterLimit {
			limitDecl = "DECLARE $limit AS Uint64;"
			limit = "$limit"
			params = table.NewQueryParameters(
				table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextText()))),
				table.ValueParam("$target", types.BytesValue(query.Embedding)),
				table.ValueParam("$limit", types.Uint64Value(10)),
			)
		}
		yql := fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
DECLARE $query AS Utf8; DECLARE $target AS String; %s
SELECT docid FROM %s
ORDER BY HybridRank(
    FullTextScore(text, $query, "Or" AS DefaultOperator, "50%%" AS MinimumShouldMatch),
    Knn::InnerProductSimilarity(embedding, $target)%s
) LIMIT %s;`, s.cfg.KMeansSearchTopSize, limitDecl, quote(s.tablePath), options, limit)
		return yql, params
	}
	tests := []struct {
		name           string
		options        string
		parameterLimit bool
	}{
		{"hybrid: default RRF with full text options", ", (10, 10) AS Limits", false},
		{"hybrid: weighted RRF and custom K", ", (1.0, 2.0) AS Weights, 40.0 AS K, (10, 10) AS Limits", false},
		{"hybrid: normalized linear fusion", ", \"linear\" AS Mode, (1.0, 2.0) AS Weights, true AS Normalize, (10, 10) AS Limits", false},
		{"hybrid: explicit indexes and parameter LIMIT", fmt.Sprintf(", (\"%s\", \"%s\") AS Indexes, (10, 10) AS Limits", s.cfg.FulltextIndex, s.cfg.VectorIndex), true},
		{"hybrid: custom RankLambda", `, ($ranks) -> {
        RETURN 1.0 / (60 + COALESCE($ranks[0], 100000))
             + 2.0 / (60 + COALESCE($ranks[1], 100000));
    } AS RankLambda, (10, 10) AS Limits`, false},
	}
	for _, test := range tests {
		yql, params := base(test.options, test.parameterLimit)
		ids, retries, err := s.selectDocIDs(ctx, yql, params)
		if err == nil && len(ids) == 0 {
			err = fmt.Errorf("query returned no documents")
		}
		report.Checks = append(report.Checks, SearchCapabilityCheck{Name: test.name, Detail: fmt.Sprintf("results=%d retries=%d", len(ids), retries), Err: err})
	}

	planYQL, _ := base(fmt.Sprintf(", (\"%s\", \"%s\") AS Indexes, (10, 10) AS Limits", s.cfg.FulltextIndex, s.cfg.VectorIndex), false)
	s.runPlanCheck(ctx, "hybrid plan uses both indexes", planYQL, []string{s.cfg.FulltextIndex, s.cfg.VectorIndex}, report)
}

func (s *Store) runPlanCheck(ctx context.Context, name, yql string, required []string, report *SearchCapabilityReport) {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	detail := ""
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		explanation, err := session.Explain(ctx, yql)
		if err != nil {
			return err
		}
		missing := make([]string, 0)
		for _, item := range required {
			if !strings.Contains(explanation.Plan, item) {
				missing = append(missing, item)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("plan does not mention indexes %v", missing)
		}
		if strings.Contains(explanation.Plan, "TableFullScan") {
			return fmt.Errorf("plan contains TableFullScan")
		}
		detail = fmt.Sprintf("indexes=%s, no TableFullScan", strings.Join(required, ","))
		return nil
	}, table.WithIdempotent())
	report.Checks = append(report.Checks, SearchCapabilityCheck{Name: name, Detail: detail, Err: err})
}
