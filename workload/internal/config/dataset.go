package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type DatasetProfile struct {
	Size         uint64 `json:"size"`
	Seed         uint64 `json:"seed"`
	DocumentFile string `json:"document_file"`
	QueryFile    string `json:"query_file"`
	ExactFile    string `json:"exact_file,omitempty"`
	QualityFile  string `json:"quality_file,omitempty"`
}

type datasetConfig struct {
	Profiles map[string]DatasetProfile `json:"profiles"`
}

// ApplyDatasetProfile selects reproducible input artifacts while preserving
// the table, index, connection and workload settings from the stand config.
func (c Config) ApplyDatasetProfile(path, name string) (Config, error) {
	if name == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read dataset config: %w", err)
	}
	var catalog datasetConfig
	if err := json.Unmarshal(data, &catalog); err != nil {
		return Config{}, fmt.Errorf("parse dataset config: %w", err)
	}
	profile, ok := catalog.Profiles[name]
	if !ok {
		return Config{}, fmt.Errorf("unknown dataset profile %q", name)
	}
	if profile.Size == 0 || profile.DocumentFile == "" || profile.QueryFile == "" {
		return Config{}, fmt.Errorf("dataset profile %q is incomplete", name)
	}
	base := filepath.Dir(path)
	c.DocumentFile = resolveDatasetPath(base, profile.DocumentFile)
	c.QueryFile = resolveDatasetPath(base, profile.QueryFile)
	if profile.ExactFile != "" {
		c.Quality.ExactFile = resolveDatasetPath(base, profile.ExactFile)
	}
	if profile.QualityFile != "" {
		c.Quality.ResultFile = resolveDatasetPath(base, profile.QualityFile)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("dataset profile %q: %w", name, err)
	}
	return c, nil
}

func resolveDatasetPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
