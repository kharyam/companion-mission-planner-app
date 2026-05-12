// Package slotorder persists per-device slot ordering across runs.
//
// DJI Fly returns slots in whatever order it pleases (GUID-ish);
// users want to organize their mission list their way. We store a
// preferred-order array per device-id in a sidecar JSON, then apply
// it as a decorator when the device package emits the slot list.
//
// Same shape as internal/names but list-valued. Kept as a separate
// file so the user can hand-edit ordering without touching names.
package slotorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store holds (deviceID, ordered-GUID-list) pairs.
type Store struct {
	path string
	mu   sync.RWMutex
	m    map[string][]string
}

// New creates a Store backed by path. Empty path = memory-only.
// Returns an error only on malformed existing JSON; a missing file
// is fine.
func New(path string) (*Store, error) {
	s := &Store{path: path, m: map[string][]string{}}
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

// Get returns a copy of the saved order for deviceID, or nil if none.
func (s *Store) Get(deviceID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.m == nil {
		return nil
	}
	src := s.m[deviceID]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Set replaces the saved order for deviceID and persists.
func (s *Store) Set(deviceID string, order []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(order) == 0 {
		delete(s.m, deviceID)
	} else {
		cp := make([]string, len(order))
		copy(cp, order)
		s.m[deviceID] = cp
	}
	return s.flushLocked()
}

// Reorder applies the saved order to slots, preserving any slot whose
// GUID isn't in the saved list by appending it after the ordered ones.
// Slots in the saved order that no longer exist on the device are
// silently dropped (the slot package returns whatever the device has).
//
// This is the only sort function we expose — callers always go through
// it so behavior stays consistent.
func Reorder[T any](order []string, items []T, guidOf func(T) string) []T {
	if len(order) == 0 || len(items) == 0 {
		return items
	}
	byGUID := make(map[string]T, len(items))
	for _, it := range items {
		byGUID[guidOf(it)] = it
	}
	out := make([]T, 0, len(items))
	used := make(map[string]bool, len(items))
	for _, g := range order {
		if v, ok := byGUID[g]; ok {
			out = append(out, v)
			used[g] = true
		}
	}
	for _, it := range items {
		if !used[guidOf(it)] {
			out = append(out, it)
		}
	}
	return out
}

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
