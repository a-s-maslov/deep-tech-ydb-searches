package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultTablePath = "deep_tech_search_documents"

type Scenario struct {
	RPS     float64  `json:"rps"`
	EndRPS  *float64 `json:"end_rps,omitempty"`
	Workers int      `json:"workers"`
}

type Ramp struct {
	Duration        string  `json:"duration,omitempty"`
	StartMultiplier float64 `json:"start_multiplier,omitempty"`
	EndMultiplier   float64 `json:"end_multiplier,omitempty"`
}

type Workload struct {
	Fulltext Scenario `json:"fulltext"`
	Vector   Scenario `json:"vector"`
	Hybrid   Scenario `json:"hybrid"`
	Ramp     Ramp     `json:"ramp,omitempty"`
}

type DML struct {
	Mode     string  `json:"mode,omitempty"`
	RPS      float64 `json:"rps,omitempty"`
	Workers  int     `json:"workers,omitempty"`
	IDStart  uint64  `json:"id_start,omitempty"`
	PoolSize uint64  `json:"pool_size,omitempty"`
}

type Metrics struct {
	Application   string `json:"application"`
	ListenAddress string `json:"listen_address"`
	Interval      string `json:"interval"`
}

type Observer struct {
	ListenAddress string `json:"listen_address,omitempty"`
	Interval      string `json:"interval,omitempty"`
}

type Quality struct {
	ExactFile  string `json:"exact_file,omitempty"`
	ResultFile string `json:"result_file,omitempty"`
	Workers    int    `json:"workers,omitempty"`
}

type Partitioning struct {
	BaseMinPartitions     uint64 `json:"base_min_partitions,omitempty"`
	FulltextMinPartitions uint64 `json:"fulltext_min_partitions,omitempty"`
	VectorMinPartitions   uint64 `json:"vector_min_partitions,omitempty"`
}

type Config struct {
	ConnectionString    string              `json:"connection_string"`
	NodeAddressOverride string              `json:"node_address_override,omitempty"`
	Token               string              `json:"token,omitempty"`
	Anonymous           bool                `json:"anonymous"`
	CAFile              string              `json:"ca_file,omitempty"`
	QueryFile           string              `json:"query_file"`
	DocumentFile        string              `json:"document_file"`
	TablePath           string              `json:"table_path"`
	FulltextIndex       string              `json:"fulltext_index"`
	VectorIndex         string              `json:"vector_index"`
	VectorDimension     int                 `json:"vector_dimension"`
	KMeansSearchTopSize int                 `json:"kmeans_search_top_size"`
	SessionPoolSize     int                 `json:"session_pool_size,omitempty"`
	SessionPoolUsage    uint64              `json:"session_pool_usage_limit,omitempty"`
	RequestTimeout      string              `json:"request_timeout"`
	AdminTimeout        string              `json:"admin_timeout"`
	Partitioning        Partitioning        `json:"partitioning,omitempty"`
	Metrics             Metrics             `json:"metrics"`
	Observer            Observer            `json:"observer,omitempty"`
	Quality             Quality             `json:"quality,omitempty"`
	DML                 DML                 `json:"dml,omitempty"`
	Workload            Workload            `json:"workload"`
	Profiles            map[string]Workload `json:"profiles,omitempty"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if value := os.Getenv("YDB_CONNECTION_STRING"); value != "" {
		cfg.ConnectionString = value
	}
	if value := os.Getenv("YDB_NODE_ADDRESS_OVERRIDE"); value != "" {
		cfg.NodeAddressOverride = value
	}
	if value := os.Getenv("YDB_TOKEN"); value != "" {
		cfg.Token, cfg.Anonymous = value, false
	}
	if value := os.Getenv("YDB_CA_FILE"); value != "" {
		cfg.CAFile = value
	}
	if value := os.Getenv("WORKLOAD_QUERY_FILE"); value != "" {
		cfg.QueryFile = value
	}
	if value := os.Getenv("WORKLOAD_DOCUMENT_FILE"); value != "" {
		cfg.DocumentFile = value
	}
	if value := os.Getenv("WORKLOAD_TABLE_PATH"); value != "" {
		cfg.TablePath = value
	}
	if value := os.Getenv("WORKLOAD_FULLTEXT_INDEX"); value != "" {
		cfg.FulltextIndex = value
	}
	if value := os.Getenv("WORKLOAD_VECTOR_INDEX"); value != "" {
		cfg.VectorIndex = value
	}
	if value := os.Getenv("WORKLOAD_SESSION_POOL_SIZE"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse WORKLOAD_SESSION_POOL_SIZE: %w", err)
		}
		cfg.SessionPoolSize = parsed
	}
	if value := os.Getenv("WORKLOAD_SESSION_POOL_USAGE_LIMIT"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse WORKLOAD_SESSION_POOL_USAGE_LIMIT: %w", err)
		}
		cfg.SessionPoolUsage = parsed
	}
	if cfg.TablePath == "" {
		cfg.TablePath = DefaultTablePath
	}
	if value := os.Getenv("WORKLOAD_METRICS_LISTEN_ADDRESS"); value != "" {
		cfg.Metrics.ListenAddress = value
	}
	if cfg.QueryFile != "" && !filepath.IsAbs(cfg.QueryFile) {
		cfg.QueryFile = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.QueryFile))
	}
	if cfg.DocumentFile != "" && !filepath.IsAbs(cfg.DocumentFile) {
		cfg.DocumentFile = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.DocumentFile))
	}
	if cfg.Quality.ExactFile != "" && !filepath.IsAbs(cfg.Quality.ExactFile) {
		cfg.Quality.ExactFile = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Quality.ExactFile))
	}
	if cfg.Quality.ResultFile != "" && !filepath.IsAbs(cfg.Quality.ResultFile) {
		cfg.Quality.ResultFile = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Quality.ResultFile))
	}
	return cfg, cfg.Validate()
}

// ApplyProfile replaces only the workload section. All connection, schema,
// artifact and metrics settings remain environment-specific and continue to
// come from the same configuration file.
func (c Config) ApplyProfile(name string) (Config, error) {
	if name == "" {
		return c, nil
	}
	profile, ok := c.Profiles[name]
	if !ok {
		return Config{}, fmt.Errorf("unknown workload profile %q", name)
	}
	c.Workload = profile
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("workload profile %q: %w", name, err)
	}
	return c, nil
}

func (c Config) Validate() error {
	var problems []error
	if c.ConnectionString == "" || c.QueryFile == "" || c.DocumentFile == "" {
		problems = append(problems, errors.New("connection_string, query_file and document_file are required"))
	}
	for _, value := range []string{c.TablePath, c.FulltextIndex, c.VectorIndex} {
		if value == "" || strings.ContainsAny(value, "\"`;\r\n\\") {
			problems = append(problems, fmt.Errorf("unsafe schema identifier %q", value))
		}
	}
	if c.VectorDimension < 1 || c.KMeansSearchTopSize < 1 {
		problems = append(problems, errors.New("vector settings must be positive"))
	}
	if c.SessionPoolSize < 0 {
		problems = append(problems, errors.New("session_pool_size must not be negative"))
	}
	if (c.Quality.ExactFile == "") != (c.Quality.ResultFile == "") {
		problems = append(problems, errors.New("quality exact_file and result_file must be configured together"))
	}
	if c.Quality.Workers < 0 {
		problems = append(problems, errors.New("quality workers must not be negative"))
	}
	if c.Partitioning.BaseMinPartitions > 64 || c.Partitioning.FulltextMinPartitions > 64 || c.Partitioning.VectorMinPartitions > 64 {
		problems = append(problems, errors.New("partitioning minimums must not exceed the configured maximum of 64"))
	}
	if _, err := c.RequestTimeoutDuration(); err != nil {
		problems = append(problems, err)
	}
	if _, err := c.AdminTimeoutDuration(); err != nil {
		problems = append(problems, err)
	}
	if _, err := c.MetricsIntervalDuration(); err != nil {
		problems = append(problems, err)
	}
	if _, err := c.ObserverIntervalDuration(); err != nil {
		problems = append(problems, err)
	}
	if _, err := c.RampDuration(); err != nil {
		problems = append(problems, err)
	}
	for name, scenario := range c.Scenarios() {
		if scenario.RPS < 0 || scenario.Workers < 0 || (scenario.RPS > 0 && scenario.Workers == 0) {
			problems = append(problems, fmt.Errorf("invalid %s scenario", name))
		}
		if scenario.EndRPS != nil && (*scenario.EndRPS < 0 || (*scenario.EndRPS > 0 && scenario.Workers == 0)) {
			problems = append(problems, fmt.Errorf("invalid %s end_rps", name))
		}
	}
	if err := c.validateDML(); err != nil {
		problems = append(problems, err)
	}
	for profileName, profile := range c.Profiles {
		profileConfig := c
		profileConfig.Workload = profile
		profileConfig.Profiles = nil
		if err := profileConfig.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("invalid profile %q: %w", profileName, err))
		}
	}
	return errors.Join(problems...)
}

func (c Config) validateDML() error {
	switch c.DML.Mode {
	case "", "disabled":
		if c.DML.RPS != 0 || c.DML.Workers != 0 {
			return errors.New("dml mode is disabled but rps or workers are configured")
		}
		return nil
	case "verified":
		if c.DML.RPS <= 0 || c.DML.Workers <= 0 || c.DML.PoolSize == 0 {
			return errors.New("verified dml requires positive rps, workers and pool_size")
		}
		if c.DML.PoolSize > uint64(^uint(0)>>1) {
			return errors.New("dml pool_size does not fit into memory on this platform")
		}
		if c.DML.IDStart > ^uint64(0)-(c.DML.PoolSize-1) {
			return errors.New("dml id range overflows Uint64")
		}
		return nil
	default:
		return fmt.Errorf("unsupported dml mode %q", c.DML.Mode)
	}
}

func (c Config) DMLEnabled() bool { return c.DML.Mode == "verified" }

func (c Config) QualityWorkers() int {
	if c.Quality.Workers == 0 {
		return 16
	}
	return c.Quality.Workers
}

func (c Config) Scenarios() map[string]Scenario {
	return map[string]Scenario{"fulltext": c.Workload.Fulltext, "vector": c.Workload.Vector, "hybrid": c.Workload.Hybrid}
}

func parseDuration(name, value string, allowEmpty bool) (time.Duration, error) {
	if value == "" && allowEmpty {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return duration, nil
}

func (c Config) RequestTimeoutDuration() (time.Duration, error) {
	return parseDuration("request_timeout", c.RequestTimeout, false)
}
func (c Config) AdminTimeoutDuration() (time.Duration, error) {
	return parseDuration("admin_timeout", c.AdminTimeout, false)
}
func (c Config) MetricsIntervalDuration() (time.Duration, error) {
	return parseDuration("metrics.interval", c.Metrics.Interval, false)
}
func (c Config) ObserverListenAddress() string {
	if c.Observer.ListenAddress == "" {
		return "0.0.0.0:9092"
	}
	return c.Observer.ListenAddress
}
func (c Config) ObserverIntervalDuration() (time.Duration, error) {
	if c.Observer.Interval == "" {
		return 5 * time.Second, nil
	}
	return parseDuration("observer.interval", c.Observer.Interval, false)
}
func (c Config) RampDuration() (time.Duration, error) {
	return parseDuration("workload.ramp.duration", c.Workload.Ramp.Duration, true)
}

func (c Config) Multiplier(elapsed time.Duration) float64 {
	duration, _ := c.RampDuration()
	if duration == 0 {
		return 1
	}
	start, end := c.Workload.Ramp.StartMultiplier, c.Workload.Ramp.EndMultiplier
	if start <= 0 {
		start = 1
	}
	if end <= 0 {
		end = 1
	}
	if elapsed >= duration {
		return end
	}
	return start + (end-start)*float64(elapsed)/float64(duration)
}

// TargetRPS returns the target for one scenario. New configurations can ramp
// selected scenarios independently with end_rps, leaving the background load
// unchanged. Configurations using the legacy global multiplier retain their
// previous behavior.
func (c Config) TargetRPS(name string, elapsed time.Duration) float64 {
	scenario, ok := c.Scenarios()[name]
	if !ok {
		return 0
	}
	if scenario.EndRPS == nil {
		return scenario.RPS * c.Multiplier(elapsed)
	}
	duration, _ := c.RampDuration()
	if duration == 0 || elapsed >= duration {
		return *scenario.EndRPS
	}
	progress := float64(elapsed) / float64(duration)
	return scenario.RPS + (*scenario.EndRPS-scenario.RPS)*progress
}
