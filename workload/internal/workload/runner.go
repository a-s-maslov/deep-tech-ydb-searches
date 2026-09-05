package workload

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

type Backend interface {
	Fulltext(context.Context, model.Query) (int, int, error)
	Vector(context.Context, model.Query) (int, int, error)
	Hybrid(context.Context, model.Query) (int, int, error)
	UpsertDocument(context.Context, model.Document) (int, error)
	VerifyFulltextMarker(context.Context, string, string) (int, error)
}

type Runner struct {
	cfg       config.Config
	backend   Backend
	queries   []model.Query
	documents []model.Document
	stats     map[string]*Stats
	started   time.Time
	errorMu   sync.Mutex
	lastError map[string]time.Time
	sequence  atomic.Uint64
}

func New(cfg config.Config, backend Backend, queries []model.Query, documents []model.Document) *Runner {
	stats := map[string]*Stats{}
	for name := range cfg.Scenarios() {
		stats[name] = &Stats{}
	}
	if cfg.DMLEnabled() {
		stats["write"] = &Stats{}
		stats["read_after_write"] = &Stats{}
		stats["dml_check"] = &Stats{}
	}
	return &Runner{cfg: cfg, backend: backend, queries: queries, documents: documents, stats: stats, lastError: map[string]time.Time{}}
}

func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.DMLEnabled() && len(r.documents) != int(r.cfg.DML.PoolSize) {
		return fmt.Errorf("dml document pool contains %d rows, want %d", len(r.documents), r.cfg.DML.PoolSize)
	}
	r.started = time.Now()
	interval, _ := r.cfg.MetricsIntervalDuration()
	metrics, err := NewMetrics(r.cfg.Metrics.Application, r.cfg.Metrics.ListenAddress)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Close(closeCtx)
	}()
	var group sync.WaitGroup
	enabled := make([]string, 0, 4)
	for name, scenario := range r.cfg.Scenarios() {
		if scenario.RPS == 0 && (scenario.EndRPS == nil || *scenario.EndRPS == 0) {
			continue
		}
		enabled = append(enabled, name)
		name, scenario := name, scenario
		group.Add(1)
		go func() { defer group.Done(); r.runScenario(ctx, name, scenario) }()
	}
	if r.cfg.DMLEnabled() {
		enabled = append(enabled, "write", "read_after_write", "dml_check")
		group.Add(1)
		go func() { defer group.Done(); r.runVerifiedWrites(ctx) }()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := time.Now()
		for {
			select {
			case now := <-ticker.C:
				values := make([]Snapshot, 0, len(enabled))
				for _, name := range enabled {
					target := r.cfg.TargetRPS(name, now.Sub(r.started))
					if name == "write" || name == "read_after_write" || name == "dml_check" {
						target = r.cfg.DML.RPS
					}
					values = append(values, r.stats[name].SnapshotAndReset(name, now.Sub(last), target))
				}
				metrics.Write(values)
				last = now
			case <-ctx.Done():
				return
			}
		}
	}()
	<-ctx.Done()
	group.Wait()
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func (r *Runner) runVerifiedWrites(ctx context.Context) {
	jobs := make(chan struct{}, r.cfg.DML.Workers*2)
	var workers sync.WaitGroup
	for worker := 0; worker < r.cfg.DML.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range jobs {
				sequence := r.sequence.Add(1)
				slot := (sequence - 1) % uint64(len(r.documents))
				document, marker := verifiedWriteDocument(r.cfg, sequence, slot, r.documents[slot])

				cycleStarted := time.Now()
				started := time.Now()
				retries, err := r.backend.UpsertDocument(ctx, document)
				if err != nil {
					r.logError("write", document.DocID, err)
				}
				r.stats["write"].Record(time.Since(started), retries, err)
				if err != nil {
					r.stats["dml_check"].Record(time.Since(cycleStarted), retries, err)
					continue
				}

				totalRetries := retries
				started = time.Now()
				retries, err = r.backend.VerifyFulltextMarker(ctx, marker, document.DocID)
				totalRetries += retries
				if err != nil {
					r.logError("read_after_write", document.DocID, err)
				}
				r.stats["read_after_write"].Record(time.Since(started), retries, err)
				r.stats["dml_check"].Record(time.Since(cycleStarted), totalRetries, err)
			}
		}()
	}

	next := time.Now()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	interval := time.Duration(float64(time.Second) / r.cfg.DML.RPS)
	for {
		next = next.Add(interval)
		if now := time.Now(); next.Before(now) {
			next = now.Add(interval)
		}
		timer.Reset(time.Until(next))
		select {
		case <-timer.C:
			select {
			case jobs <- struct{}{}:
			default:
				r.stats["write"].Drop()
				r.stats["dml_check"].Drop()
			}
		case <-ctx.Done():
			timer.Stop()
			close(jobs)
			workers.Wait()
			return
		}
	}
}

func verifiedWriteDocument(cfg config.Config, sequence, slot uint64, source model.Document) (model.Document, string) {
	marker := "workshopmarker" + alphabeticToken(sequence)
	docID := fmt.Sprintf("workshop-live-%06d", slot)
	title := strings.TrimSpace(source.Title)
	text := strings.TrimSpace(source.Text)
	if title == "" {
		title = "Workshop document"
	}
	if text == "" {
		text = title
	}
	return model.Document{
		ID:        cfg.DML.IDStart + slot,
		DocID:     docID,
		Title:     title,
		Text:      text + " " + marker,
		Embedding: source.Embedding,
	}, marker
}

func alphabeticToken(value uint64) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, 0, 14)
	for {
		result = append(result, alphabet[value%uint64(len(alphabet))])
		value /= uint64(len(alphabet))
		if value == 0 {
			break
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return string(result)
}

func (r *Runner) runScenario(ctx context.Context, name string, scenario config.Scenario) {
	// Search traffic is open-loop: a scheduled operation starts immediately or
	// is counted as dropped when all workers are busy. An unbuffered channel is
	// intentional — retaining stale jobs would produce a post-recovery burst and
	// measure backlog drain instead of the cluster's current capacity.
	jobs := make(chan struct{})
	r.stats[name].ConfigureConcurrency(scenario.Workers)
	var workers sync.WaitGroup
	for worker := 0; worker < scenario.Workers; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			random := rand.New(rand.NewSource(scenarioSeed(name, worker)))
			for range jobs {
				r.stats[name].Begin()
				query := r.queries[random.Intn(len(r.queries))]
				started := time.Now()
				retries, err := r.execute(ctx, name, query)
				if err != nil {
					r.logError(name, query.QID, err)
				}
				r.stats[name].Record(time.Since(started), retries, err)
				r.stats[name].End()
			}
		}(worker)
	}
	next := time.Now()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		target := r.cfg.TargetRPS(name, time.Since(r.started))
		if target < .001 {
			target = .001
		}
		interval := time.Duration(float64(time.Second) / target)
		next = next.Add(interval)
		if now := time.Now(); next.Before(now) {
			next = now.Add(interval)
		}
		timer.Reset(time.Until(next))
		select {
		case <-timer.C:
			select {
			case jobs <- struct{}{}:
			default:
				r.stats[name].Drop()
			}
		case <-ctx.Done():
			timer.Stop()
			close(jobs)
			workers.Wait()
			return
		}
	}
}

func scenarioSeed(name string, worker int) int64 {
	// Keep workload runs reproducible while giving every scenario an independent
	// query sequence. Using len(name) here is insufficient: "vector" and
	// "hybrid" have the same length and would select the same queries in lockstep.
	var scenario int64
	for _, value := range []byte(name) {
		scenario = scenario*131 + int64(value)
	}
	return 20260825 + int64(worker)*101 + scenario
}

func (r *Runner) logError(scenario, qid string, err error) {
	r.errorMu.Lock()
	defer r.errorMu.Unlock()
	now := time.Now()
	if last := r.lastError[scenario]; !last.IsZero() && now.Sub(last) < 5*time.Second {
		return
	}
	r.lastError[scenario] = now
	message := err.Error()
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	log.Printf("scenario=%s qid=%s: %s", scenario, qid, message)
}

func (r *Runner) execute(ctx context.Context, name string, query model.Query) (int, error) {
	switch name {
	case "fulltext":
		_, retries, err := r.backend.Fulltext(ctx, query)
		return retries, err
	case "vector":
		_, retries, err := r.backend.Vector(ctx, query)
		return retries, err
	case "hybrid":
		_, retries, err := r.backend.Hybrid(ctx, query)
		return retries, err
	default:
		return 0, fmt.Errorf("unknown scenario %q", name)
	}
}
