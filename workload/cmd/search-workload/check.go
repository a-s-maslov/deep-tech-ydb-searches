package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type checkBackend interface {
	CountDocuments(context.Context) (uint64, error)
	Fulltext(context.Context, model.Query) (int, int, error)
	VectorDocIDs(context.Context, model.Query) ([]string, int, error)
	HybridDocIDs(context.Context, model.Query) ([]string, int, error)
}

func runChecks(ctx context.Context, backend checkBackend, queries []model.Query, output io.Writer) error {
	if len(queries) == 0 {
		return errors.New("check requires at least one query")
	}
	var problems []error

	count, err := backend.CountDocuments(ctx)
	if err != nil {
		fmt.Fprintf(output, "count: ERROR: %v\n", err)
		problems = append(problems, fmt.Errorf("count check: %w", err))
	} else {
		fmt.Fprintf(output, "count: OK documents=%d\n", count)
	}

	fulltextRows := 0
	fulltextQID := ""
	fulltextFailed := false
	for i := 0; i < len(queries) && i < 25; i++ {
		rows, _, queryErr := backend.Fulltext(ctx, queries[i])
		if queryErr != nil {
			fmt.Fprintf(output, "fulltext: ERROR: %v\n", queryErr)
			problems = append(problems, fmt.Errorf("fulltext check: %w", queryErr))
			fulltextFailed = true
			break
		}
		if rows > 0 {
			fulltextRows, fulltextQID = rows, queries[i].QID
			break
		}
	}
	if !fulltextFailed {
		if fulltextRows == 0 {
			err := errors.New("no results in the first 25 queries")
			fmt.Fprintf(output, "fulltext: ERROR: %v\n", err)
			problems = append(problems, fmt.Errorf("fulltext check: %w", err))
		} else {
			fmt.Fprintf(output, "fulltext: OK rows=%d qid=%s\n", fulltextRows, fulltextQID)
		}
	}

	query := queries[0]
	vectorIDs, _, vectorErr := backend.VectorDocIDs(ctx, query)
	if vectorErr != nil {
		fmt.Fprintf(output, "vector: ERROR: %v\n", vectorErr)
		problems = append(problems, fmt.Errorf("vector check: %w", vectorErr))
	} else {
		vectorHits := relevantHits(vectorIDs, query.RelevantDocIDs)
		if len(vectorIDs) == 0 || vectorHits == 0 {
			err := fmt.Errorf("rows=%d qrel_hits=%d", len(vectorIDs), vectorHits)
			fmt.Fprintf(output, "vector: ERROR: %v\n", err)
			problems = append(problems, fmt.Errorf("vector check: %w", err))
		} else {
			fmt.Fprintf(output, "vector: OK rows=%d qrel_hits=%d qid=%s\n", len(vectorIDs), vectorHits, query.QID)
		}
	}

	hybridIDs, _, hybridErr := backend.HybridDocIDs(ctx, query)
	if hybridErr != nil {
		fmt.Fprintf(output, "hybrid: ERROR: %v\n", hybridErr)
		problems = append(problems, fmt.Errorf("hybrid check: %w", hybridErr))
	} else {
		hybridHits := relevantHits(hybridIDs, query.RelevantDocIDs)
		if len(hybridIDs) == 0 || hybridHits == 0 {
			err := fmt.Errorf("rows=%d qrel_hits=%d", len(hybridIDs), hybridHits)
			fmt.Fprintf(output, "hybrid: ERROR: %v\n", err)
			problems = append(problems, fmt.Errorf("hybrid check: %w", err))
		} else {
			fmt.Fprintf(output, "hybrid: OK rows=%d qrel_hits=%d qid=%s\n", len(hybridIDs), hybridHits, query.QID)
		}
	}

	return errors.Join(problems...)
}
