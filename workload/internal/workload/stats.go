package workload

import (
	"sort"
	"sync"
	"time"
)

type Stats struct {
	mu                                  sync.Mutex
	successes, errors, retries, dropped int64
	inFlight, workerLimit               int64
	latencies                           []time.Duration
}

type Snapshot struct {
	Scenario                           string
	RPS, TargetRPS, P50, P95, P99, Max float64
	ErrorRPS, RetryRPS, DroppedRPS     float64
	Errors, Retries, Dropped           int64
	InFlight, WorkerLimit              int64
	HasData                            bool
}

func (s *Stats) ConfigureConcurrency(workers int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerLimit = int64(workers)
}

func (s *Stats) Begin() {
	s.mu.Lock()
	s.inFlight++
	s.mu.Unlock()
}

func (s *Stats) End() { s.mu.Lock(); s.inFlight--; s.mu.Unlock() }

func (s *Stats) Record(latency time.Duration, retries int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries += int64(retries)
	if err != nil {
		s.errors++
		return
	}
	s.successes++
	s.latencies = append(s.latencies, latency)
}

func (s *Stats) Drop() { s.mu.Lock(); s.dropped++; s.mu.Unlock() }

func (s *Stats) SnapshotAndReset(name string, elapsed time.Duration, target float64) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Snapshot{
		Scenario: name, TargetRPS: target,
		Errors: s.errors, Retries: s.retries, Dropped: s.dropped,
		InFlight: s.inFlight, WorkerLimit: s.workerLimit,
	}
	result.HasData = s.successes > 0 || s.errors > 0 || s.retries > 0 || s.dropped > 0 || s.inFlight > 0
	if elapsed > 0 {
		result.RPS = float64(s.successes) / elapsed.Seconds()
		result.ErrorRPS = float64(s.errors) / elapsed.Seconds()
		result.RetryRPS = float64(s.retries) / elapsed.Seconds()
		result.DroppedRPS = float64(s.dropped) / elapsed.Seconds()
	}
	if len(s.latencies) > 0 {
		sort.Slice(s.latencies, func(i, j int) bool { return s.latencies[i] < s.latencies[j] })
		at := func(f float64) float64 {
			return float64(s.latencies[int(float64(len(s.latencies)-1)*f)]) / float64(time.Millisecond)
		}
		result.P50, result.P95, result.P99 = at(.50), at(.95), at(.99)
		result.Max = float64(s.latencies[len(s.latencies)-1]) / float64(time.Millisecond)
	}
	s.successes, s.errors, s.retries, s.dropped = 0, 0, 0, 0
	s.latencies = s.latencies[:0]
	return result
}
