package auth

import (
	"sync"
	"testing"
	"time"
)

func testSessions(t *testing.T) (*Sessions, *time.Time) {
	t.Helper()
	s, err := NewSessions(DefaultSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}
func TestSessionRotationGraceAndAbsolute(t *testing.T) {
	s, now := testSessions(t)
	u := User{ID: 1}
	p, old, err := s.New(u, "")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(s.cfg.Rotation)
	rotated, current, err := s.Use(old, u)
	if err != nil || current == old || rotated.CreatedAt != p.CreatedAt || rotated.CSRF != p.CSRF {
		t.Fatal("rotation failed or extended absolute age")
	}
	_, retry, err := s.Use(old, u)
	if err != nil || retry != current {
		t.Fatal("lost response cannot recover current cookie during grace")
	}
	*now = now.Add(s.cfg.Grace)
	if _, err = s.Get(old); err == nil {
		t.Fatal("old token accepted after grace")
	}
	for now.Before(p.CreatedAt.Add(s.cfg.Absolute)) {
		*now = now.Add(10 * time.Minute)
		if !now.Before(p.CreatedAt.Add(s.cfg.Absolute)) {
			break
		}
		_, current, err = s.Use(current, u)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Get(current); err == nil {
		t.Fatal("absolute expiry bypassed by activity")
	}
}
func TestSessionIdleFixationElevationAndRevocation(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		s, now := testSessions(t)
		_, token, _ := s.New(User{ID: 1}, "")
		*now = now.Add(s.cfg.Idle)
		if _, err := s.Get(token); err == nil {
			t.Fatal("idle session accepted")
		}
	})
	t.Run("login fixation", func(t *testing.T) {
		s, _ := testSessions(t)
		_, anonymous, _ := s.New(User{}, "")
		_, token, _ := s.New(User{ID: 1}, anonymous)
		if token == anonymous {
			t.Fatal("login reused token")
		}
		if _, err := s.Get(anonymous); err == nil {
			t.Fatal("anonymous token elevated")
		}
	})
	t.Run("privilege elevation", func(t *testing.T) {
		s, _ := testSessions(t)
		u := User{ID: 1}
		_, old, _ := s.New(u, "")
		u.PrivilegeVersion++
		_, next, err := s.Use(old, u)
		if err != nil || old == next {
			t.Fatal("elevation did not rotate")
		}
		s.Revoke(old)
		if _, err = s.Get(next); err == nil {
			t.Fatal("logout through alias did not revoke family")
		}
	})
	t.Run("revoked privilege", func(t *testing.T) {
		s, _ := testSessions(t)
		u := User{ID: 1}
		_, token, _ := s.New(u, "")
		u.SecurityVersion++
		if _, _, err := s.Use(token, u); err == nil {
			t.Fatal("revoked session accepted")
		}
	})
}
func TestConcurrentRotation(t *testing.T) {
	s, now := testSessions(t)
	u := User{ID: 1}
	_, old, _ := s.New(u, "")
	*now = now.Add(s.cfg.Rotation)
	var wg sync.WaitGroup
	tokens := make(chan string, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, token, err := s.Use(old, u)
			if err != nil {
				t.Error(err)
			}
			tokens <- token
		}()
	}
	wg.Wait()
	close(tokens)
	current := ""
	for token := range tokens {
		if current != "" && token != current {
			t.Fatal("parallel requests rotated multiple times")
		}
		current = token
	}
}

func TestSessionCapacityAllowsLoginReplacement(t *testing.T) {
	s, _ := testSessions(t)
	s.cfg.MaxSessions = 1
	_, anonymous, err := s.New(User{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.New(User{}, ""); err == nil {
		t.Fatal("capacity limit ignored")
	}
	if _, _, err = s.New(User{ID: 1}, anonymous); err != nil {
		t.Fatalf("login cannot replace existing session at capacity: %v", err)
	}
}
