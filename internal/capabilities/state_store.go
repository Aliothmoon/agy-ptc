package capabilities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

const (
	MaxStateEntryBytes = 512 * 1024      // 512 KB per key
	MaxStateTotalBytes = 4 * 1024 * 1024 // 4 MB per workspace session
)

// StateStore provides fast in-memory + disk-persisted key-value storage across PTC turns.
type StateStore struct {
	mu       sync.RWMutex
	dataDir  string
	filePath string
	entries  map[string]stateEntry
	seq      int64
	dirty    bool // pending changes; persisted by Flush (once per exec) instead of on every state.set
}

// Flush persists pending changes to disk.
func (s *StateStore) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty {
		s.saveToDiskLocked()
		s.dirty = false
	}
}

type stateEntry struct {
	Value     any    `json:"value"`
	Bytes     int    `json:"bytes"`
	UpdatedAt string `json:"updatedAt"`
}

// NewStateStore creates or loads a workspace-scoped state store under dataDir.
func NewStateStore(dataDir, workspaceDir string) *StateStore {
	s := &StateStore{
		dataDir: dataDir,
		entries: map[string]stateEntry{},
	}
	if dataDir != "" {
		h := sha256.Sum256([]byte(strings.ToLower(filepath.ToSlash(filepath.Clean(workspaceDir)))))
		id := hex.EncodeToString(h[:8])
		sessDir := filepath.Join(dataDir, "sessions")
		_ = os.MkdirAll(sessDir, 0o755)
		s.filePath = filepath.Join(sessDir, id+".json")
		s.loadFromDisk()
	}
	return s
}

func (s *StateStore) loadFromDisk() {
	if s.filePath == "" {
		return
	}
	raw, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var loaded map[string]stateEntry
	if err := json.Unmarshal(raw, &loaded); err == nil && loaded != nil {
		s.entries = loaded
	}
}

func (s *StateStore) saveToDiskLocked() {
	if s.filePath == "" {
		return
	}
	raw, err := json.Marshal(s.entries)
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.filePath)
}

// Set stores a JSON-serializable value under key.
func (s *StateStore) Set(key string, val any) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("state key cannot be empty")
	}
	if val == nil {
		s.Delete(key)
		return nil
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("state value must be JSON-serializable: %w", err)
	}
	if len(raw) > MaxStateEntryBytes {
		return fmt.Errorf("state value for '%s' (%d bytes) exceeds per-key limit (%d bytes)", key, len(raw), MaxStateEntryBytes)
	}

	// Normalize value through JSON so stored in-memory structure has clean detached maps/slices.
	var detached any
	if err := json.Unmarshal(raw, &detached); err != nil {
		detached = val
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	total := len(raw)
	for k, e := range s.entries {
		if k != key {
			total += e.Bytes
		}
	}
	if total > MaxStateTotalBytes {
		// Evict oldest auto-spill entries (`__result_*`) first before failing.
		s.evictAutoSpillsLocked(total - MaxStateTotalBytes)
		total = len(raw)
		for k, e := range s.entries {
			if k != key {
				total += e.Bytes
			}
		}
		if total > MaxStateTotalBytes {
			return fmt.Errorf("state store total size (%d bytes) exceeds session limit (%d bytes); call state.clear() or state.delete(key)", total, MaxStateTotalBytes)
		}
	}

	s.entries[key] = stateEntry{
		Value:     detached,
		Bytes:     len(raw),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.dirty = true
	return nil
}

// SaveAutoSpill saves an oversized return value into state so the model can filter/page it in the next turn without file spills.
func (s *StateStore) SaveAutoSpill(val any) string {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("__result_%d", s.seq)
	s.mu.Unlock()
	if err := s.Set(id, val); err != nil {
		return ""
	}
	_ = s.Set("__last_result", val)
	return id
}

// Get retrieves a detached copy of key's value (or nil if absent).
func (s *StateStore) Get(key string) any {
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	raw, err := json.Marshal(e.Value)
	if err != nil {
		return e.Value
	}
	var cp any
	if err := json.Unmarshal(raw, &cp); err != nil {
		return e.Value
	}
	return cp
}

// Delete removes a key from the store.
func (s *StateStore) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.entries[key]
	delete(s.entries, key)
	if existed {
		s.dirty = true
	}
	return existed
}

// Keys lists all keys currently in the store.
func (s *StateStore) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Clear wipes all entries in the session store.
func (s *StateStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]stateEntry{}
	s.dirty = true
}

func (s *StateStore) evictAutoSpillsLocked(bytesNeeded int) {
	type cand struct {
		key       string
		updatedAt string
		bytes     int
	}
	var cands []cand
	for k, e := range s.entries {
		if strings.HasPrefix(k, "__result_") {
			cands = append(cands, cand{key: k, updatedAt: e.UpdatedAt, bytes: e.Bytes})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].updatedAt < cands[j].updatedAt })
	freed := 0
	for _, c := range cands {
		delete(s.entries, c.key)
		freed += c.bytes
		if freed >= bytesNeeded {
			break
		}
	}
}

// StateBinder binds the `state` namespace (`get`, `set`, `delete`, `keys`, `clear`) into the sandbox.
type StateBinder struct {
	Store *StateStore
}

func (b *StateBinder) Bind(env *sandbox.ExecutionEnv) error {
	syncMethods := map[string]sandbox.SyncHostFunc{
		"get": func(_ context.Context, args []any) (any, error) {
			key, err := requireStringArg(args, 0, "key")
			if err != nil {
				return nil, err
			}
			return b.Store.Get(key), nil
		},
		"set": func(_ context.Context, args []any) (any, error) {
			key, err := requireStringArg(args, 0, "key")
			if err != nil {
				return nil, err
			}
			var val any
			if len(args) > 1 {
				val = args[1]
			}
			if err := b.Store.Set(key, val); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "key": key}, nil
		},
		"delete": func(_ context.Context, args []any) (any, error) {
			key, err := requireStringArg(args, 0, "key")
			if err != nil {
				return nil, err
			}
			return map[string]any{"deleted": b.Store.Delete(key)}, nil
		},
		"keys": func(_ context.Context, _ []any) (any, error) {
			return b.Store.Keys(), nil
		},
		"clear": func(_ context.Context, _ []any) (any, error) {
			b.Store.Clear()
			return map[string]any{"ok": true}, nil
		},
	}
	return env.SetNamespace("state", nil, syncMethods)
}
