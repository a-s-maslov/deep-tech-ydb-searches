package ydbstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

// DemoScript is a generated YQL file shown in the browser part of the demo.
type DemoScript struct {
	Name    string
	Content string
}

// DemoQueryCheck describes one literal, parameter-free query that is shown in
// YDB UI and has also been executed through the Table API.
type DemoQueryCheck struct {
	Name string
	Rows int
}

// DemoScripts renders DDL from the production schema builders and search YQL
// from the same online builders that are executed by the workload. Search
// parameters are inlined only because YDB UI does not bind driver parameters.
func DemoScripts(cfg config.Config, query model.Query) []DemoScript {
	embedding := base64.StdEncoding.EncodeToString(query.Embedding)
	targetExpression := fmt.Sprintf("Unwrap(String::Base64Decode(%q))", embedding)
	fulltextValue := normalizeFulltextQuery(query.FulltextWorkloadText())
	hybridValue := normalizeFulltextQuery(query.FulltextText())

	return []DemoScript{
		{
			Name:    "01-fulltext-index.yql",
			Content: fmt.Sprintf("-- DDL used by the workload schema. Do not execute during the demo.\n%s\n", FulltextDDL(cfg)),
		},
		{
			Name: "02-fulltext-query.yql",
			Content: onlineFulltextYQL(cfg, fmt.Sprintf(
				"-- Workload query qid=%s; source: %q\n$query = %qu;",
				query.QID, query.Text, fulltextValue,
			)) + "\n",
		},
		{
			Name:    "03-vector-index.yql",
			Content: fmt.Sprintf("-- DDL used by the workload schema. Do not execute during the demo.\n%s\n", VectorDDL(cfg)),
		},
		{
			Name: "04-vector-query.yql",
			Content: onlineVectorYQLWithTarget(cfg, fmt.Sprintf(
				"-- Workload query qid=%s; source: %q",
				query.QID, query.Text,
			), targetExpression) + "\n",
		},
		{
			Name: "05-hybrid-query.yql",
			Content: onlineHybridYQLWithTarget(cfg, fmt.Sprintf(
				"-- Workload query qid=%s; source: %q\n$query = %qu;",
				query.QID, query.Text, hybridValue,
			), targetExpression) + "\n",
		},
	}
}

// CheckDemoQueries executes the exact parameter-free YQL rendered for YDB UI.
// It complements the regular workload check, which validates the same query
// builders with driver-bound parameters.
func (s *Store) CheckDemoQueries(ctx context.Context, query model.Query) ([]DemoQueryCheck, error) {
	checks := make([]DemoQueryCheck, 0, 3)
	for _, script := range DemoScripts(s.cfg, query) {
		if !strings.HasPrefix(script.Name, "02-") &&
			!strings.HasPrefix(script.Name, "04-") &&
			!strings.HasPrefix(script.Name, "05-") {
			continue
		}
		ids, _, err := s.selectDocIDs(ctx, script.Content, nil)
		if err != nil {
			return checks, fmt.Errorf("execute %s: %w", script.Name, err)
		}
		if len(ids) == 0 {
			return checks, fmt.Errorf("execute %s: query returned no rows", script.Name)
		}
		checks = append(checks, DemoQueryCheck{Name: script.Name, Rows: len(ids)})
	}
	if len(checks) != 3 {
		return checks, fmt.Errorf("expected 3 demo queries, checked %d", len(checks))
	}
	return checks, nil
}
