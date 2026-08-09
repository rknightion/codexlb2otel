package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads a YAML file over the top of Default(), so an absent key keeps its
// default rather than becoming a zero value - a config that only sets loki.token
// must not silently zero out loki.batch_size. It then validates the result and
// returns Validate's error unchanged; Validate already names every bad field, so
// there is nothing to add here.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return parse(b)
}

// LoadOverlay reads a YAML file over Default() WITHOUT running Validate, and treats a
// missing file as "no overlay" rather than an error.
//
// It exists for the oneshot tools. Validate enforces what the SERVICE needs - at least one
// sink enabled, fully credentialed - because a daemon that tails the archive and ships it
// nowhere is a silent no-op. clbsum runs no pipeline and pushes to no sink, so applying
// that rule to it would mean `clbsum -list` on a laptop demanded Loki credentials it never
// uses. Each tool validates the part of the config it actually reads: see
// Summarize.Validate.
func LoadOverlay(path string) (Config, error) {
	if path == "" {
		return Default(), nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse yaml: %w", err)
	}
	return cfg, nil
}

// parse is Load's body, split out so a test can exercise it without a file on disk.
func parse(b []byte) (Config, error) {
	cfg := Default()

	// Decoding into the already-Default()-populated cfg is what makes this an
	// overlay: yaml.v3, like encoding/json, only touches struct fields whose keys
	// are actually present in the document (an empty document is a no-op). A key
	// absent from the file leaves whatever Default() put there untouched.
	//
	// time.Duration fields (archive.poll_interval, loki.batch_wait, ...) take a
	// plain "5s"-style string with no help needed from this file: yaml.v3 special-
	// cases any field whose Go type is time.Duration and parses a string scalar with
	// time.ParseDuration - verified against the pinned v3.0.1, since that behaviour
	// is easy to assume rather than check. It deliberately does NOT accept a bare
	// int there (see yaml.v3's decode.go, the isDuration guard), so a malformed
	// duration fails the decode below with a message naming the value rather than
	// silently keeping Default()'s.
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse yaml: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
