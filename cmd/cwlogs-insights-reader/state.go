package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is what survives a restart: the watermark (every window ending at or
// before it was emitted) and the events already emitted whose timestamps still
// fall inside the overlap, by @ptr. Together they make a restart neither drop
// nor repeat an event.
type State struct {
	Watermark time.Time `json:"watermark"`
	// Seen maps @ptr to the event's timestamp in epoch milliseconds.
	Seen map[string]int64 `json:"seen"`
}

// NewState is an empty state.
func NewState() *State { return &State{Seen: map[string]int64{}} }

// LoadState reads path; a missing file is an empty state.
func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewState(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	s := NewState()
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}

	if s.Seen == nil {
		s.Seen = map[string]int64{}
	}

	return s, nil
}

// Save writes the state atomically (temp file, fsync, rename).
func (s *State) Save(path string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name()) //nolint:errcheck

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close() //nolint:errcheck

		return err
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// Prune forgets events older than before: no later window starts earlier.
func (s *State) Prune(before time.Time) {
	cut := before.UnixMilli()

	for k, ts := range s.Seen {
		if ts < cut {
			delete(s.Seen, k)
		}
	}
}
