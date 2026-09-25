package voicegateway

import (
	"sync"
	"testing"
)

func TestTurnRefAllocatorStartsAtOneAndNeverRepeats(t *testing.T) {
	t.Parallel()
	var a TurnRefAllocator
	for want := uint32(1); want <= 5; want++ {
		got := a.Next()
		if got == nil {
			t.Fatalf("Next() at %d returned nil", want)
		}
		if *got != want {
			t.Fatalf("Next() = %d, want %d", *got, want)
		}
	}
	if got := a.Peek(); got != 5 {
		t.Fatalf("Peek() = %d, want 5", got)
	}
}

func TestNilTurnRefAllocatorDegradesToUnattributed(t *testing.T) {
	t.Parallel()
	var a *TurnRefAllocator
	if got := a.Next(); got != nil {
		t.Fatalf("a nil allocator must produce no attribution, got %d", *got)
	}
	if got := a.Peek(); got != 0 {
		t.Fatalf("Peek() on nil = %d, want 0", got)
	}
}

func TestTurnRefAllocatorIsSafeUnderConcurrentProducers(t *testing.T) {
	t.Parallel()
	var a TurnRefAllocator
	const producers, each = 8, 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[uint32]bool{}
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				ref := a.Next()
				mu.Lock()
				if seen[*ref] {
					mu.Unlock()
					t.Errorf("turn_ref %d was handed out twice", *ref)
					return
				}
				seen[*ref] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != producers*each {
		t.Fatalf("handed out %d distinct refs, want %d", len(seen), producers*each)
	}
}

func TestTurnRefAllocatorAndSeqAllocatorNumberSeparateDomains(t *testing.T) {
	t.Parallel()
	var seq SeqAllocator
	var refs TurnRefAllocator
	for i := 0; i < 3; i++ {
		seq.Next()
	}
	if got := refs.Peek(); got != 0 {
		t.Fatalf("numbering frames must not advance the turn counter: %d", got)
	}
	if got := refs.Next(); got == nil || *got != 1 {
		t.Fatalf("first turn_ref = %v, want 1", got)
	}
	if got := seq.Peek(); got != 3 {
		t.Fatalf("numbering a turn must not advance the frame counter: %d", got)
	}
}
