// Package config persists non-secret freshen preferences.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is deliberately limited to preferences. Authentication stays in gh.
type Config struct {
	Workspace   string   `json:"workspace"`
	Owner       string   `json:"owner,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

var userConfigDir = os.UserConfigDir

func Path() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "freshen", "config.json"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func Save(c Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
