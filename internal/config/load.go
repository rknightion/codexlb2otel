package config

import (
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
