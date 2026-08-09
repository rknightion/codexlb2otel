// Package tail follows a directory of append-only gzip archive files, converting
// newly written frames into turns and reclaiming files once they are consumed.
package tail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Size at the time of the last read.
	Size int64 `json:"size"`
	// Fingerprint identifies the file CONTENT, not the path.
	//
	// A path is not a stable identity here. codex-lb reopens the archive with
	// O_APPEND|O_CREAT for every batch, so moving a file away - copying the day's
	// archives off the host, say - makes it recreate the same name from scratch. That
	// is not hypothetical: 2026-08-06T18 exists as two entirely different files, one
	// covering 18:00-18:27 and one 18:43-18:52.
	//
	// Detecting that by size alone is unsound. It only works while the replacement is
	// still SMALLER than the old offset; if the new file has already grown past it by
	// the next poll, the old offset is silently applied to unrelated bytes and the
	// reader resumes mid-member on a different stream. The fingerprint catches the
	// replacement whatever the sizes happen to be.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Deleted marks a file reclaimed after ingest, so it is not re-read if it
	// somehow reappears and is not re-reported as new.
	Deleted bool `json:"deleted,omitempty"`
}

// fingerprintBytes is how much of the file head identifies it. One gzip member header
// plus the start of its payload is ample, and cheap enough to re-read every poll.
const fingerprintBytes = 512

// fingerprint hashes the head of a file. A file shorter than fingerprintBytes is
// hashed whole, so a file being replaced by a smaller one is still detected.
func fingerprint(f *os.File) (string, error) {
	buf := make([]byte, fingerprintBytes)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	sum := sha256.Sum256(buf[:n])
	return hex.EncodeToString(sum[:8]), nil
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
	c.Updated = time.Now().UTC()
	b, err := c.render()
	if err != nil {
		return err
	}
	return c.write(path, b)
}

// render marshals the checkpoint as it currently stands, Updated included.
func (c *Checkpoint) render() ([]byte, error) {
	c.Version = checkpointVersion
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: marshal: %w", err)
	}
	return b, nil
}

// contentSum fingerprints everything the checkpoint MEANS, deliberately excluding Updated.
//
// Updated moves on every Save, so a fingerprint that included it would differ from the
// previous one every single time and the unchanged-content skip in Watcher.save would
// never fire - the file would go back to being rewritten and fsynced on a timer forever,
// which is precisely the behaviour this exists to stop. That is not hypothetical: the
// first version of this compared whole renders and did exactly that.
func (c *Checkpoint) contentSum() ([32]byte, error) {
	d := *c // shallow copy; the maps are only read from here
	d.Version = checkpointVersion
	d.Updated = time.Time{}

	b, err := json.Marshal(&d)
	if err != nil {
		return [32]byte{}, fmt.Errorf("checkpoint: marshal: %w", err)
	}
	return sha256.Sum256(b), nil
}

// write puts already-rendered bytes on disk atomically.
func (c *Checkpoint) write(path string, b []byte) error {
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
