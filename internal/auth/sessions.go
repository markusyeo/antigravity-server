package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

const maxSessions = 200

// Session is one signed-in device. Key is the SHA-256 of the cookie value, never
// the value itself.
type Session struct {
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	LastSeen  time.Time `json:"last_seen"`
	UserAgent string    `json:"user_agent,omitempty"`
	IP        string    `json:"ip,omitempty"`
}

func (s Session) Expired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}

// SessionStore holds active sessions and persists them across restarts.
type SessionStore struct {
	mu       sync.Mutex
	path     string
	ttl      time.Duration
	sessions map[string]*Session
	dirty    bool
	modTime  time.Time
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewSessionStore opens the store at path, dropping anything already expired.
func NewSessionStore(path string, ttl time.Duration) (*SessionStore, error) {
	s := &SessionStore{
		path:     path,
		ttl:      ttl,
		sessions: map[string]*Session{},
	}

	if err := s.loadFromDiskLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SessionStore) loadFromDiskLocked() error {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.sessions = map[string]*Session{}
			s.modTime = time.Time{}
			return nil
		}
		return err
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.sessions = map[string]*Session{}
			s.modTime = time.Time{}
			return nil
		}
		return err
	}

	var stored []*Session
	if err := json.Unmarshal(data, &stored); err != nil {
		s.modTime = info.ModTime()
		return nil
	}

	now := time.Now()
	loaded := make(map[string]*Session, len(stored))
	for _, sess := range stored {
		if sess.Key == "" || sess.Expired(now) {
			continue
		}
		loaded[sess.Key] = sess
	}
	s.sessions = loaded
	s.modTime = info.ModTime()
	s.dirty = false
	return nil
}

func (s *SessionStore) reloadIfChangedLocked() {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) && !s.modTime.IsZero() {
			s.sessions = map[string]*Session{}
			s.modTime = time.Time{}
			s.dirty = false
		}
		return
	}
	if !info.ModTime().Equal(s.modTime) {
		_ = s.loadFromDiskLocked()
	}
}

// Create issues a new session and returns the raw token for the cookie. Only its
// hash is retained.
func (s *SessionStore) Create(userAgent, ip string) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	sess := &Session{
		Key:       hashToken(raw),
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
		LastSeen:  now,
		UserAgent: truncate(userAgent, 200),
		IP:        ip,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	s.pruneLocked(now)
	s.sessions[sess.Key] = sess
	if len(s.sessions) > maxSessions {
		s.evictOldestLocked(len(s.sessions) - maxSessions)
	}

	return raw, s.persistLocked()
}

// Validate reports whether raw names a live session, refreshing its last-seen
// time at most once a minute to avoid writing on every request.
func (s *SessionStore) Validate(raw string) bool {
	if raw == "" {
		return false
	}
	key := hashToken(raw)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()

	sess, ok := s.sessions[key]
	if !ok {
		return false
	}
	if sess.Expired(now) {
		delete(s.sessions, key)
		s.dirty = true
		return false
	}

	if now.Sub(sess.LastSeen) > time.Minute {
		sess.LastSeen = now
		s.dirty = true
	}
	return true
}

// Revoke signs out one device.
func (s *SessionStore) Revoke(raw string) error {
	if raw == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	delete(s.sessions, hashToken(raw))
	return s.persistLocked()
}

// RevokeAll signs out every device and reports how many were affected.
func (s *SessionStore) RevokeAll() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	n := len(s.sessions)
	s.sessions = map[string]*Session{}
	return n, s.persistLocked()
}

// List returns active sessions, newest first.
func (s *SessionStore) List() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()

	out := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, *sess)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Count returns the number of active sessions.
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reloadIfChangedLocked()
	return len(s.sessions)
}

func (s *SessionStore) pruneLocked(now time.Time) {
	for key, sess := range s.sessions {
		if sess.Expired(now) {
			delete(s.sessions, key)
			s.dirty = true
		}
	}
}

func (s *SessionStore) evictOldestLocked(n int) {
	type kv struct {
		key string
		at  time.Time
	}
	all := make([]kv, 0, len(s.sessions))
	for key, sess := range s.sessions {
		all = append(all, kv{key, sess.LastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })

	for i := 0; i < n && i < len(all); i++ {
		delete(s.sessions, all[i].key)
	}
	s.dirty = true
}

func (s *SessionStore) persistLocked() error {
	list := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.dirty = false

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.modTime = info.ModTime()
	}
	return nil
}

// Flush writes pending last-seen updates. Call it periodically and on shutdown.
func (s *SessionStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty {
		return nil
	}
	return s.persistLocked()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
