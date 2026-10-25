package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"IF3110-K02-09/internal/access"
)

var ErrNotFound = errors.New("record not found")
var ErrConflict = errors.New("record already exists")

// User is an internal record. Never serialize it in an HTTP response.
type User struct {
	ID               int
	Username         string
	PasswordHash     string
	Admin            bool
	Disabled         bool
	SecurityVersion  uint64
	PrivilegeVersion uint64
}
type Group struct {
	ID      int                     `json:"id"`
	Name    string                  `json:"name"`
	Members map[int]bool            `json:"members"`
	Grants  map[string]access.Level `json:"grants"`
}
type storeData struct {
	Users     map[int]User
	Groups    map[int]Group
	NextUser  int
	NextGroup int
}
type Store struct {
	mu   sync.RWMutex
	path string
	data storeData
}

// Identity storage must remain separate from user-managed target databases.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: storeData{Users: map[int]User{}, Groups: map[int]Group{}, NextUser: 1, NextGroup: 1}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Users == nil || s.data.Groups == nil || s.data.NextUser < 1 || s.data.NextGroup < 1 {
		return nil, errors.New("invalid identity store")
	}
	return s, nil
}

// Failed disk writes must not change live state.
func (s *Store) mutate(fn func(*storeData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	var next storeData
	if err = json.Unmarshal(b, &next); err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	// A reduced effective grant revokes sessions; an increase requires rotation.
	for id, u := range next.Users {
		old, exists := s.data.Users[id]
		if !exists {
			continue
		}
		before, after := principal(s.data, old), principal(next, u)
		reduced, increased := (!old.Disabled && u.Disabled) || (old.Admin && !u.Admin) || old.PasswordHash != u.PasswordHash, !old.Admin && u.Admin
		for db, level := range before.Databases {
			if rank(after.Databases[db]) < rank(level) {
				reduced = true
			}
		}
		for db, level := range after.Databases {
			if rank(level) > rank(before.Databases[db]) {
				increased = true
			}
		}
		if reduced {
			u.SecurityVersion++
		}
		if increased {
			u.PrivilegeVersion++
		}
		next.Users[id] = u
	}
	if s.path != "" {
		b, err = json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".identity-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(b)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(name, s.path); err != nil {
			return err
		}
	}
	s.data = next
	return nil
}
func rank(l access.Level) int {
	if l == access.Edit {
		return 2
	}
	if l == access.View {
		return 1
	}
	return 0
}
func principal(d storeData, u User) access.Principal {
	p := access.Principal{ID: u.ID, Username: u.Username, Admin: u.Admin, Databases: map[string]access.Level{}}
	for _, g := range d.Groups {
		if g.Members[u.ID] {
			for db, grant := range g.Grants {
				p.Databases[db] = access.Merge(p.Databases[db], grant)
			}
		}
	}
	return p
}
func (s *Store) Snapshot(id int) (User, access.Principal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.data.Users[id]
	if !ok || u.Disabled {
		return User{}, access.Principal{}, ErrNotFound
	}
	return u, principal(s.data, u), nil
}
func (s *Store) FindUsername(name string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.data.Users {
		if u.Username == name {
			return u, true
		}
	}
	return User{}, false
}
func (s *Store) Empty() bool { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.data.Users) == 0 }
func (s *Store) CreateUser(name, password string, admin bool) (int, error) {
	name = strings.TrimSpace(name)
	if len(name) < 3 || len(name) > 64 || strings.ContainsAny(name, "\r\n\t") {
		return 0, errors.New("username must contain 3 to 64 bytes")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int
	err = s.mutate(func(d *storeData) error {
		for _, u := range d.Users {
			if u.Username == name {
				return ErrConflict
			}
		}
		id = d.NextUser
		d.NextUser++
		d.Users[id] = User{ID: id, Username: name, PasswordHash: hash, Admin: admin}
		return nil
	})
	return id, err
}
func (s *Store) UpdateUser(id int, admin, disabled bool, password string) error {
	var hash string
	if password != "" {
		var err error
		hash, err = HashPassword(password)
		if err != nil {
			return err
		}
	}
	return s.mutate(func(d *storeData) error {
		u, ok := d.Users[id]
		if !ok {
			return ErrNotFound
		}
		if u.Admin && !u.Disabled && (!admin || disabled) {
			count := 0
			for _, candidate := range d.Users {
				if candidate.Admin && !candidate.Disabled {
					count++
				}
			}
			if count <= 1 {
				return errors.New("cannot remove the last active platform admin")
			}
		}
		u.Admin, u.Disabled = admin, disabled
		if hash != "" {
			u.PasswordHash = hash
		}
		d.Users[id] = u
		return nil
	})
}
func (s *Store) CreateGroup(name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return 0, errors.New("invalid group name")
	}
	var id int
	err := s.mutate(func(d *storeData) error {
		for _, g := range d.Groups {
			if g.Name == name {
				return ErrConflict
			}
		}
		id = d.NextGroup
		d.NextGroup++
		d.Groups[id] = Group{ID: id, Name: name, Members: map[int]bool{}, Grants: map[string]access.Level{}}
		return nil
	})
	return id, err
}
func (s *Store) SetMembership(groupID, userID int, member bool) error {
	return s.mutate(func(d *storeData) error {
		g, ok := d.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		if _, ok = d.Users[userID]; !ok {
			return ErrNotFound
		}
		if member {
			g.Members[userID] = true
		} else {
			delete(g.Members, userID)
		}
		return nil
	})
}
func (s *Store) SetGrant(groupID int, database string, level access.Level) error {
	if database == "" || (level != access.None && level != access.View && level != access.Edit) {
		return errors.New("invalid database grant")
	}
	return s.mutate(func(d *storeData) error {
		g, ok := d.Groups[groupID]
		if !ok {
			return ErrNotFound
		}
		if level == access.None {
			delete(g.Grants, database)
		} else {
			g.Grants[database] = level
		}
		return nil
	})
}
