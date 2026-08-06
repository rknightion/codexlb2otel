package turn

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
)

// seenSet is a bounded set of content fingerprints with LRU eviction.
//
// Every response.create re-sends the whole conversation history - input arrays of
// 800+ items were common in the captured traffic, and that redundancy is why
// response.create alone accounted for a third of all archive bytes. Content is
// therefore emitted only the first time it is observed.
//
// The bound is what makes this safe in a long-running process: a per-thread map of
// everything ever seen would grow without limit. Evicting the oldest fingerprint
// instead costs at worst a duplicate log line for content that reappears after a
// very long gap, which is a far better failure than unbounded memory.
type seenSet struct {
	max   int
	order *list.List               // front = most recent
	index map[uint64]*list.Element // fingerprint -> node
}

func newSeenSet(max int) *seenSet {
	if max <= 0 {
		max = 1
	}
	return &seenSet{max: max, order: list.New(), index: make(map[uint64]*list.Element, max)}
}

// add records a fingerprint and reports whether it was new.
func (s *seenSet) add(parts ...string) bool {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // separator, so ("ab","c") and ("a","bc") differ
	}
	sum := h.Sum(nil)
	key := binary.BigEndian.Uint64(sum[:8])

	if el, ok := s.index[key]; ok {
		s.order.MoveToFront(el)
		return false
	}
	s.index[key] = s.order.PushFront(key)
	if s.order.Len() > s.max {
		oldest := s.order.Back()
		if oldest != nil {
			s.order.Remove(oldest)
			delete(s.index, oldest.Value.(uint64))
		}
	}
	return true
}

func (s *seenSet) len() int { return s.order.Len() }
