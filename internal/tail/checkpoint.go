// Package tail follows a directory of append-only gzip archive files, converting
// newly written frames into turns and reclaiming files once they are consumed.
package tail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

// Checkpoint is what must survive a restart.
//
// Two separate things are recorded and both matter. Offsets stop already-shipped
// frames being shipped again, which Loki deduplicates poorly and OTLP counters not
// at all. The reducer state stops the first reading after a restart being mistaken
// for a delta, which would attribute a whole turn's tokens to one response.
type Checkpoint struct {
	Version int                  `json:"version"`
	Files   map[string]FileState `json:"files"`
	Reducer turn.State           `json:"reducer"`
	Updated time.Time            `json:"updated"`
}

// FileState is how far into one archive file we have read.
type FileState struct {
	// Offset is a byte position that always falls on a gzip member boundary, so a
	// resume never starts mid-member. See archive.DecodeMembers.
	Offset int64 `json:"offset"`
	// Size at the time of the last read, used to notice truncation or replacement.
	Size int64 `json:"size"`
	// Deleted marks a file reclaimed after ingest, so it is not re-read if it
	// somehow reappears and is not re-reported as new.
	Deleted bool `json:"deleted,omitempty"`
}

const checkpointVersion = 1

// LoadCheckpoint reads a checkpoint. A missing file yields an empty checkpoint
// rather than an error - that is just a first run.
//
// A checkpoint that is corrupt or from an unknown version is also treated as a cold
// start. Refusing to start would mean no telemetry at all; starting cold costs some
// duplicate shipping and one turn's delta accuracy.
func LoadCheckpoint(path string) (Checkpoint, error) {
	c := Checkpoint{Version: checkpointVersion, Files: map[string]FileState{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("checkpoint: read %s: %w", path, err)
	}
	var loaded Checkpoint
	if err := json.Unmarshal(b, &loaded); err != nil || loaded.Version != checkpointVersion {
		return c, nil
	}
	if loaded.Files == nil {
		loaded.Files = map[string]FileState{}
	}
	return loaded, nil
}

// Save writes the checkpoint atomically.
//
// Atomicity is not optional here: a half-written checkpoint that survives a crash
// would be parsed as a cold start on the next boot, re-shipping everything. Write to
// a temp file in the same directory, fsync, then rename.
func (c *Checkpoint) Save(path string) error {
	c.Version = checkpointVersion
	c.Updated = time.Now().UTC()

	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("checkpoint: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}
