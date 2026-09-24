package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is the owner's config file. The zero value means no overrides.
type Config struct {
	Workers Workers `json:"workers"`
}

// Workers holds per-worker overrides.
type Workers struct {
	Codex  Worker `json:"codex"`
	Claude Worker `json:"claude"`
}

// Worker holds one worker's overrides. Binary is an absolute path or empty.
type Worker struct {
	Binary string `json:"binary,omitempty"`
}

// Load reads the config at path. A missing file means no overrides; anything
// else that is wrong with the file, including an unknown field, is an error
// naming the path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	c, err := parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

func parse(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, errors.New("file is empty; delete it or write {}")
		}
		return Config{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("unexpected data after the top-level JSON object")
	}
	if err := absOrEmpty("codex", c.Workers.Codex.Binary); err != nil {
		return Config{}, err
	}
	if err := absOrEmpty("claude", c.Workers.Claude.Binary); err != nil {
		return Config{}, err
	}
	return c, nil
}

func absOrEmpty(worker, binary string) error {
	if binary != "" && !filepath.IsAbs(binary) {
		return fmt.Errorf("workers.%s.binary must be an absolute path, got %q", worker, binary)
	}
	return nil
}
