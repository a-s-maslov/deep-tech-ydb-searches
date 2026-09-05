package ydbstore

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb_Table"
	"github.com/ydb-platform/ydb-go-sdk/v3"
	"github.com/ydb-platform/ydb-go-sdk/v3/table"
	tableoptions "github.com/ydb-platform/ydb-go-sdk/v3/table/options"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/result/named"
	"github.com/ydb-platform/ydb-go-sdk/v3/table/types"
	"golang.org/x/text/unicode/norm"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type Store struct {
	cfg          config.Config
	db           *ydb.Driver
	tablePath    string
	timeout      time.Duration
	adminTimeout time.Duration
}

type PartitionStat = model.PartitionStat

const (
	VectorIndexLevels                  = 2
	VectorIndexClusters                = 128
	VectorIndexOverlapClusters         = 3
	FulltextMinimumShouldMatch         = "50%"
	FulltextWorkloadMinimumShouldMatch = "100%"
	FulltextK1                         = 1.0
	FulltextB                          = 0.25
	HybridMode                         = "linear"
	HybridFulltextWeight               = 0.075
	HybridVectorWeight                 = 1.0
	HybridNormalize                    = true
	// Online queries return the top ten shown in the workshop. Quality runs use
	// deeper top-30 result sets and branch pools through the methods below.
	OnlineVectorLimit       = 10
	OnlineHybridBranchLimit = 10
	HybridBranchLimit       = 30
)

func Open(ctx context.Context, cfg config.Config) (*Store, error) {
	options := make([]ydb.Option, 0, 3)
	if cfg.Anonymous {
		options = append(options, ydb.WithAnonymousCredentials())
	}
	if cfg.Token != "" {
		options = append(options, ydb.WithAccessTokenCredentials(cfg.Token))
	}
	if cfg.CAFile != "" {
		options = append(options, ydb.WithCertificatesFromFile(cfg.CAFile))
	}
	if cfg.NodeAddressOverride != "" {
		options = append(options, ydb.WithNodeAddressMutator(func(string) string { return cfg.NodeAddressOverride }))
	}
	if cfg.SessionPoolSize > 0 {
		options = append(options, ydb.WithSessionPoolSizeLimit(cfg.SessionPoolSize))
	}
	if cfg.SessionPoolUsage > 0 {
		options = append(options, ydb.WithSessionPoolSessionUsageLimit(cfg.SessionPoolUsage))
	}
	driver, err := ydb.Open(ctx, cfg.ConnectionString, options...)
	if err != nil {
		return nil, fmt.Errorf("open YDB: %w", err)
	}
	timeout, _ := cfg.RequestTimeoutDuration()
	adminTimeout, _ := cfg.AdminTimeoutDuration()
	tablePath := cfg.TablePath
	if !strings.HasPrefix(tablePath, "/") {
		tablePath = path.Join(driver.Name(), tablePath)
	}
	return &Store{cfg: cfg, db: driver, tablePath: tablePath, timeout: timeout, adminTimeout: adminTimeout}, nil
}

func (s *Store) Close(ctx context.Context) error { return s.db.Close(ctx) }

func TableDDL(cfg config.Config) string {
	return fmt.Sprintf(`CREATE TABLE %s (
    id Uint64 NOT NULL,
    docid Utf8 NOT NULL,
    title Utf8 NOT NULL,
    text Utf8 NOT NULL,
    embedding String NOT NULL,
    PRIMARY KEY (id)
) WITH (
    AUTO_PARTITIONING_BY_LOAD = ENABLED,
    AUTO_PARTITIONING_BY_SIZE = ENABLED,
    AUTO_PARTITIONING_PARTITION_SIZE_MB = 2000,
    AUTO_PARTITIONING_MIN_PARTITIONS_COUNT = %d,
    AUTO_PARTITIONING_MAX_PARTITIONS_COUNT = 64
);`, quote(cfg.TablePath), max(cfg.Partitioning.BaseMinPartitions, 1))
}

func FulltextDDL(cfg config.Config) string {
	return fmt.Sprintf(`ALTER TABLE %s
ADD INDEX %s GLOBAL USING fulltext_relevance
ON (text) COVER (title, docid)
WITH (
    tokenizer = standard,
    use_filter_lowercase = true,
    use_filter_snowball = true,
    language = "russian"
);`, quote(cfg.TablePath), quote(cfg.FulltextIndex))
}

func VectorDDL(cfg config.Config) string {
	return fmt.Sprintf(`ALTER TABLE %s
ADD INDEX %s GLOBAL SYNC USING vector_kmeans_tree
ON (embedding) COVER (title, docid)
WITH (
    similarity = "inner_product",
    vector_type = "float",
    vector_dimension = %d,
	levels = %d,
	clusters = %d,
	overlap_clusters = %d
);`, quote(cfg.TablePath), quote(cfg.VectorIndex), cfg.VectorDimension,
		VectorIndexLevels, VectorIndexClusters, VectorIndexOverlapClusters)
}

func (s *Store) ExecScheme(ctx context.Context, ddl string) error {
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	return s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		return session.ExecuteSchemeQuery(ctx, ddl)
	}, table.WithIdempotent())
}

func (s *Store) Drop(ctx context.Context) error {
	return s.ExecScheme(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s;", quote(s.cfg.TablePath)))
}

func (s *Store) Init(ctx context.Context) error { return s.ExecScheme(ctx, TableDDL(s.cfg)) }

func (s *Store) CreateIndexes(ctx context.Context) error {
	for _, index := range []struct {
		name string
		ddl  string
	}{
		{name: s.cfg.FulltextIndex, ddl: FulltextDDL(s.cfg)},
		{name: s.cfg.VectorIndex, ddl: VectorDDL(s.cfg)},
	} {
		statuses, err := s.indexStatuses(ctx)
		if err != nil {
			return fmt.Errorf("describe indexes before creating %s: %w", index.name, err)
		}
		if status, exists := statuses[index.name]; exists {
			fmt.Printf("index %s already exists: %s\n", index.name, status.String())
			continue
		}
		if err := s.ExecScheme(ctx, index.ddl); err != nil {
			// A long-running schema request can be accepted by YDB even when the
			// client reaches its deadline. Re-read the schema before reporting a
			// failure so the command can safely resume after an ambiguous timeout.
			statuses, describeErr := s.indexStatuses(ctx)
			if describeErr == nil {
				if status, exists := statuses[index.name]; exists {
					fmt.Printf("index %s appeared after the DDL error: %s\n", index.name, status.String())
					continue
				}
			}
			return fmt.Errorf("create index %s: %w", index.name, err)
		}
	}
	return nil
}

func (s *Store) CreateFulltextVariantIndex(ctx context.Context, cfg model.FulltextConfig, snowball bool) error {
	for _, identifier := range []string{cfg.Index, cfg.Column} {
		if identifier == "" || strings.ContainsAny(identifier, "\"`;\r\n\\") {
			return fmt.Errorf("unsafe fulltext identifier %q", identifier)
		}
	}
	tablePath, err := s.resolveTable(cfg.Table)
	if err != nil {
		return err
	}
	statuses, err := s.indexStatusesAt(ctx, tablePath)
	if err != nil {
		return fmt.Errorf("describe indexes before creating %s: %w", cfg.Index, err)
	}
	if status, exists := statuses[cfg.Index]; exists {
		fmt.Printf("index %s already exists: %s\n", cfg.Index, status.String())
		return nil
	}
	filters := "tokenizer = standard, use_filter_lowercase = true"
	if snowball {
		filters += `, use_filter_snowball = true, language = "russian"`
	}
	if cfg.FilterLengthMin < 0 || cfg.FilterLengthMax < 0 || (cfg.FilterLengthMax > 0 && cfg.FilterLengthMin > cfg.FilterLengthMax) {
		return fmt.Errorf("invalid fulltext token length range %d..%d", cfg.FilterLengthMin, cfg.FilterLengthMax)
	}
	if cfg.FilterLengthMin > 0 || cfg.FilterLengthMax > 0 {
		filters += ", use_filter_length = true"
		if cfg.FilterLengthMin > 0 {
			filters += fmt.Sprintf(", filter_length_min = %d", cfg.FilterLengthMin)
		}
		if cfg.FilterLengthMax > 0 {
			filters += fmt.Sprintf(", filter_length_max = %d", cfg.FilterLengthMax)
		}
	}
	ddl := fmt.Sprintf(`ALTER TABLE %s
ADD INDEX %s GLOBAL USING fulltext_relevance
ON (%s) COVER (docid)
WITH (%s);`, quote(tablePath), quote(cfg.Index), quote(cfg.Column), filters)
	if err := s.ExecScheme(ctx, ddl); err != nil {
		return fmt.Errorf("create fulltext quality index %s: %w", cfg.Index, err)
	}
	fmt.Printf("index %s creation submitted\n", cfg.Index)
	return nil
}

func (s *Store) DropFulltextVariantIndex(ctx context.Context, cfg model.FulltextConfig) error {
	if cfg.Index == "" || strings.ContainsAny(cfg.Index, "\"`;\r\n\\") {
		return fmt.Errorf("unsafe index identifier %q", cfg.Index)
	}
	tablePath, err := s.resolveTable(cfg.Table)
	if err != nil {
		return err
	}
	return s.ExecScheme(ctx, fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", quote(tablePath), quote(cfg.Index)))
}

func (s *Store) ResetFulltextIndex(ctx context.Context) error {
	if err := s.dropIndex(ctx, s.cfg.FulltextIndex, DropFulltextDDL(s.cfg)); err != nil {
		return fmt.Errorf("drop fulltext index: %w", err)
	}
	// CreateIndexes re-reads the schema after an ambiguous DDL error. This is
	// important for index rebuilds: YDB may accept ADD INDEX even if the client
	// loses its tablet connection before receiving the acknowledgement.
	return s.CreateIndexes(ctx)
}

func DropFulltextDDL(cfg config.Config) string {
	return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", quote(cfg.TablePath), quote(cfg.FulltextIndex))
}

func (s *Store) ResetVectorIndex(ctx context.Context) error {
	if err := s.dropIndex(ctx, s.cfg.VectorIndex, DropVectorDDL(s.cfg)); err != nil {
		return fmt.Errorf("drop vector index: %w", err)
	}
	// CreateIndexes re-reads the schema, skips the existing full-text index and
	// handles an ambiguous timeout after YDB has accepted the vector-index DDL.
	return s.CreateIndexes(ctx)
}

func (s *Store) dropIndex(ctx context.Context, name, ddl string) error {
	if err := s.ExecScheme(ctx, ddl); err != nil {
		// ExecuteSchemeQuery can lose the acknowledgement after YDB has already
		// applied the schema change. Treat the outcome as successful only when a
		// fresh schema read proves that the requested index is absent.
		statuses, describeErr := s.indexStatuses(ctx)
		if describeErr == nil {
			if _, exists := statuses[name]; !exists {
				fmt.Printf("index %s disappeared after the DDL error; continuing\n", name)
				return nil
			}
		}
		return err
	}
	return nil
}

func DropVectorDDL(cfg config.Config) string {
	return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", quote(cfg.TablePath), quote(cfg.VectorIndex))
}

func (s *Store) indexStatuses(ctx context.Context) (map[string]Ydb_Table.TableIndexDescription_Status, error) {
	return s.indexStatusesAt(ctx, s.tablePath)
}

func (s *Store) indexStatusesAt(ctx context.Context, tablePath string) (map[string]Ydb_Table.TableIndexDescription_Status, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	description, err := s.db.Table().DescribeTable(requestCtx, tablePath)
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]Ydb_Table.TableIndexDescription_Status, len(description.Indexes))
	for _, index := range description.Indexes {
		statuses[index.Name] = index.Status
	}
	return statuses, nil
}

func (s *Store) WaitIndexes(ctx context.Context, interval time.Duration) error {
	for {
		statuses, err := s.indexStatuses(ctx)
		if err != nil {
			return err
		}
		parts := make([]string, 0, 2)
		ready := true
		for _, name := range []string{s.cfg.FulltextIndex, s.cfg.VectorIndex} {
			status, exists := statuses[name]
			parts = append(parts, fmt.Sprintf("%s=%s", name, status.String()))
			ready = ready && exists && status == Ydb_Table.TableIndexDescription_STATUS_READY
		}
		sort.Strings(parts)
		fmt.Println(strings.Join(parts, ", "))
		if ready {
			return nil
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Store) WaitIndex(ctx context.Context, tableName, name string, interval time.Duration) error {
	tablePath, err := s.resolveTable(tableName)
	if err != nil {
		return err
	}
	for {
		statuses, err := s.indexStatusesAt(ctx, tablePath)
		if err != nil {
			return err
		}
		status, exists := statuses[name]
		if !exists {
			return fmt.Errorf("index %s is absent", name)
		}
		fmt.Printf("%s=%s\n", name, status.String())
		if status == Ydb_Table.TableIndexDescription_STATUS_READY {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *Store) resolveTable(name string) (string, error) {
	if name == "" {
		return s.tablePath, nil
	}
	if strings.ContainsAny(name, "\"`;\r\n\\") {
		return "", fmt.Errorf("unsafe table identifier %q", name)
	}
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	return path.Join(s.db.Name(), name), nil
}

func (s *Store) BulkUpsert(ctx context.Context, documents []model.Document) error {
	values := documentValues(documents)
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	return s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		return session.BulkUpsert(ctx, s.tablePath, types.ListValue(values...))
	}, table.WithIdempotent())
}

// DeleteDocumentsRange removes one bounded primary-key interval. Keeping the
// transaction small makes cleanup of the reusable DML pool predictable even
// when synchronous search indexes have to process every deleted row.
func (s *Store) DeleteDocumentsRange(ctx context.Context, start, end uint64) error {
	if start >= end {
		return fmt.Errorf("invalid document range [%d, %d)", start, end)
	}
	yql := fmt.Sprintf(`DECLARE $start AS Uint64;
DECLARE $end AS Uint64;
DELETE FROM %s
WHERE id >= $start AND id < $end;`, quote(s.cfg.TablePath))
	params := table.NewQueryParameters(
		table.ValueParam("$start", types.Uint64Value(start)),
		table.ValueParam("$end", types.Uint64Value(end)),
	)
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	return s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		_, result, err := session.Execute(ctx, table.SerializableReadWriteTxControl(table.CommitTx()), yql, params)
		if err != nil {
			return err
		}
		defer result.Close()
		return result.Err()
	}, table.WithIdempotent())
}

func documentValues(documents []model.Document) []types.Value {
	values := make([]types.Value, 0, len(documents))
	for _, document := range documents {
		values = append(values, types.StructValue(
			types.StructFieldValue("id", types.Uint64Value(document.ID)),
			types.StructFieldValue("docid", types.UTF8Value(document.DocID)),
			types.StructFieldValue("title", types.UTF8Value(document.Title)),
			types.StructFieldValue("text", types.UTF8Value(document.Text)),
			types.StructFieldValue("embedding", types.BytesValue(document.Embedding)),
		))
	}
	return values
}

func (s *Store) UpsertDocument(ctx context.Context, document model.Document) (int, error) {
	yql := fmt.Sprintf(`DECLARE $id AS Uint64;
DECLARE $docid AS Utf8;
DECLARE $title AS Utf8;
DECLARE $text AS Utf8;
DECLARE $embedding AS String;
UPSERT INTO %s (id, docid, title, text, embedding)
VALUES ($id, $docid, $title, $text, $embedding);`, quote(s.cfg.TablePath))
	params := table.NewQueryParameters(
		table.ValueParam("$id", types.Uint64Value(document.ID)),
		table.ValueParam("$docid", types.UTF8Value(document.DocID)),
		table.ValueParam("$title", types.UTF8Value(document.Title)),
		table.ValueParam("$text", types.UTF8Value(document.Text)),
		table.ValueParam("$embedding", types.BytesValue(document.Embedding)),
	)
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	attempts := 0
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		attempts++
		_, result, err := session.Execute(ctx, table.SerializableReadWriteTxControl(table.CommitTx()), yql, params)
		if err != nil {
			return err
		}
		defer result.Close()
		return result.Err()
	}, table.WithIdempotent())
	return retryCount(attempts), err
}

func (s *Store) VerifyFulltextMarker(ctx context.Context, marker, expectedDocID string) (int, error) {
	yql := fmt.Sprintf(`DECLARE $marker AS Utf8;
SELECT docid FROM %s VIEW %s
WHERE FulltextMatch(text, $marker)
LIMIT 10;`, quote(s.cfg.TablePath), quote(s.cfg.FulltextIndex))
	docIDs, retries, err := s.selectDocIDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$marker", types.UTF8Value(marker)),
	))
	if err != nil {
		return retries, err
	}
	for _, docID := range docIDs {
		if docID == expectedDocID {
			return retries, nil
		}
	}
	return retries, fmt.Errorf("marker %q is not visible through index %s for docid %q", marker, s.cfg.FulltextIndex, expectedDocID)
}

func (s *Store) CountDocuments(ctx context.Context) (uint64, error) {
	yql := fmt.Sprintf("SELECT COUNT(*) AS count FROM %s;", quote(s.cfg.TablePath))
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var count uint64
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		_, result, err := session.Execute(ctx, table.OnlineReadOnlyTxControl(), yql, nil)
		if err != nil {
			return err
		}
		defer result.Close()
		if err := result.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !result.NextRow() {
			return fmt.Errorf("count returned no row")
		}
		return result.ScanNamed(named.Required("count", &count))
	}, table.WithIdempotent())
	return count, err
}

func (s *Store) ServerVersion(ctx context.Context) (string, error) {
	const yql = "SELECT Version() AS version;"
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var version string
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		_, result, err := session.Execute(ctx, table.OnlineReadOnlyTxControl(), yql, nil)
		if err != nil {
			return err
		}
		defer result.Close()
		if err := result.NextResultSetErr(ctx); err != nil {
			return err
		}
		if !result.NextRow() {
			return fmt.Errorf("Version() returned no row")
		}
		return result.ScanNamed(named.Required("version", &version))
	}, table.WithIdempotent())
	return version, err
}

func (s *Store) PartitionStats(ctx context.Context) ([]PartitionStat, error) {
	yql := `DECLARE $base AS Utf8;
DECLARE $children AS Utf8;
SELECT Path, TabletId, RowCount, DataSize, CPUCores
FROM ` + "`" + `.sys/partition_stats` + "`" + `
WHERE Path = $base OR Path LIKE $children
ORDER BY Path, TabletId;`
	params := table.NewQueryParameters(
		table.ValueParam("$base", types.UTF8Value(s.tablePath)),
		table.ValueParam("$children", types.UTF8Value(s.tablePath+"/%")),
	)
	requestCtx, cancel := context.WithTimeout(ctx, s.adminTimeout)
	defer cancel()
	stats := make([]PartitionStat, 0, 8)
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		_, result, err := session.Execute(ctx, table.OnlineReadOnlyTxControl(), yql, params)
		if err != nil {
			return err
		}
		defer result.Close()
		if err := result.NextResultSetErr(ctx); err != nil {
			return err
		}
		byPath := make(map[string]*PartitionStat, 8)
		for result.NextRow() {
			var path string
			var tabletID, rows, size uint64
			var cpu float64
			if err := result.ScanNamed(
				named.OptionalWithDefault("Path", &path),
				named.OptionalWithDefault("TabletId", &tabletID),
				named.OptionalWithDefault("RowCount", &rows),
				named.OptionalWithDefault("DataSize", &size),
				named.OptionalWithDefault("CPUCores", &cpu),
			); err != nil {
				return err
			}
			item := byPath[path]
			if item == nil {
				item = &PartitionStat{Path: path}
				byPath[path] = item
				stats = append(stats, *item)
			}
			item.Partitions++
			item.Rows += rows
			item.Size += size
			item.CPU += cpu
			item.Tablets = append(item.Tablets, model.TabletStat{ID: tabletID, CPU: cpu})
			stats[len(stats)-1] = *item
		}
		return result.Err()
	}, table.WithIdempotent())
	return stats, err
}

func (s *Store) SetPartitioning(ctx context.Context, mode, scope string) error {
	targets, err := partitionTargets(s.cfg.TablePath, s.cfg.FulltextIndex, s.cfg.VectorIndex, scope)
	if err != nil {
		return err
	}
	stats, err := s.PartitionStats(ctx)
	if err != nil {
		return err
	}
	partitionsByTarget := make(map[string]uint64, len(targets))
	existingTargets := make([]string, 0, len(targets))
	for _, target := range targets {
		found := false
		for _, stat := range stats {
			candidate := strings.Trim(stat.Path, "/")
			if strings.HasSuffix(candidate, strings.Trim(target, "/")) {
				found = true
				partitionsByTarget[target] = stat.Partitions
				existingTargets = append(existingTargets, target)
				break
			}
		}
		if !found {
			fmt.Printf("skip partitioning target absent in this YDB build: %s\n", target)
		}
	}
	if len(existingTargets) == 0 {
		return fmt.Errorf("no partitioning targets found for scope %s", scope)
	}
	load := "DISABLED"
	if mode == "auto" || mode == "elastic" {
		load = "ENABLED"
	} else if mode != "fixed" {
		return fmt.Errorf("partition mode must be fixed, auto or elastic")
	}
	for _, target := range existingTargets {
		minimum := uint64(0)
		if mode == "fixed" {
			minimum = partitionsByTarget[target]
		} else if mode == "elastic" {
			minimum = elasticMinimum(s.cfg, target, partitionsByTarget[target])
		}
		ddl := partitioningDDL(target, load, minimum)
		if err := s.ExecScheme(ctx, ddl); err != nil {
			return fmt.Errorf("set partitioning for %s: %w", target, err)
		}
	}
	return nil
}

func elasticMinimum(cfg config.Config, target string, current uint64) uint64 {
	minimum := current
	cleanTarget := strings.Trim(target, "/")
	if cleanTarget == strings.Trim(cfg.TablePath, "/") {
		return max(minimum, cfg.Partitioning.BaseMinPartitions)
	}
	fulltextDocsTable := path.Join(cfg.TablePath, cfg.FulltextIndex, "indexImplDocsTable")
	if cleanTarget == strings.Trim(fulltextDocsTable, "/") {
		return max(minimum, cfg.Partitioning.FulltextMinPartitions)
	}
	vectorPostingTable := path.Join(cfg.TablePath, cfg.VectorIndex, "indexImplPostingTable")
	vectorLevelTable := path.Join(cfg.TablePath, cfg.VectorIndex, "indexImplLevelTable")
	if cleanTarget == strings.Trim(vectorPostingTable, "/") || cleanTarget == strings.Trim(vectorLevelTable, "/") {
		return max(minimum, cfg.Partitioning.VectorMinPartitions)
	}
	return minimum
}

// partitioningDDL pins the current partition count when minimum is non-zero.
// Fixed mode pins the current count and disables load-based split. Elastic
// mode keeps load-based split enabled and pins at least the configured demo
// capacity. Auto mode changes only the split switch and leaves the existing
// minimum unchanged.
func partitioningDDL(target, load string, minimum uint64) string {
	minimumSetting := ""
	if minimum > 0 {
		minimumSetting = fmt.Sprintf("    AUTO_PARTITIONING_MIN_PARTITIONS_COUNT = %d,\n", minimum)
	}
	return fmt.Sprintf(`ALTER TABLE %s SET (
    AUTO_PARTITIONING_BY_LOAD = %s,
    AUTO_PARTITIONING_BY_SIZE = ENABLED,
    AUTO_PARTITIONING_PARTITION_SIZE_MB = 2000,
%s    AUTO_PARTITIONING_MAX_PARTITIONS_COUNT = 64
);`, quote(target), load, minimumSetting)
}

func partitionTargets(tableName, fulltextIndex, vectorIndex, scope string) ([]string, error) {
	groups := map[string][]string{
		"base": {tableName},
		"fulltext": {
			path.Join(tableName, fulltextIndex, "indexImplDictTable"),
			path.Join(tableName, fulltextIndex, "indexImplDocsTable"),
			path.Join(tableName, fulltextIndex, "indexImplStatsTable"),
			path.Join(tableName, fulltextIndex, "indexImplTable"),
		},
		"vector": {
			path.Join(tableName, vectorIndex, "indexImplLevelTable"),
			path.Join(tableName, vectorIndex, "indexImplPostingTable"),
		},
		"vector-level": {path.Join(tableName, vectorIndex, "indexImplLevelTable")},
	}
	if scope == "all" {
		result := append([]string{}, groups["base"]...)
		result = append(result, groups["fulltext"]...)
		return append(result, groups["vector"]...), nil
	}
	result, ok := groups[scope]
	if !ok {
		return nil, fmt.Errorf("partition scope must be base, fulltext, vector, vector-level or all")
	}
	return result, nil
}

func (s *Store) Fulltext(ctx context.Context, query model.Query) (int, int, error) {
	yql := onlineFulltextYQL(s.cfg, "DECLARE $query AS Utf8;")
	ids, retries, err := s.selectDocIDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextWorkloadText()))),
	))
	return len(ids), retries, err
}

// ProfileFulltext executes the production full-text query and asks YDB for
// basic execution statistics. It is intentionally separate from the live
// workload path: collecting query statistics has overhead and per-query qids
// must not become Prometheus labels.
func (s *Store) ProfileFulltext(ctx context.Context, query model.Query, limit int) (model.QueryExecutionStats, error) {
	if limit < 1 {
		return model.QueryExecutionStats{}, fmt.Errorf("fulltext diagnostic limit must be positive")
	}
	score := workloadFulltextScore("$query")
	yql := fmt.Sprintf(`DECLARE $query AS Utf8;
SELECT docid, %s AS relevance FROM %s VIEW %s
WHERE %s > 0
ORDER BY relevance DESC, docid ASC LIMIT %d;`, score, quote(s.cfg.TablePath), quote(s.cfg.FulltextIndex), score, limit)
	return s.selectDocIDsWithStats(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextWorkloadText()))),
	))
}

func (s *Store) ProfileFulltextVariant(ctx context.Context, query model.Query, cfg model.FulltextConfig, limit int) (model.QueryExecutionStats, error) {
	if limit < 1 {
		return model.QueryExecutionStats{}, fmt.Errorf("fulltext diagnostic limit must be positive")
	}
	for _, identifier := range []string{cfg.Index, cfg.Column} {
		if identifier == "" || strings.ContainsAny(identifier, "\"`;\r\n\\") {
			return model.QueryExecutionStats{}, fmt.Errorf("unsafe fulltext identifier %q", identifier)
		}
	}
	tablePath, err := s.resolveTable(cfg.Table)
	if err != nil {
		return model.QueryExecutionStats{}, err
	}
	queryText, err := selectFulltextQuery(query, cfg.QueryTransform)
	if err != nil {
		return model.QueryExecutionStats{}, err
	}
	score, err := fulltextScore(cfg.Column, "$query", cfg.MinimumShouldMatch, cfg.K1, cfg.B)
	if err != nil {
		return model.QueryExecutionStats{}, err
	}
	yql := fmt.Sprintf(`DECLARE $query AS Utf8;
SELECT docid, %s AS relevance FROM %s VIEW %s
WHERE %s > 0
ORDER BY relevance DESC, docid ASC LIMIT %d;`, score, quote(tablePath), quote(cfg.Index), score, limit)
	return s.selectDocIDsWithStats(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(queryText)),
	))
}

func (s *Store) Vector(ctx context.Context, query model.Query) (int, int, error) {
	yql := onlineVectorYQL(s.cfg, "DECLARE $target AS String;")
	ids, retries, err := s.selectDocIDs(ctx, yql, table.NewQueryParameters(table.ValueParam("$target", types.BytesValue(query.Embedding))))
	return len(ids), retries, err
}

func (s *Store) Hybrid(ctx context.Context, query model.Query) (int, int, error) {
	yql := onlineHybridYQL(s.cfg, "DECLARE $query AS Utf8; DECLARE $target AS String;")
	params := table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextText()))),
		table.ValueParam("$target", types.BytesValue(query.Embedding)),
	)
	ids, retries, err := s.selectDocIDs(ctx, yql, params)
	return len(ids), retries, err
}

// The online builders below are shared by the live workload and the generated
// YQL files used in the browser demo. Only the parameter preamble differs:
// the driver uses DECLARE while the UI files contain literal demo values.
func onlineFulltextYQL(cfg config.Config, parameterPreamble string) string {
	score := workloadFulltextScore("$query")
	return fmt.Sprintf(`%s
SELECT docid, %s AS relevance FROM %s VIEW %s
WHERE %s > 0
ORDER BY relevance DESC, docid ASC LIMIT 10;`, parameterPreamble, score, quote(cfg.TablePath), quote(cfg.FulltextIndex), score)
}

func onlineVectorYQL(cfg config.Config, parameterPreamble string) string {
	return onlineVectorYQLWithTarget(cfg, parameterPreamble, "$target")
}

func onlineVectorYQLWithTarget(cfg config.Config, parameterPreamble, targetExpression string) string {
	return fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
%s
SELECT docid FROM %s VIEW %s
ORDER BY Knn::InnerProductSimilarity(embedding, %s) DESC LIMIT %d;`, cfg.KMeansSearchTopSize, parameterPreamble, quote(cfg.TablePath), quote(cfg.VectorIndex), targetExpression, OnlineVectorLimit)
}

func onlineHybridYQL(cfg config.Config, parameterPreamble string) string {
	return onlineHybridYQLWithTarget(cfg, parameterPreamble, "$target")
}

func onlineHybridYQLWithTarget(cfg config.Config, parameterPreamble, targetExpression string) string {
	score := productionFulltextScore("$query")
	return fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
%s
SELECT docid FROM %s
ORDER BY HybridRank(
    %s,
    Knn::InnerProductSimilarity(embedding, %s),
    %q AS Mode,
    (%.3f, %.1f) AS Weights,
    %t AS Normalize,
    ("%s", "%s") AS Indexes,
    (%d, %d) AS Limits
) LIMIT 10;`, cfg.KMeansSearchTopSize, parameterPreamble, quote(cfg.TablePath), score, targetExpression,
		HybridMode, HybridFulltextWeight, HybridVectorWeight, HybridNormalize,
		cfg.FulltextIndex, cfg.VectorIndex, OnlineHybridBranchLimit, OnlineHybridBranchLimit)
}

func (s *Store) VectorDocIDs(ctx context.Context, query model.Query) ([]string, int, error) {
	return s.VectorDocIDsLimit(ctx, query, 30)
}

func (s *Store) FulltextDocIDsLimit(ctx context.Context, query model.Query, limit int) ([]string, int, error) {
	if limit < 1 {
		return nil, 0, fmt.Errorf("fulltext limit must be positive")
	}
	score := productionFulltextScore("$query")
	yql := fmt.Sprintf(`DECLARE $query AS Utf8;
SELECT docid, %s AS relevance FROM %s VIEW %s
WHERE %s > 0
ORDER BY relevance DESC, docid ASC LIMIT %d;`, score, quote(s.cfg.TablePath), quote(s.cfg.FulltextIndex), score, limit)
	return s.selectDocIDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextText()))),
	))
}

func (s *Store) FulltextDocIDsVariant(ctx context.Context, query model.Query, cfg model.FulltextConfig, limit int) ([]string, int, error) {
	if limit < 1 {
		return nil, 0, fmt.Errorf("fulltext limit must be positive")
	}
	for _, identifier := range []string{cfg.Index, cfg.Column} {
		if identifier == "" || strings.ContainsAny(identifier, "\"`;\r\n\\") {
			return nil, 0, fmt.Errorf("unsafe fulltext identifier %q", identifier)
		}
	}
	if cfg.MinimumShouldMatch == "" || strings.ContainsAny(cfg.MinimumShouldMatch, "\"'`;\r\n\\") {
		return nil, 0, fmt.Errorf("unsafe minimum_should_match %q", cfg.MinimumShouldMatch)
	}
	tablePath, err := s.resolveTable(cfg.Table)
	if err != nil {
		return nil, 0, err
	}
	queryText, err := selectFulltextQuery(query, cfg.QueryTransform)
	if err != nil {
		return nil, 0, err
	}
	score, err := fulltextScore(cfg.Column, "$query", cfg.MinimumShouldMatch, cfg.K1, cfg.B)
	if err != nil {
		return nil, 0, err
	}
	yql := fmt.Sprintf(`DECLARE $query AS Utf8;
SELECT docid, %s AS relevance FROM %s VIEW %s
WHERE %s > 0
ORDER BY relevance DESC, docid ASC LIMIT %d;`, score, quote(tablePath), quote(cfg.Index), score, limit)
	return s.selectDocIDs(ctx, yql, table.NewQueryParameters(table.ValueParam("$query", types.UTF8Value(queryText))))
}

func fulltextScore(column, query, minimumShouldMatch string, k1, b float64) (string, error) {
	if minimumShouldMatch == "" || strings.ContainsAny(minimumShouldMatch, "\"'`;\r\n\\") {
		return "", fmt.Errorf("unsafe minimum_should_match %q", minimumShouldMatch)
	}
	options := fmt.Sprintf(`"Or" AS DefaultOperator, "%s" AS MinimumShouldMatch`, minimumShouldMatch)
	if k1 > 0 {
		options += fmt.Sprintf(", %.6f AS K1", k1)
	}
	if b > 0 {
		options += fmt.Sprintf(", %.6f AS B", b)
	}
	return fmt.Sprintf("FullTextScore(%s, %s, %s)", quote(column), query, options), nil
}

func productionFulltextScore(query string) string {
	return fmt.Sprintf(
		`FullTextScore(text, %s, "Or" AS DefaultOperator, "%s" AS MinimumShouldMatch, %.1f AS K1, %.2f AS B)`,
		query, FulltextMinimumShouldMatch, FulltextK1, FulltextB,
	)
}

func workloadFulltextScore(query string) string {
	return fmt.Sprintf(
		`FullTextScore(text, %s, "Or" AS DefaultOperator, "%s" AS MinimumShouldMatch, %.1f AS K1, %.2f AS B)`,
		query, FulltextWorkloadMinimumShouldMatch, FulltextK1, FulltextB,
	)
}

func normalizeFulltextQuery(value string) string {
	normalized, _ := transformFulltextQuery(value, "strip-stress")
	return normalized
}

func SelectFulltextQuery(query model.Query, transform string) (string, error) {
	switch transform {
	case "workload":
		return normalizeFulltextQuery(query.FulltextWorkloadText()), nil
	case "lexical":
		return normalizeFulltextQuery(query.FulltextText()), nil
	case "lexical-required-1":
		return requireDiscriminativeTerms(normalizeFulltextQuery(query.FulltextText()), 1), nil
	case "lexical-required-2":
		return requireDiscriminativeTerms(normalizeFulltextQuery(query.FulltextText()), 2), nil
	case "lexical-required-entity":
		return requireEntityOrYearTerm(normalizeFulltextQuery(query.FulltextText())), nil
	case "original":
		return normalizeFulltextQuery(query.Text), nil
	default:
		// Keep the historical experiment modes applied to the original text.
		return transformFulltextQuery(query.Text, transform)
	}
}

// requireEntityOrYearTerm adds one anchor only when the query itself contains
// a strong, query-independent signal: a year/long number or a name-like token
// away from the sentence start. Queries without such a signal retain the
// measured production behavior instead of forcing a weak generic term.
func requireEntityOrYearTerm(value string) string {
	words := strings.Fields(value)
	type candidate struct {
		position int
		numeric  bool
		length   int
	}
	candidates := make([]candidate, 0, len(words))
	for position, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		numeric := len(runes) >= 3
		if numeric {
			for _, r := range runes {
				if !unicode.IsDigit(r) {
					numeric = false
					break
				}
			}
		}
		nameLike := position > 0 && len(runes) >= 4 && unicode.IsUpper(runes[0])
		if numeric || nameLike {
			candidates = append(candidates, candidate{position: position, numeric: numeric, length: len(runes)})
		}
	}
	if len(candidates) == 0 {
		return value
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].numeric != candidates[j].numeric {
			return candidates[i].numeric
		}
		if candidates[i].length != candidates[j].length {
			return candidates[i].length > candidates[j].length
		}
		return candidates[i].position < candidates[j].position
	})
	words[candidates[0].position] = "+" + words[candidates[0].position]
	return strings.Join(words, " ")
}

func selectFulltextQuery(query model.Query, transform string) (string, error) {
	return SelectFulltextQuery(query, transform)
}

// requireDiscriminativeTerms converts a lexical query to YDB's required-term
// syntax without consulting qrels or result documents. Numbers are preferred,
// then capitalized name-like tokens, then longer tokens; ties keep source
// order. This is deliberately only an experiment transform until measured on
// the complete query set.
func requireDiscriminativeTerms(value string, count int) string {
	words := strings.Fields(value)
	if count <= 0 || len(words) == 0 {
		return value
	}
	type candidate struct {
		position    int
		numeric     bool
		capitalized bool
		length      int
	}
	candidates := make([]candidate, len(words))
	for position, word := range words {
		runes := []rune(word)
		numeric := len(runes) > 0
		for _, r := range runes {
			if !unicode.IsDigit(r) {
				numeric = false
				break
			}
		}
		candidates[position] = candidate{
			position:    position,
			numeric:     numeric,
			capitalized: len(runes) > 0 && unicode.IsUpper(runes[0]),
			length:      len(runes),
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.numeric != right.numeric {
			return left.numeric
		}
		if left.capitalized != right.capitalized {
			return left.capitalized
		}
		if left.length != right.length {
			return left.length > right.length
		}
		return left.position < right.position
	})
	required := make(map[int]struct{}, min(count, len(words)))
	for _, item := range candidates[:min(count, len(candidates))] {
		required[item.position] = struct{}{}
	}
	for position := range words {
		if _, ok := required[position]; ok {
			words[position] = "+" + words[position]
		}
	}
	return strings.Join(words, " ")
}

func transformFulltextQuery(value, transform string) (string, error) {
	switch transform {
	case "", "none":
		return value, nil
	case "strip-stress":
		decomposed := norm.NFD.String(value)
		return norm.NFC.String(strings.Map(func(r rune) rune {
			if r == '\u0301' {
				return -1
			}
			return r
		}, decomposed)), nil
	case "ru-question-words":
		stop := map[string]struct{}{
			"как": {}, "какая": {}, "какие": {}, "какой": {}, "какое": {}, "какую": {}, "какого": {}, "какому": {}, "каким": {}, "каком": {}, "каких": {}, "какими": {},
			"каков": {}, "какова": {}, "каковы": {}, "когда": {}, "кто": {}, "где": {}, "куда": {}, "откуда": {}, "почему": {}, "зачем": {}, "сколько": {},
			"что": {}, "чего": {}, "чему": {}, "чем": {}, "ли": {},
		}
		words := strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		kept := words[:0]
		for _, word := range words {
			if _, remove := stop[strings.ToLower(word)]; !remove {
				kept = append(kept, word)
			}
		}
		if len(kept) == 0 {
			return value, nil
		}
		return strings.Join(kept, " "), nil
	default:
		return "", fmt.Errorf("unsupported query transform %q", transform)
	}
}

func (s *Store) VectorDocIDsLimit(ctx context.Context, query model.Query, limit int) ([]string, int, error) {
	if limit < 1 {
		return nil, 0, fmt.Errorf("vector limit must be positive")
	}
	yql := fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
DECLARE $target AS String;
SELECT docid FROM %s VIEW %s
ORDER BY Knn::InnerProductSimilarity(embedding, $target) DESC LIMIT %d;`, s.cfg.KMeansSearchTopSize, quote(s.cfg.TablePath), quote(s.cfg.VectorIndex), limit)
	return s.selectDocIDs(ctx, yql, table.NewQueryParameters(table.ValueParam("$target", types.BytesValue(query.Embedding))))
}

func (s *Store) HybridDocIDs(ctx context.Context, query model.Query) ([]string, int, error) {
	return s.HybridDocIDsLimit(ctx, query, 10)
}

func (s *Store) HybridDocIDsLimit(ctx context.Context, query model.Query, limit int) ([]string, int, error) {
	if limit < 1 {
		return nil, 0, fmt.Errorf("hybrid limit must be positive")
	}
	score := productionFulltextScore("$query")
	yql := fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
DECLARE $query AS Utf8; DECLARE $target AS String;
SELECT docid FROM %s
ORDER BY HybridRank(
    %s,
    Knn::InnerProductSimilarity(embedding, $target),
    %q AS Mode,
    (%.3f, %.1f) AS Weights,
    %t AS Normalize,
    ("%s", "%s") AS Indexes,
    (%d, %d) AS Limits
) LIMIT %d;`, s.cfg.KMeansSearchTopSize, quote(s.cfg.TablePath), score,
		HybridMode, HybridFulltextWeight, HybridVectorWeight, HybridNormalize,
		s.cfg.FulltextIndex, s.cfg.VectorIndex, HybridBranchLimit, HybridBranchLimit, limit)
	return s.selectDocIDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(normalizeFulltextQuery(query.FulltextText()))),
		table.ValueParam("$target", types.BytesValue(query.Embedding)),
	))
}

func (s *Store) HybridDocIDsVariant(ctx context.Context, query model.Query, cfg model.HybridConfig, limit int) ([]string, int, error) {
	if limit < 1 || cfg.BranchLimit < 1 || cfg.FulltextWeight <= 0 || cfg.VectorWeight <= 0 {
		return nil, 0, fmt.Errorf("hybrid limits and weights must be positive")
	}
	mode := strings.ToLower(cfg.Mode)
	if mode == "" {
		mode = "rrf"
	}
	if mode != "rrf" && mode != "linear" {
		return nil, 0, fmt.Errorf("hybrid mode must be rrf or linear")
	}
	if mode == "rrf" && cfg.RRFK <= 0 {
		return nil, 0, fmt.Errorf("hybrid RRF K must be positive")
	}
	for _, identifier := range []string{cfg.FulltextIndex, cfg.FulltextColumn, cfg.VectorIndex, cfg.VectorColumn} {
		if identifier == "" || strings.ContainsAny(identifier, "\"`;\r\n\\") {
			return nil, 0, fmt.Errorf("unsafe hybrid identifier %q", identifier)
		}
	}
	tablePath, err := s.resolveTable(cfg.Table)
	if err != nil {
		return nil, 0, err
	}
	queryText, err := selectFulltextQuery(query, cfg.QueryTransform)
	if err != nil {
		return nil, 0, err
	}
	score, err := fulltextScore(cfg.FulltextColumn, "$query", cfg.MinimumShouldMatch, cfg.K1, cfg.B)
	if err != nil {
		return nil, 0, err
	}
	fusion := fmt.Sprintf(`"%s" AS Mode,
    (%.6f, %.6f) AS Weights,`, mode, cfg.FulltextWeight, cfg.VectorWeight)
	if mode == "rrf" {
		fusion += fmt.Sprintf("\n    %.6f AS K,", cfg.RRFK)
	} else {
		fusion += fmt.Sprintf("\n    %t AS Normalize,", cfg.Normalize)
	}
	yql := fmt.Sprintf(`PRAGMA ydb.KMeansTreeSearchTopSize = "%d";
DECLARE $query AS Utf8; DECLARE $target AS String;
SELECT docid FROM %s
ORDER BY HybridRank(
    %s,
    Knn::InnerProductSimilarity(%s, $target),
    %s
    ("%s", "%s") AS Indexes,
    (%d, %d) AS Limits
) LIMIT %d;`, s.cfg.KMeansSearchTopSize, quote(tablePath), score, quote(cfg.VectorColumn), fusion,
		cfg.FulltextIndex, cfg.VectorIndex,
		cfg.BranchLimit, cfg.BranchLimit, limit)
	return s.selectDocIDs(ctx, yql, table.NewQueryParameters(
		table.ValueParam("$query", types.UTF8Value(queryText)),
		table.ValueParam("$target", types.BytesValue(query.Embedding)),
	))
}

func (s *Store) selectDocIDs(ctx context.Context, yql string, params *table.QueryParameters) ([]string, int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	attempts := 0
	ids := make([]string, 0, 10)
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		attempts++
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
			var docID string
			if err := result.ScanNamed(named.Required("docid", &docID)); err != nil {
				return err
			}
			ids = append(ids, docID)
		}
		return result.Err()
	}, table.WithIdempotent())
	return ids, retryCount(attempts), err
}

func (s *Store) selectDocIDsWithStats(ctx context.Context, yql string, params *table.QueryParameters) (model.QueryExecutionStats, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	started := time.Now()
	attempts := 0
	profile := model.QueryExecutionStats{}
	err := s.db.Table().Do(requestCtx, func(ctx context.Context, session table.Session) error {
		attempts++
		profile = model.QueryExecutionStats{}
		_, result, err := session.Execute(
			ctx,
			table.OnlineReadOnlyTxControl(),
			yql,
			params,
			tableoptions.WithCollectStatsModeBasic(),
		)
		if err != nil {
			return err
		}
		defer result.Close()
		if err := result.NextResultSetErr(ctx); err != nil {
			return err
		}
		for result.NextRow() {
			var docID string
			var relevance float64
			if err := result.ScanNamed(
				named.Required("docid", &docID),
				named.Required("relevance", &relevance),
			); err != nil {
				return err
			}
			profile.ResultCount++
		}
		if err := result.Err(); err != nil {
			return err
		}
		stats := result.Stats()
		if stats == nil {
			return fmt.Errorf("YDB returned no query statistics")
		}
		profile.ServerDuration = stats.TotalDuration()
		profile.CPUTime = stats.TotalCPUTime()
		byTable := make(map[string]*model.QueryTableAccess)
		for phase, ok := stats.NextPhase(); ok; phase, ok = stats.NextPhase() {
			for access, ok := phase.NextTableAccess(); ok; access, ok = phase.NextTableAccess() {
				item := byTable[access.Name]
				if item == nil {
					item = &model.QueryTableAccess{Name: access.Name}
					byTable[access.Name] = item
				}
				item.Rows += access.Reads.Rows
				item.Bytes += access.Reads.Bytes
				profile.ReadRows += access.Reads.Rows
				profile.ReadBytes += access.Reads.Bytes
				if strings.HasSuffix(access.Name, "/indexImplDocsTable") {
					profile.ScoredDocumentRows += access.Reads.Rows
				}
			}
		}
		profile.TableAccesses = make([]model.QueryTableAccess, 0, len(byTable))
		for _, access := range byTable {
			profile.TableAccesses = append(profile.TableAccesses, *access)
		}
		sort.Slice(profile.TableAccesses, func(i, j int) bool {
			return profile.TableAccesses[i].Name < profile.TableAccesses[j].Name
		})
		return nil
	}, table.WithIdempotent())
	profile.ClientDuration = time.Since(started)
	profile.Retries = retryCount(attempts)
	return profile, err
}

func retryCount(attempts int) int {
	if attempts <= 1 {
		return 0
	}
	return attempts - 1
}

func quote(value string) string { return "`" + value + "`" }
