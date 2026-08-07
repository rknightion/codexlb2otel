package archive

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Resynchronising is what makes sampling a multi-gigabyte archive affordable, so
// the property that matters is exactness: the member found from an arbitrary byte
// offset must be a real member start, not merely a plausible one. Decoding from it
// must therefore produce a suffix of what a full decode produces.
func TestFindMemberStart_LandsOnARealBoundary(t *testing.T) {
	var batches [][]string
	for i := range 40 {
		batches = append(batches, []string{
			fmt.Sprintf(`{"seq":%d,"kind":"responses","payload":{"text":"%s"}}`, i, strings.Repeat("x", 200)),
		})
	}
	data := appendMembers(t, batches...)

	full, err := DecodeMembers(data)
	if err != nil {
		t.Fatal(err)
	}

	// Cut at every offset, including deep inside members, and require that whatever
	// we resync onto decodes to a suffix of the whole file.
	tried, resynced := 0, 0
	for cut := 1; cut < len(data); cut += 7 {
		tried++
		off, ok := FindMemberStart(data[cut:])
		if !ok {
			continue
		}
		resynced++
		res, err := DecodeMembers(data[cut+off:])
		if err != nil {
			t.Fatalf("cut %d: resynced at +%d but decode failed: %v", cut, off, err)
		}
		if !bytes.HasSuffix(full.Data, res.Data) {
			t.Fatalf("cut %d: resynced at +%d decoded %d bytes that are not a suffix of the full decode",
				cut, off, len(res.Data))
		}
	}
	if resynced == 0 {
		t.Fatalf("never resynced across %d cuts", tried)
	}
	t.Logf("resynced from %d of %d cut points", resynced, tried)
}

// The gzip magic occurs by chance inside compressed bodies. A byte search alone
// would return those offsets and produce garbage, so rejecting them is the whole
// job of the validation step.
func TestFindMemberStart_RejectsNonMembers(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"magic only", []byte{0x1f, 0x8b, 0x08}},
		{"magic with reserved flag bits", append([]byte{0x1f, 0x8b, 0x08, 0xFF}, bytes.Repeat([]byte{0}, 32)...)},
		{"plausible header, no body", append([]byte{0x1f, 0x8b, 0x08, 0x00}, bytes.Repeat([]byte{0}, 32)...)},
		{"json text", []byte(`{"kind":"responses","payload":{"text":"{}"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if off, ok := FindMemberStart(tc.data); ok {
				t.Fatalf("accepted a non-member at offset %d", off)
			}
		})
	}
}

// A member whose decompressed body is not archive-shaped is not what we are looking
// for either: sampling must land on conversation records, not on any gzip stream
// that happens to be embedded.
func TestFindMemberStart_RequiresArchiveShapedContent(t *testing.T) {
	notArchive := appendMembers(t, []string{strings.Repeat("plain text, no json object here", 40)})
	if off, ok := FindMemberStart(notArchive); ok {
		t.Fatalf("accepted a non-archive gzip member at offset %d", off)
	}

	archive := appendMembers(t, []string{`{"kind":"responses"}`})
	if _, ok := FindMemberStart(archive); !ok {
		t.Fatal("rejected a real archive member")
	}
}
