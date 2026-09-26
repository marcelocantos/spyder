// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const dayLayout = "2006-01-02"

// Store is a daily-JSONL battery history under a directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Query selects samples from the store. Zero Since/Until mean no bound
// on that side. DeviceID, when set, matches Sample.DeviceID exactly.
type Query struct {
	Since    time.Time
	Until    time.Time
	DeviceID string
}

// Open prepares dir (mkdir) and returns a store rooted there.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("battery: empty store dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("battery: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store root.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Append writes one sample to the UTC day's JSONL file.
func (s *Store) Append(sm Sample) error {
	if s == nil {
		return fmt.Errorf("battery: nil store")
	}
	if sm.TS.IsZero() {
		sm.TS = time.Now().UTC()
	} else {
		sm.TS = sm.TS.UTC()
	}
	line, err := json.Marshal(sm)
	if err != nil {
		return err
	}
	path := s.dayPath(sm.TS)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// Query returns samples in [Since, Until) (open on the right when Until
// is set), sorted by timestamp then device_id. Malformed lines are skipped.
func (s *Store) Query(q Query) ([]Sample, error) {
	if s == nil {
		return nil, fmt.Errorf("battery: nil store")
	}
	days, err := s.dayFiles(q.Since, q.Until)
	if err != nil {
		return nil, err
	}
	var out []Sample
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, path := range days {
		samples, err := readDay(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, sm := range samples {
			if !q.Since.IsZero() && sm.TS.Before(q.Since) {
				continue
			}
			if !q.Until.IsZero() && !sm.TS.Before(q.Until) {
				continue
			}
			if q.DeviceID != "" && sm.DeviceID != q.DeviceID {
				continue
			}
			out = append(out, sm)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TS.Equal(out[j].TS) {
			return out[i].TS.Before(out[j].TS)
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out, nil
}

// Prune deletes daily files whose date is older than now-retention.
func (s *Store) Prune(now time.Time, retention time.Duration) error {
	if s == nil {
		return fmt.Errorf("battery: nil store")
	}
	if retention <= 0 {
		return nil
	}
	cutoff := now.UTC().Add(-retention).Format(dayLayout)
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".jsonl")
		if _, perr := time.Parse(dayLayout, day); perr != nil {
			continue
		}
		if day < cutoff {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (s *Store) dayPath(ts time.Time) string {
	return filepath.Join(s.dir, ts.UTC().Format(dayLayout)+".jsonl")
}

func (s *Store) dayFiles(since, until time.Time) ([]string, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sinceDay, untilDay string
	if !since.IsZero() {
		sinceDay = since.UTC().Format(dayLayout)
	}
	if !until.IsZero() {
		// A sample at exactly until is excluded; the day's file may
		// still hold earlier samples, so include until's calendar day.
		untilDay = until.UTC().Format(dayLayout)
	}
	var paths []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".jsonl")
		if _, perr := time.Parse(dayLayout, day); perr != nil {
			continue
		}
		if sinceDay != "" && day < sinceDay {
			continue
		}
		if untilDay != "" && day > untilDay {
			continue
		}
		paths = append(paths, filepath.Join(s.dir, e.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func readDay(path string) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var sm Sample
		if err := json.Unmarshal([]byte(line), &sm); err != nil {
			continue
		}
		out = append(out, sm)
	}
	return out, sc.Err()
}
