// Package scan does concurrent, sharded scanning of codex-lb's multi-member gzip
// archives.
//
// Sharding a single archive across goroutines is only sound because codex-lb closes
// a gzip member per batch: a worker can seek into the middle of the file and
// resynchronise onto the next member boundary (archive.FindMemberStart) instead of
// decoding everything before it. Callers that need the records back in file order
// (the reducer is a state machine over a response's frames) recover it from the
// shard index Collect and File hand back - see Collect's doc comment.
package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/rknightion/codexlb2otel/internal/archive"
	"github.com/rknightion/codexlb2otel/internal/frame"
)

const chunkSize = 16 << 20

// shardChunk is deliberately smaller than chunkSize: a shard's compressed buffer
// decompresses to roughly three times its size, and every core running one at once
// is what decides the scan's peak memory.
const shardChunk = 4 << 20

// Collect gathers the records matching want, IN FILE ORDER, scanning concurrently.
//
// Order is not optional here: the reducer is a state machine over a response's
// frames, so replaying them out of sequence produces a different turn. Shards write
// into their own slice and are concatenated by index afterwards, which is exact -
// no locking on the hot path and no reliance on timestamps to reconstruct order.
func Collect(path string, want func(*frame.Record) bool) ([]*frame.Record, error) {
	var mu sync.Mutex
	perShard := map[int][]*frame.Record{}

	err := File(path, func(shard int, line []byte) error {
		var rec frame.Record
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		if !want(&rec) {
			return nil
		}
		mu.Lock()
		perShard[shard] = append(perShard[shard], &rec)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}

	idx := make([]int, 0, len(perShard))
	for i := range perShard {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []*frame.Record
	for _, i := range idx {
		out = append(out, perShard[i]...)
	}
	return out, nil
}

// Shards splits a file into byte ranges that can be scanned concurrently.
//
// This is only sound because codex-lb closes a gzip member per batch: a worker can
// seek into the middle of the file and resynchronise onto the next member boundary
// (archive.FindMemberStart) instead of decoding everything before it. Boundaries
// cannot overlap or leave a gap, because "the first member start at or after X" is
// deterministic - so worker k stops at X and worker k+1 begins at the same member.
func Shards(size int64, n int) [][2]int64 {
	if n < 1 {
		n = 1
	}
	out := make([][2]int64, 0, n)
	step := size / int64(n)
	if step < MinShard {
		return [][2]int64{{0, size}}
	}
	for i := range n {
		start := int64(i) * step
		end := start + step
		if i == n-1 {
			end = size
		}
		out = append(out, [2]int64{start, end})
	}
	return out
}

// MinShard keeps the work per goroutine worth the resync it costs. A var so tests
// can force sharding on a fixture small enough to be quick.
var MinShard int64 = 8 << 20

// File scans one archive concurrently, calling fn for every complete line with
// the index of the shard it came from.
//
// fn is called from several goroutines, so it must be safe for concurrent use. The
// shard index is what lets a caller that needs FILE ORDER recover it: shards cover
// ascending byte ranges, so appending per shard and concatenating in index order
// reproduces the original sequence exactly - without inventing an ordering from
// timestamps, which frames written in the same microsecond would not supply.
func File(path string, fn func(shard int, line []byte) error) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	ranges := Shards(st.Size(), max(1, runtime.NumCPU()-1))

	var wg sync.WaitGroup
	errs := make([]error, len(ranges))
	for i, r := range ranges {
		wg.Add(1)
		go func(i int, start, end int64) {
			defer wg.Done()
			errs[i] = streamRange(path, start, end, func(line []byte) error { return fn(i, line) })
		}(i, r[0], r[1])
	}
	wg.Wait()
	return errors.Join(errs...)
}

// streamRange hands over the lines of every member that BEGINS within [start, end).
//
// "Begins within" is the whole contract, and it is what makes shards tile the file
// exactly. A member straddling `end` belongs to this shard, because its start is
// inside the range; the next shard resynchronises past it. Getting this wrong is not
// subtle in effect but is invisible in code: an earlier version simply read a chunk
// and checked the offset afterwards, so with a chunk larger than a shard every
// worker ran to EOF and the scan emitted every line about five times.
func streamRange(path string, start, end int64, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	pos := start
	if start > 0 {
		// Land on a real member boundary before decoding anything.
		head := make([]byte, min(int64(shardChunk), end-start+resyncSlack))
		n, err := f.ReadAt(head, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		off, ok := archive.FindMemberStart(head[:n])
		if !ok {
			return nil // no member begins in this range
		}
		pos = start + int64(off)
	}
	if pos >= end {
		return nil
	}
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return err
	}

	var pending []byte
	var buf []byte
	tmp := make([]byte, shardChunk)

	// Phase one: everything wholly inside the range. Reads are clamped to `end` so
	// no member starting beyond it can be decoded by accident.
	for pos < end {
		want := min(int64(len(tmp)), end-pos-int64(len(buf)))
		if want <= 0 {
			break
		}
		n, rerr := f.Read(tmp[:want])
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return derr
			}
			if res.Consumed > 0 {
				if pending, err = emitLines(pending, res.Data, fn); err != nil {
					return err
				}
				buf = append(buf[:0], buf[res.Consumed:]...)
				pos += int64(res.Consumed)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			break
		}
	}

	// Phase two: the member that straddles `end`. It started before `end`, so it is
	// ours - read past the boundary far enough to finish it, and exactly one member.
	if len(buf) > 0 {
		slack := make([]byte, resyncSlack)
		n, rerr := f.Read(slack)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return rerr
		}
		buf = append(buf, slack[:n]...)
		data, consumed, derr := archive.DecodeMember(buf)
		if derr != nil {
			return derr
		}
		if consumed > 0 {
			if pending, err = emitLines(pending, data, fn); err != nil {
				return err
			}
		}
	}

	if line := bytes.TrimSpace(pending); len(line) > 0 {
		return fn(line)
	}
	return nil
}

// resyncSlack is how far past a shard's end a worker may read to finish the member
// it is already decoding.
const resyncSlack = 4 << 20

// emitLines hands every complete line in data to fn and returns the trailing
// partial for the next chunk to prepend.
//
// data is scanned in place. Archive members hold whole records, so the carry is
// almost always empty and the common path copies nothing - which is what keeps a
// sharded scan CPU-bound instead of drowning the collector.
func emitLines(carry, data []byte, fn func([]byte) error) ([]byte, error) {
	if len(carry) > 0 {
		data = append(carry, data...)
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(data[:i]); len(line) > 0 {
			if err := fn(line); err != nil {
				return nil, err
			}
		}
		data = data[i+1:]
	}
	if len(data) == 0 {
		return nil, nil
	}
	return append([]byte(nil), data...), nil
}

// streamLines decodes whole gzip members and hands over complete lines, so memory
// stays bounded no matter how large the archive is.
func streamLines(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var pending []byte
	buf := make([]byte, 0, chunkSize)
	tmp := make([]byte, chunkSize)
	for {
		n, rerr := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return derr
			}
			if res.Consumed > 0 {
				var err error
				if pending, err = emitLines(pending, res.Data, fn); err != nil {
					return err
				}
				buf = append(buf[:0], buf[res.Consumed:]...)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			break
		}
	}
	if line := bytes.TrimSpace(pending); len(line) > 0 {
		return fn(line)
	}
	return nil
}

// Archives resolves paths that may be individual archives or directories to walk.
//
// A named file is taken at its word rather than filtered on extension: if someone
// points at a specific archive, refusing it because it does not end in .jsonl.gz
// would be unhelpful, and it will simply fail to decode if it is not one.
func Archives(paths []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		err = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(q, ".jsonl.gz") && !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
