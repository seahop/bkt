package api

import "testing"

func TestReplicationCreatesCycle(t *testing.T) {
	chain := map[string]string{}
	next := func(n string) string { return chain[n] }

	if !replicationCreatesCycle("a", "a", next) {
		t.Error("self-replication must be a cycle")
	}
	if replicationCreatesCycle("a", "b", next) {
		t.Error("a->b with b not replicating is not a cycle")
	}

	chain["b"] = "a" // b already replicates to a
	if !replicationCreatesCycle("a", "b", next) {
		t.Error("a->b with b->a must be a cycle")
	}

	chain = map[string]string{"b": "c", "c": "a"} // a->b->c->a
	if !replicationCreatesCycle("a", "b", next) {
		t.Error("a->b->c->a must be a cycle")
	}

	chain = map[string]string{"b": "c"} // plain chain a->b->c
	if replicationCreatesCycle("a", "b", next) {
		t.Error("a->b->c is a chain, not a cycle")
	}

	chain = map[string]string{"b": "c", "c": "b"} // pre-existing loop not through a
	if !replicationCreatesCycle("a", "b", next) {
		t.Error("joining a pre-existing loop must be refused (and must terminate)")
	}
}

func TestCheckRetentionChange(t *testing.T) {
	cases := []struct {
		current, requested int
		retained           int64
		wantErr            bool
	}{
		{0, 30, 0, false},  // enabling
		{30, 60, 5, false}, // increasing is always allowed
		{30, 30, 5, false}, // unchanged
		{30, 0, 5, true},   // clearing while data is retained
		{30, 10, 1, true},  // lowering while data is retained
		{30, 0, 0, false},  // nothing retained any more
		{30, 10, 0, false},
	}
	for _, c := range cases {
		err := checkRetentionChange(c.current, c.requested, c.retained)
		if (err != nil) != c.wantErr {
			t.Errorf("checkRetentionChange(%d,%d,%d) err=%v wantErr=%v", c.current, c.requested, c.retained, err, c.wantErr)
		}
	}
}

func TestNoncurrentExpiryAllowed(t *testing.T) {
	cases := []struct {
		name                                   string
		marker, retained, latest, othersRemain bool
		want                                   bool
	}{
		{"content version, not retained", false, false, false, true, true},
		{"content version, retained", false, true, false, true, false},
		{"non-latest delete marker", true, false, false, true, true},
		// The bug: removing the latest marker while (retained) content remains
		// beneath it resurrected the object, which current-expiry re-deleted.
		{"latest marker hiding versions", true, false, true, true, false},
		{"latest marker, sole remaining version", true, false, true, false, true},
	}
	for _, c := range cases {
		if got := noncurrentExpiryAllowed(c.marker, c.retained, c.latest, c.othersRemain); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
