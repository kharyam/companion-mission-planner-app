// Package managed persists per-slot "is this slot under our control"
// flags. Default is managed=true; the store records explicit overrides
// so the file only grows when the user opts a slot out.
//
// Same shape as internal/names but boolean-valued. Lives next to
// names + slot-order in the platform config dir.
package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	path string
	mu   sync.RWMutex
	m    map[string]map[string]bool // deviceID → guid → managed
}

func New(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]map[string]bool{}}
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

// Get returns whether deviceID/guid is currently marked managed.
// Slots without an explicit record default to managed=true.
func (s *Store) Get(deviceID, guid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if dev, ok := s.m[deviceID]; ok {
		if v, ok := dev[guid]; ok {
			return v
		}
	}
	return true
}

// Set records an explicit managed value and persists. managed=true
// (the default) is stored as a deletion so the sidecar file stays
// minimal.
func (s *Store) Set(deviceID, guid string, managed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if managed {
		if dev, ok := s.m[deviceID]; ok {
			delete(dev, guid)
			if len(dev) == 0 {
				delete(s.m, deviceID)
			}
		}
	} else {
		if s.m[deviceID] == nil {
			s.m[deviceID] = map[string]bool{}
		}
		s.m[deviceID][guid] = false
	}
	return s.flushLocked()
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
