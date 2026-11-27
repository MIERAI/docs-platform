// Package config is the fixture's configuration.
package config

import "time"

// Config is the server configuration.
type Config struct {
	// Retention is how long finished orders are kept.
	Retention time.Duration `yaml:"retention"`
	// BatchSize is how many orders are written per transaction.
	BatchSize int `yaml:"batch_size"`
}

// New returns the default configuration.
func New() Config {
	return Config{
		Retention: 72 * time.Hour,
		BatchSize: 100,
	}
}
