package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

var ErrSession = errors.New("invalid or expired session")

type SessionConfig struct {
	Idle, Absolute, Rotation, Grace time.Duration
	MaxSessions                     int
}

func DefaultSessionConfig() SessionConfig {
	return SessionConfig{30 * time.Minute, 8 * time.Hour, 15 * time.Minute, 30 * time.Second, 10000}
}

type Session struct {
	UserID                            int
	SecurityVersion, PrivilegeVersion uint64
	CSRF                              string
	CreatedAt, LastSeen, RotatedAt    time.Time
	current                           string
}
type alias struct {
	session *Session
	until   time.Time
}
type Sessions struct {
	mu     sync.Mutex
	cfg    SessionConfig
	now    func() time.Time
	tokens map[string]alias
}

func NewSessions(cfg SessionConfig) (*Sessions, error) {
	if cfg.Idle <= 0 || cfg.Absolute <= 0 || cfg.Rotation <= 0 || cfg.Grace <= 0 || cfg.Grace >= cfg.Rotation || cfg.MaxSessions <= 0 {
		return nil, errors.New("invalid session configuration")
	}
	return &Sessions{cfg: cfg, now: time.Now, tokens: map[string]alias{}}, nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Sessions) expired(p *Session, now time.Time) bool {
	return !now.Before(p.CreatedAt.Add(s.cfg.Absolute)) || !now.Before(p.LastSeen.Add(s.cfg.Idle))
}
func (s *Sessions) cleanup(now time.Time) {
	for token, a := range s.tokens {
		if s.expired(a.session, now) || (!a.until.IsZero() && !now.Before(a.until)) {
			delete(s.tokens, token)
		}
	}
}
func (s *Sessions) lookup(token string, now time.Time) (*Session, error) {
	a, ok := s.tokens[token]
	if !ok || s.expired(a.session, now) || (!a.until.IsZero() && !now.Before(a.until)) {
		return nil, ErrSession
	}
	return a.session, nil
}

// Revoke pre-login IDs immediately to prevent session fixation.
func (s *Sessions) New(user User, replace string) (Session, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanup(now)
	count := 0
	for _, a := range s.tokens {
		if a.until.IsZero() {
			count++
		}
	}
	_, replacing := s.tokens[replace]
	if count >= s.cfg.MaxSessions && !replacing {
		return Session{}, "", errors.New("session capacity reached")
	}
	token, err := randomToken()
	if err != nil {
		return Session{}, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, "", err
	}
	if a, ok := s.tokens[replace]; ok {
		s.revoke(a.session)
	}
	p := &Session{UserID: user.ID, SecurityVersion: user.SecurityVersion, PrivilegeVersion: user.PrivilegeVersion, CSRF: csrf, CreatedAt: now, LastSeen: now, RotatedAt: now, current: token}
	s.tokens[token] = alias{session: p}
	return *p, token, nil
}
func (s *Sessions) Get(token string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.lookup(token, s.now())
	if err != nil {
		return Session{}, err
	}
	return *p, nil
}

// Rotation preserves absolute expiry and CSRF for concurrent requests.
func (s *Sessions) Use(token string, user User) (Session, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanup(now)
	p, err := s.lookup(token, now)
	if err != nil {
		return Session{}, "", err
	}
	if p.UserID != user.ID || p.SecurityVersion != user.SecurityVersion || user.Disabled {
		s.revoke(p)
		return Session{}, "", ErrSession
	}
	if p.PrivilegeVersion != user.PrivilegeVersion || !now.Before(p.RotatedAt.Add(s.cfg.Rotation)) {
		next, err := randomToken()
		if err != nil {
			return Session{}, "", err
		}
		s.tokens[p.current] = alias{session: p, until: now.Add(s.cfg.Grace)}
		p.current, p.RotatedAt, p.PrivilegeVersion = next, now, user.PrivilegeVersion
		s.tokens[next] = alias{session: p}
	}
	p.LastSeen = now
	return *p, p.current, nil
}
func (s *Sessions) revoke(p *Session) {
	for token, a := range s.tokens {
		if a.session == p {
			delete(s.tokens, token)
		}
	}
}
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.tokens[token]; ok {
		s.revoke(a.session)
	}
}
func (s *Sessions) RevokeUser(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, a := range s.tokens {
		if a.session.UserID == id {
			delete(s.tokens, token)
		}
	}
}
