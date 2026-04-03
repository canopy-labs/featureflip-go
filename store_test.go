package featureflip

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewStore_Empty(t *testing.T) {
	s := newStore()

	_, ok := s.getFlag("anything")
	if ok {
		t.Error("new store should have no flags")
	}

	_, ok = s.getSegment("anything")
	if ok {
		t.Error("new store should have no segments")
	}

	segs := s.allSegments()
	if len(segs) != 0 {
		t.Errorf("allSegments should be empty, got %d", len(segs))
	}
}

func TestStore_SetAll(t *testing.T) {
	s := newStore()

	flags := []flagDTO{
		{Key: "flag-1", Version: 1, Enabled: true},
		{Key: "flag-2", Version: 2, Enabled: false},
	}
	segments := []segmentDTO{
		{Key: "seg-1", Version: 1},
		{Key: "seg-2", Version: 2},
	}

	s.setAll(flags, segments)

	// Check flags
	f1, ok := s.getFlag("flag-1")
	if !ok {
		t.Fatal("flag-1 should exist")
	}
	if f1.Version != 1 {
		t.Errorf("flag-1 Version = %d, want 1", f1.Version)
	}
	if !f1.Enabled {
		t.Error("flag-1 should be enabled")
	}

	f2, ok := s.getFlag("flag-2")
	if !ok {
		t.Fatal("flag-2 should exist")
	}
	if f2.Version != 2 {
		t.Errorf("flag-2 Version = %d, want 2", f2.Version)
	}

	// Check segments
	seg1, ok := s.getSegment("seg-1")
	if !ok {
		t.Fatal("seg-1 should exist")
	}
	if seg1.Version != 1 {
		t.Errorf("seg-1 Version = %d, want 1", seg1.Version)
	}

	seg2, ok := s.getSegment("seg-2")
	if !ok {
		t.Fatal("seg-2 should exist")
	}
	if seg2.Version != 2 {
		t.Errorf("seg-2 Version = %d, want 2", seg2.Version)
	}
}

func TestStore_SetAll_ReplacesExisting(t *testing.T) {
	s := newStore()

	// Initial data
	s.setAll(
		[]flagDTO{{Key: "flag-1", Version: 1}},
		[]segmentDTO{{Key: "seg-1", Version: 1}},
	)

	// Replace with new data
	s.setAll(
		[]flagDTO{{Key: "flag-2", Version: 2}},
		[]segmentDTO{{Key: "seg-2", Version: 2}},
	)

	// Old data should be gone
	_, ok := s.getFlag("flag-1")
	if ok {
		t.Error("flag-1 should no longer exist after setAll")
	}
	_, ok = s.getSegment("seg-1")
	if ok {
		t.Error("seg-1 should no longer exist after setAll")
	}

	// New data should be present
	_, ok = s.getFlag("flag-2")
	if !ok {
		t.Error("flag-2 should exist")
	}
	_, ok = s.getSegment("seg-2")
	if !ok {
		t.Error("seg-2 should exist")
	}
}

func TestStore_SetFlag(t *testing.T) {
	s := newStore()

	s.setAll(
		[]flagDTO{{Key: "flag-1", Version: 1, Enabled: false}},
		nil,
	)

	// Update the flag
	s.setFlag(flagDTO{Key: "flag-1", Version: 2, Enabled: true})

	f, ok := s.getFlag("flag-1")
	if !ok {
		t.Fatal("flag-1 should exist")
	}
	if f.Version != 2 {
		t.Errorf("Version = %d, want 2", f.Version)
	}
	if !f.Enabled {
		t.Error("flag-1 should be enabled after update")
	}
}

func TestStore_SetFlag_New(t *testing.T) {
	s := newStore()

	s.setFlag(flagDTO{Key: "new-flag", Version: 1, Enabled: true})

	f, ok := s.getFlag("new-flag")
	if !ok {
		t.Fatal("new-flag should exist")
	}
	if f.Version != 1 {
		t.Errorf("Version = %d, want 1", f.Version)
	}
}

func TestStore_GetFlag_NotFound(t *testing.T) {
	s := newStore()

	_, ok := s.getFlag("nonexistent")
	if ok {
		t.Error("nonexistent flag should not be found")
	}
}

func TestStore_GetSegment_NotFound(t *testing.T) {
	s := newStore()

	_, ok := s.getSegment("nonexistent")
	if ok {
		t.Error("nonexistent segment should not be found")
	}
}

func TestStore_AllSegments_ReturnsCopy(t *testing.T) {
	s := newStore()

	s.setAll(nil, []segmentDTO{
		{Key: "seg-1", Version: 1},
		{Key: "seg-2", Version: 2},
	})

	segs := s.allSegments()
	if len(segs) != 2 {
		t.Fatalf("allSegments returned %d, want 2", len(segs))
	}

	// Modify the returned copy — should not affect the store.
	delete(segs, "seg-1")

	// Original should still have seg-1.
	seg1, ok := s.getSegment("seg-1")
	if !ok {
		t.Error("seg-1 should still exist in store after modifying copy")
	}
	if seg1.Version != 1 {
		t.Errorf("seg-1 Version = %d, want 1", seg1.Version)
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := newStore()

	// Seed the store.
	s.setAll(
		[]flagDTO{{Key: "flag-0", Version: 1, Enabled: true}},
		[]segmentDTO{{Key: "seg-0", Version: 1}},
	)

	var wg sync.WaitGroup
	n := 100

	// 50 goroutines writing
	for i := 0; i < n/2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("flag-%d", i)
			s.setFlag(flagDTO{Key: key, Version: i, Enabled: true})
		}(i)
	}

	// 25 goroutines reading flags
	for i := 0; i < n/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.getFlag("flag-0")
		}()
	}

	// 25 goroutines reading segments
	for i := 0; i < n/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.allSegments()
		}()
	}

	wg.Wait()

	// Verify the store is still consistent.
	f, ok := s.getFlag("flag-0")
	if !ok {
		t.Error("flag-0 should still exist after concurrent access")
	}
	_ = f

	seg, ok := s.getSegment("seg-0")
	if !ok {
		t.Error("seg-0 should still exist after concurrent access")
	}
	_ = seg
}
