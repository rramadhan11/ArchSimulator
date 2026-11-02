package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// store keeps saved designs and their version history as JSON files:
//
//	<dir>/<id>/meta.json   name, timestamps, latest version
//	<dir>/<id>/v<N>.json   one immutable file per saved version
//
// One user and designs of a few KB do not need a database; plain files are
// easy to back up (copy the directory) and to inspect. Writes go to a temp
// file that is synced and renamed, so a crash never leaves a torn file.
type store struct {
	dir string
	mu  sync.Mutex // serializes writers; reads of immutable versions need no lock
	now func() time.Time
}

type designMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Versions  int       `json:"versions"`
}

type version struct {
	Version      int             `json:"version"`
	SavedAt      time.Time       `json:"savedAt"`
	Note         string          `json:"note,omitempty"`
	Name         string          `json:"name"`
	Architecture json.RawMessage `json:"architecture"`
}

// versionInfo is a version without its (large) architecture.
type versionInfo struct {
	Version int       `json:"version"`
	SavedAt time.Time `json:"savedAt"`
	Note    string    `json:"note,omitempty"`
	Name    string    `json:"name"`
}

var (
	errNotFound  = errors.New("not found")
	errUnchanged = errors.New("unchanged")
	validID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

const (
	maxNameLen = 200
	maxNoteLen = 500
)

func newStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	return &store{dir: dir, now: time.Now}, nil
}

// checkDesign validates what the editor and engine rely on to open a design;
// the engine validates the rest when it runs.
func checkDesign(name, note string, arch json.RawMessage) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len(name) > maxNameLen:
		return fmt.Errorf("name must be 1-%d characters", maxNameLen)
	case len(note) > maxNoteLen:
		return fmt.Errorf("note must be at most %d characters", maxNoteLen)
	}
	var a struct {
		Nodes    []json.RawMessage `json:"nodes"`
		Edges    []json.RawMessage `json:"edges"`
		Workload map[string]any    `json:"workload"`
	}
	if err := json.Unmarshal(arch, &a); err != nil || a.Nodes == nil || a.Edges == nil || a.Workload == nil {
		return errors.New("architecture must be an object with nodes, edges and workload")
	}
	return nil
}

func (s *store) list() ([]designMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []designMeta{}
	for _, e := range entries {
		if !e.IsDir() || !validID.MatchString(e.Name()) {
			continue
		}
		m, err := s.meta(e.Name())
		if errors.Is(err, errNotFound) {
			continue // created but never completed; ignored
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b designMeta) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return out, nil
}

func (s *store) meta(id string) (designMeta, error) {
	var m designMeta
	if !validID.MatchString(id) {
		return m, errNotFound
	}
	err := readJSON(filepath.Join(s.dir, id, "meta.json"), &m)
	return m, err
}

func (s *store) versions(id string) ([]versionInfo, error) {
	m, err := s.meta(id)
	if err != nil {
		return nil, err
	}
	out := make([]versionInfo, 0, m.Versions)
	for n := m.Versions; n >= 1; n-- {
		v, err := s.version(id, n)
		if err != nil {
			return nil, err
		}
		out = append(out, versionInfo{Version: v.Version, SavedAt: v.SavedAt, Note: v.Note, Name: v.Name})
	}
	return out, nil
}

func (s *store) version(id string, n int) (version, error) {
	var v version
	if !validID.MatchString(id) || n < 1 {
		return v, errNotFound
	}
	err := readJSON(filepath.Join(s.dir, id, "v"+strconv.Itoa(n)+".json"), &v)
	return v, err
}

// create saves a new design as version 1.
func (s *store) create(name, note string, arch json.RawMessage) (designMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return designMeta{}, err
	}
	if err := os.Mkdir(filepath.Join(s.dir, id), 0o700); err != nil {
		return designMeta{}, err
	}
	now := s.now().UTC()
	m := designMeta{ID: id, CreatedAt: now}
	return m, s.appendLocked(&m, name, note, arch, now)
}

// save appends a version unless the design equals the latest one, which
// returns errUnchanged with the current meta.
func (s *store) save(id, name, note string, arch json.RawMessage) (designMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.meta(id)
	if err != nil {
		return m, err
	}
	latest, err := s.version(id, m.Versions)
	if err != nil {
		return m, err
	}
	if same, err := equalJSON(latest.Architecture, arch); err != nil {
		return m, err
	} else if same && latest.Name == strings.TrimSpace(name) {
		return m, errUnchanged
	}
	return m, s.appendLocked(&m, name, note, arch, s.now().UTC())
}

func (s *store) appendLocked(m *designMeta, name, note string, arch json.RawMessage, now time.Time) error {
	v := version{Version: m.Versions + 1, SavedAt: now, Note: strings.TrimSpace(note), Name: strings.TrimSpace(name), Architecture: arch}
	// The version file goes first: meta.json only ever points at complete versions.
	if err := writeJSON(filepath.Join(s.dir, m.ID, "v"+strconv.Itoa(v.Version)+".json"), v); err != nil {
		return err
	}
	m.Versions, m.Name, m.UpdatedAt = v.Version, v.Name, now
	return writeJSON(filepath.Join(s.dir, m.ID, "meta.json"), m)
}

func (s *store) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.meta(id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, id))
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// equalJSON compares documents by value, ignoring key order and whitespace.
func equalJSON(a, b json.RawMessage) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb), nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSON replaces path atomically: temp file, fsync, rename, fsync dir.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
