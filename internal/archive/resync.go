package archive

import (
	"bytes"
	"compress/gzip"
	"io"
	"unicode/utf8"
)

// gzipMagic is the fixed prefix of every member codex-lb writes: ID1, ID2, and
// CM=8 (deflate), which is the only compression method gzip defines.
var gzipMagic = []byte{0x1f, 0x8b, 0x08}

// resyncScanLimit bounds how far FindMemberStart will hunt before giving up.
// Real members are ~1KB, so a start is normally found within the first few
// hundred bytes; anything beyond this means the buffer is not archive data.
const resyncScanLimit = 4 << 20

// FindMemberStart locates the first byte of a complete gzip member inside data.
//
// This is what makes random-access sampling of an archive possible. Every member
// is self-contained, so a reader can drop into the middle of a multi-gigabyte
// file, resynchronise on the next member boundary, and decode from there without
// touching the bytes in between. Whole-file scanning is the only alternative.
//
// The gzip magic can occur by chance inside compressed data, so a candidate is
// accepted only once it actually decodes into plausible archive content. That
// check is what separates this from a naive byte search.
func FindMemberStart(data []byte) (int, bool) {
	limit := min(len(data), resyncScanLimit)
	for i := 0; i < limit; {
		j := bytes.Index(data[i:limit], gzipMagic)
		if j < 0 {
			return 0, false
		}
		i += j
		if plausibleMemberAt(data[i:]) {
			return i, true
		}
		i++
	}
	return 0, false
}

// plausibleMemberAt reports whether a real member begins at data[0].
//
// A single successful decode is not enough - random compressed bytes decode into
// short garbage often enough to matter. The test is that the member decodes into
// valid UTF-8 containing a JSON object, and that whatever follows it is either
// another member start or the end of the buffer. Two independent structural
// agreements is what makes a false positive vanishingly unlikely.
func plausibleMemberAt(data []byte) bool {
	if !plausibleHeader(data) {
		return false
	}
	out, consumed, err := decodeOne(data)
	if err != nil || consumed <= 0 || len(out) == 0 {
		return false
	}
	if !utf8.Valid(out) || !bytes.Contains(out, []byte(`{"`)) {
		return false
	}
	rest := data[consumed:]
	if len(rest) < len(gzipMagic) {
		return true // ran out of buffer, not a contradiction
	}
	return plausibleHeader(rest)
}

// plausibleHeader checks the fixed-shape fields of a gzip header. FLG's top three
// bits are reserved and must be zero, which rejects most random matches for free.
func plausibleHeader(data []byte) bool {
	if len(data) < 10 || !bytes.HasPrefix(data, gzipMagic) {
		return false
	}
	return data[3]&0xE0 == 0
}

// decodeOne decompresses exactly one member and reports how many input bytes it
// covered.
func decodeOne(data []byte) (out []byte, consumed int, err error) {
	br := bytes.NewReader(data)
	zr, err := gzip.NewReader(br)
	if err != nil {
		return nil, 0, err
	}
	defer zr.Close()
	zr.Multistream(false)

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(zr, maxMemberSize)); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), len(data) - br.Len(), nil
}
