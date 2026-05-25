package display

import "testing"

func eqLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogBufferCollapsesConsecutiveDuplicates(t *testing.T) {
	b := newLogBuffer(10)
	b.push("a")
	b.push("dup")
	b.push("dup")
	b.push("dup")
	b.push("b")
	b.push("dup") // not consecutive with the earlier run — stays separate
	eqLines(t, b.snapshot(), []string{"a", "dup  (×3)", "b", "dup"})
}

func TestLogBufferKeepsHistoryDespiteRepeatFlood(t *testing.T) {
	// A flood of one repeated line must not evict earlier distinct lines:
	// collapsing means the repeat occupies a single slot regardless of count.
	b := newLogBuffer(3)
	b.push("first")
	b.push("second")
	for i := 0; i < 100; i++ {
		b.push("flood")
	}
	eqLines(t, b.snapshot(), []string{"first", "second", "flood  (×100)"})
}

func TestLogBufferTrimsToCapacity(t *testing.T) {
	b := newLogBuffer(3)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		b.push(s)
	}
	eqLines(t, b.snapshot(), []string{"3", "4", "5"})
}

func TestLogBufferSnapshotIsACopy(t *testing.T) {
	b := newLogBuffer(4)
	b.push("x")
	snap := b.snapshot()
	b.push("y")
	// The earlier snapshot must not see the later push.
	eqLines(t, snap, []string{"x"})
}
