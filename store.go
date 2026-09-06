package featureflip

import (
	"reflect"
	"sync"
)

// store is a thread-safe in-memory store for flag and segment definitions.
// It uses sync.RWMutex to allow concurrent reads with exclusive writes.
type store struct {
	mu       sync.RWMutex
	flags    map[string]flagDTO
	segments map[string]segmentDTO
}

// newStore creates a new empty store.
func newStore() *store {
	return &store{
		flags:    make(map[string]flagDTO),
		segments: make(map[string]segmentDTO),
	}
}

// setAll replaces all flags and segments in the store atomically, returning
// the flag keys whose evaluated value may have moved (nil when nothing did).
//
// The diff runs INSIDE the lock, with the previous snapshot still in place.
// Reading the previous state from outside and diffing after the write would
// lose any change a concurrent writer landed in between — the notification
// would silently under-report, which is the one direction that matters.
func (s *store) setAll(flags []flagDTO, segments []segmentDTO) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := diffSnapshot(s.flags, s.segments, flags, segments)

	s.flags = make(map[string]flagDTO, len(flags))
	for _, f := range flags {
		s.flags[f.Key] = f
	}

	s.segments = make(map[string]segmentDTO, len(segments))
	for _, seg := range segments {
		s.segments[seg.Key] = seg
	}

	return changed
}

// setFlag adds or updates a single flag, returning the keys whose evaluated
// value may have moved — the flag itself plus anything depending on it through
// a prerequisite. Returns nil when the incoming flag is identical to the one
// already held, so a redundant delta does not wake every listener.
func (s *store) setFlag(flag flagDTO) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if previous, ok := s.flags[flag.Key]; ok && reflect.DeepEqual(previous, flag) {
		return nil
	}
	s.flags[flag.Key] = flag

	changed := map[string]struct{}{flag.Key: {}}
	addPrerequisiteDependents(s.flagsSliceLocked(), changed)
	return sortedKeys(changed)
}

// removeFlag removes a flag from the store, returning the keys whose evaluated
// value may have moved. No-op — and nil — if the key was not held.
//
// Dependents are resolved from what REMAINS after the delete: they still carry
// the now-dangling prerequisite row, so their evaluated value moves to their
// off variation with [ReasonPrerequisiteFailed].
func (s *store) removeFlag(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.flags[key]; !ok {
		return nil
	}
	delete(s.flags, key)

	changed := map[string]struct{}{key: {}}
	addPrerequisiteDependents(s.flagsSliceLocked(), changed)
	return sortedKeys(changed)
}

// flagsSliceLocked renders the held flags as a slice. The caller must hold at
// least a read lock.
func (s *store) flagsSliceLocked() []flagDTO {
	flags := make([]flagDTO, 0, len(s.flags))
	for _, flag := range s.flags {
		flags = append(flags, flag)
	}
	return flags
}

// getFlag retrieves a flag by key. Returns the flag and true if found,
// or a zero value and false if not found.
func (s *store) getFlag(key string) (flagDTO, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, ok := s.flags[key]
	return f, ok
}

// getSegment retrieves a segment by key. Returns the segment and true if found,
// or a zero value and false if not found.
func (s *store) getSegment(key string) (segmentDTO, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seg, ok := s.segments[key]
	return seg, ok
}

// allSegments returns a copy of all segments in the store.
func (s *store) allSegments() map[string]segmentDTO {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]segmentDTO, len(s.segments))
	for k, v := range s.segments {
		result[k] = v
	}
	return result
}

// allFlags returns a copy of all flags in the store. Used by the evaluator
// to resolve prerequisites.
func (s *store) allFlags() map[string]flagDTO {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]flagDTO, len(s.flags))
	for k, v := range s.flags {
		result[k] = v
	}
	return result
}
