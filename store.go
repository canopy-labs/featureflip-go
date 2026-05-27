package featureflip

import "sync"

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

// setAll replaces all flags and segments in the store atomically.
func (s *store) setAll(flags []flagDTO, segments []segmentDTO) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.flags = make(map[string]flagDTO, len(flags))
	for _, f := range flags {
		s.flags[f.Key] = f
	}

	s.segments = make(map[string]segmentDTO, len(segments))
	for _, seg := range segments {
		s.segments[seg.Key] = seg
	}
}

// setFlag adds or updates a single flag in the store.
func (s *store) setFlag(flag flagDTO) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.flags[flag.Key] = flag
}

// removeFlag removes a flag from the store. No-op if key doesn't exist.
func (s *store) removeFlag(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.flags, key)
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
