// Package archive reads codex-lb conversation-archive files.
//
// codex-lb writes these with O_APPEND, closing a complete gzip member per batch
// (see _write_gzip_jsonl_records in app/core/conversation_archive.py). A file is
// therefore a concatenation of thousands of self-contained members - one 16MB file
// held 15,106 of them, ~1.1 JSON records each.
//
// That property is what makes safe tailing possible: we decode whole members only,
// stop at the first incomplete one, and checkpoint the byte offset of the last
// complete member. A partial member at the tail is the normal steady state, not an
// error - the writer is mid-append.
package archive

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// ErrPartialMember signals that the input ended mid-member. Not a failure: the
// caller keeps its offset and retries once the writer has appended more.
var ErrPartialMember = errors.New("archive: incomplete trailing gzip member")

// maxMemberSize caps a single decoded member to stop a corrupt length field from
// driving an unbounded allocation. Real members are ~1KB; 64MB is absurdly generous.
const maxMemberSize = 64 << 20

// Result reports what DecodeMembers managed to read.
type Result struct {
	// Data is the concatenated decompressed bytes of every complete member.
	Data []byte
	// Consumed is the number of input bytes covered by those members. This is what
	// the caller adds to its checkpoint offset - never len(data), which may include
	// a partial member.
	Consumed int
	// Members is how many complete gzip members were decoded.
	Members int
}

// DecodeMembers decompresses every complete gzip member at the front of data.
//
// A trailing partial member is not an error; it simply is not included in Consumed.
// A member that is complete but internally corrupt returns an error along with
// everything decoded before it, so a caller can make progress past good data.
func DecodeMembers(data []byte) (res Result, err error) {
	var buf bytes.Buffer
	br := bytes.NewReader(data)

	defer func() { res.Data = buf.Bytes() }()

	// bytes.Reader implements io.ByteReader, so gzip does not wrap it in a bufio
	// and will not read ahead past the member trailer. That is what keeps the
	// offset arithmetic below exact.
	var zr *gzip.Reader

	for {
		pos := len(data) - br.Len()
		if pos >= len(data) {
			return res, nil
		}

		if zr == nil {
			zr, err = gzip.NewReader(br)
		} else {
			err = zr.Reset(br)
		}
		if err != nil {
			// Header truncated mid-write: everything before pos is still good.
			if isIncomplete(err) {
				return res, nil
			}
			return res, fmt.Errorf("archive: gzip header at offset %d: %w", pos, err)
		}

		// One member at a time so member boundaries stay observable.
		zr.Multistream(false)

		n, cErr := io.Copy(&buf, io.LimitReader(zr, maxMemberSize))
		if cErr != nil {
			// Body or trailer truncated. Roll back this member's output.
			buf.Truncate(buf.Len() - int(n))
			if isIncomplete(cErr) {
				return res, nil
			}
			return res, fmt.Errorf("archive: gzip body at offset %d: %w", pos, cErr)
		}
		if n == maxMemberSize {
			buf.Truncate(buf.Len() - int(n))
			return res, fmt.Errorf("archive: member at offset %d exceeds %d bytes", pos, maxMemberSize)
		}

		// The member decoded cleanly, so the reader now sits exactly on the next
		// member's first byte and everything up to here is durable.
		res.Consumed = len(data) - br.Len()
		res.Members++
	}
}

// isIncomplete reports whether err means "the writer has not finished this member",
// as opposed to genuine corruption.
func isIncomplete(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}
