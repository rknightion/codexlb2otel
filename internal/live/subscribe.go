package live

// subQueue is how many snapshots a subscriber may fall behind before it is dropped.
//
// Small on purpose. A snapshot is self-contained - it is the whole view, not a delta -
// so a subscriber that is behind gains nothing from the queued intermediates and only
// wants the newest one. The queue exists to absorb a scheduling hiccup, not to buffer a
// client that cannot keep up.
const subQueue = 4

type subscriber struct {
	ch chan Snapshot
}

// Subscribe registers a subscriber and returns its channel plus a cancel func. The
// channel is closed by cancel or by Store.Close; a reader must tolerate both.
func (s *Store) Subscribe() (<-chan Snapshot, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextSub
	s.nextSub++
	sub := &subscriber{ch: make(chan Snapshot, subQueue)}
	s.subs[id] = sub

	// Prime it with the current view so a client that connects during a quiet period
	// renders immediately instead of staring at an empty page until the next poll.
	sub.ch <- s.snapshotLocked()

	var once bool
	return sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if once {
			return
		}
		once = true
		if cur, ok := s.subs[id]; ok && cur == sub {
			delete(s.subs, id)
			close(sub.ch)
		}
	}
}

// broadcast hands the snapshot to every subscriber WITHOUT WAITING FOR ANY OF THEM.
//
// This is the single most load-bearing property of the package. Its caller is Emit,
// which runs inside tail.Watcher.Poll under the watcher's write lock, and whose return
// value gates the ingestion checkpoint. A blocking send here would mean one browser on
// a bad connection can stall archive ingestion for the entire service - a UI feature
// taking down the pipeline it exists to observe.
//
// So a full queue drops the snapshot for that subscriber. Nothing is lost by it: every
// snapshot is the complete view, so the next successful send fully resynchronises the
// client. That is exactly why the payload was designed as a snapshot rather than a
// delta stream.
func (s *Store) broadcast(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.subs {
		select {
		case sub.ch <- snap:
		default:
		}
	}
}
