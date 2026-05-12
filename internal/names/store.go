// Package names persists user-set slot names across runs.
//
// DJI Fly stores the user-editable slot name in its private Android
// storage, which we can't reach. We can't make DJI Fly's mission-list
// text label show our name either (Phase 2B confirmed this).
//
// What we *can* do is remember the user's preferred name for each
// (device, slot) pair on the host side. That name surfaces in our own
// UI's slot list and is baked into the preview JPEG overlay (the
// thumbnail DJI Fly DOES render is visually labeled with it).
//
// The store is a single JSON file in the platform config directory.
// Concurrent-safe with an RWMutex; writes hit disk every time so we
// never lose data on a crash.
package names

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store maps (deviceID, slotGUID) to a user-assigned name.
type Store struct {
	path string

	mu sync.RWMutex
	m  map[string]map[string]string // device -> guid -> name
}

// New creates a Store backed by path. If path is empty, the store
// runs in memory only (useful for tests). Returns an error only on
// malformed existing JSON; a missing file is fine.
func New(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]map[string]string{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Get returns the saved name for (deviceID, guid), or "" if none.
func (s *Store) Get(deviceID, guid string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.m == nil {
		return ""
	}
	return s.m[deviceID][guid]
}

// All returns a copy of the names for deviceID. Caller-owned map.
func (s *Store) All(deviceID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.m[deviceID]))
	for k, v := range s.m[deviceID] {
		out[k] = v
	}
	return out
}

// Set assigns a name and immediately persists. Empty name is treated
// as a removal so callers can use one method for both.
func (s *Store) Set(deviceID, guid, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		if _, ok := s.m[deviceID]; ok {
			delete(s.m[deviceID], guid)
			if len(s.m[deviceID]) == 0 {
				delete(s.m, deviceID)
			}
		}
	} else {
		if s.m[deviceID] == nil {
			s.m[deviceID] = map[string]string{}
		}
		s.m[deviceID][guid] = name
	}
	return s.flushLocked()
}

// Remove clears the saved name. Equivalent to Set with an empty value.
func (s *Store) Remove(deviceID, guid string) error {
	return s.Set(deviceID, guid, "")
}

// flushLocked writes the in-memory map to disk. Caller must hold mu.
// Atomic via write-temp + rename so a crash mid-write doesn't leave
// a partial file readers will reject on next load.
func (s *Store) flushLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
