package turn

import "testing"

func TestCorpusTurnCacheLoadsOnce(t *testing.T) {
	var loads int
	want := []*Turn{{RequestID: "cached"}}
	cache := corpusTurnCache{}

	for range 2 {
		got := cache.load(func() []*Turn {
			loads++
			return want
		})
		if len(got) != 1 || got[0].RequestID != "cached" {
			t.Fatalf("cached turns = %#v, want one cached turn", got)
		}
	}
	if loads != 1 {
		t.Fatalf("loader called %d times, want 1", loads)
	}
}
