package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rknightion/codexlb2otel/internal/archive"
)

// DefaultChunk is how many compressed bytes a full scan decodes per pass. Memory
// stays bounded at roughly this regardless of how large the archive is.
const DefaultChunk = 16 << 20

// Coverage records how much of a file a scan actually looked at, so a caller can
// tell a whole-file answer from a sampled one. A sampled scan cannot prove
// absence: the rarest shapes in the live corpus occur ~10 times in 1.3M records,
// so anything it does not see may simply not have been in the windows it read.
type Coverage struct {
	Files       int   `json:"files"`
	FileBytes   int64 `json:"file_bytes"`
	ReadBytes   int64 `json:"read_bytes"`
	Windows     int   `json:"windows,omitempty"`
	Resynced    int   `json:"resynced_windows,omitempty"`
	DeadWindows int   `json:"windows_with_no_member,omitempty"`
	Sampled     bool  `json:"sampled"`
}

// Fraction is the share of compressed bytes the scan read, in [0,1].
func (c Coverage) Fraction() float64 {
	if c.FileBytes <= 0 {
		return 0
	}
	f := float64(c.ReadBytes) / float64(c.FileBytes)
	if f > 1 {
		return 1
	}
	return f
}

func (c *Coverage) merge(o Coverage) {
	c.Files += o.Files
	c.FileBytes += o.FileBytes
	c.ReadBytes += o.ReadBytes
	c.Windows += o.Windows
	c.Resynced += o.Resynced
	c.DeadWindows += o.DeadWindows
	c.Sampled = c.Sampled || o.Sampled
}

// ScanFile profiles a whole archive, streaming so that memory is bounded by chunk
// rather than by file size.
func ScanFile(path string, chunk int) (*Profile, Coverage, error) {
	if chunk <= 0 {
		chunk = DefaultChunk
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer f.Close()

	size, err := fileSize(f)
	if err != nil {
		return nil, Coverage{}, err
	}

	p := New()
	fp := FileProfile{Name: filepath.Base(path)}

	var pending []byte
	buf := make([]byte, 0, chunk)
	tmp := make([]byte, chunk)
	for {
		n, rerr := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			res, derr := archive.DecodeMembers(buf)
			if derr != nil {
				return nil, Coverage{}, derr
			}
			if res.Consumed > 0 {
				fp.Bytes += int64(len(res.Data))
				fp.Members += int64(res.Members)
				pending = consume(p, &fp, append(pending, res.Data...))
				buf = append(buf[:0], buf[res.Consumed:]...)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, Coverage{}, rerr
			}
			break
		}
	}
	if line := bytes.TrimSpace(pending); len(line) > 0 {
		p.AddLine(line, &fp)
	}

	p.Bytes = fp.Bytes
	p.Members = fp.Members
	p.Files = []FileProfile{fp}
	return p, Coverage{Files: 1, FileBytes: size, ReadBytes: size}, nil
}

// ScanSampled profiles evenly spaced windows of an archive instead of all of it.
//
// It exists because a full pass over a day of capture is minutes of CPU and gigabytes
// of decompression, while the question asked most often - "has the format changed?" -
// is answerable from a few megabytes. Each window resynchronises onto a gzip member
// boundary (see archive.FindMemberStart), which is only possible because codex-lb
// closes a member per batch.
//
// The trade is stated in the returned Coverage and must not be forgotten: sampling
// detects drift in common shapes and proves nothing about rare ones.
func ScanSampled(path string, windows, windowBytes int) (*Profile, Coverage, error) {
	if windows < 1 {
		windows = 1
	}
	if windowBytes <= 0 {
		windowBytes = 1 << 20
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer f.Close()

	size, err := fileSize(f)
	if err != nil {
		return nil, Coverage{}, err
	}
	// Small enough to read whole: sampling would cost the same and answer less.
	if size <= int64(windows)*int64(windowBytes) {
		p, cov, err := ScanFile(path, DefaultChunk)
		return p, cov, err
	}

	p := New()
	fp := FileProfile{Name: filepath.Base(path)}
	cov := Coverage{Files: 1, FileBytes: size, Sampled: true}

	buf := make([]byte, windowBytes)
	for _, off := range windowOffsets(size, int64(windowBytes), windows) {
		n, err := f.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, cov, fmt.Errorf("read at %d: %w", off, err)
		}
		if n == 0 {
			continue
		}
		cov.Windows++
		cov.ReadBytes += int64(n)

		w := buf[:n]
		if off > 0 {
			start, ok := archive.FindMemberStart(w)
			if !ok {
				cov.DeadWindows++
				continue
			}
			cov.Resynced++
			w = w[start:]
		}

		res, derr := archive.DecodeMembers(w)
		if derr != nil {
			// A window can land on a member the *writer* corrupted, or on bytes that
			// only looked like a header. Neither invalidates the other windows.
			cov.DeadWindows++
			continue
		}
		if res.Members == 0 {
			cov.DeadWindows++
			continue
		}
		fp.Bytes += int64(len(res.Data))
		fp.Members += int64(res.Members)
		// Each window is independent, so its trailing partial line is dropped rather
		// than carried across the gap - stitching two unrelated offsets together would
		// manufacture a record that never existed.
		consume(p, &fp, res.Data)
	}

	p.Bytes = fp.Bytes
	p.Members = fp.Members
	p.Files = []FileProfile{fp}
	return p, cov, nil
}

// windowOffsets spreads windows across the file, always including the true start
// (offset 0 needs no resync and is where a format change to the header would show)
// and the tail (the most recent traffic, where a change appears first).
func windowOffsets(size, window int64, n int) []int64 {
	if n == 1 {
		return []int64{0}
	}
	last := max(size-window, 0)
	out := make([]int64, 0, n)
	for i := range n {
		out = append(out, last*int64(i)/int64(n-1))
	}
	return out
}

// consume folds every complete line into the profile and returns the trailing partial.
func consume(p *Profile, fp *FileProfile, data []byte) []byte {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return data
		}
		if line := bytes.TrimSpace(data[:i]); len(line) > 0 {
			p.AddLine(line, fp)
		}
		data = data[i+1:]
	}
}

func fileSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
