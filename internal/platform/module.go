package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// SecurityDependencies wajib berasal dari modul A yang memakai identitas PostgreSQL yang sama dengan administrasi B.
type SecurityDependencies struct {
	Identity       VerifiedUserProvider
	PasswordHasher UserPasswordHasher
	Sessions       UserSessionRevoker
	CSRF           UserCSRFVerifier
}

type PlatformModule struct {
	Pools          *TargetConnectionManager
	Access         *EffectiveAccessService
	Authentication *AuthenticationRepository
	users          *UserService
	groups         *GroupService
	connections    *DatabaseConnectionService
	viewer         *ViewerService
	analysis       *QueryAnalysisService
	security       SecurityDependencies
}

// Komposisi memakai satu repository konfigurasi agar penulisan admin dan pool target berbagi invalidasi yang sama.
func NewPlatformModule(db *sql.DB, cipher *DatabasePasswordCipher, security SecurityDependencies) (*PlatformModule, error) {
	if db == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	repository := NewDatabaseConnectionRepository(db, cipher)
	pools, err := NewTargetConnectionManager(repository, TargetPoolOptions{})
	if err != nil {
		return nil, err
	}
	users := NewUserService(NewUserRepository(db), security.Identity, security.PasswordHasher, security.Sessions)
	access := NewEffectiveAccessService(NewEffectiveAccessRepository(db), security.Identity)
	return &PlatformModule{Pools: pools, Access: access, Authentication: NewAuthenticationRepository(db), users: users,
		groups: NewGroupService(NewGroupRepository(db), users), connections: NewDatabaseConnectionService(repository, pools, users),
		viewer: NewViewerService(access, pools, NewViewerMetadataRepository()), analysis: NewQueryAnalysisService(access, pools), security: security}, nil
}

func (m *PlatformModule) RegisterRoutes(mux *http.ServeMux) {
	RegisterUserRoutes(mux, NewUserHandler(m.users, m.security.CSRF))
	RegisterGroupRoutes(mux, NewGroupHandler(m.groups, m.security.CSRF))
	RegisterDatabaseConnectionRoutes(mux, NewDatabaseConnectionHandler(m.connections, m.security.CSRF))
	RegisterViewerRoutes(mux, NewViewerHandler(m.viewer))
	RegisterQueryAnalysisRoutes(mux, NewQueryAnalysisHandler(m.analysis, m.security.CSRF))
	mux.HandleFunc("/api/platform/me", m.handleIdentity)
	RegisterDashboardRoutes(mux)
}
func (m *PlatformModule) Close() error {
	if m == nil {
		return nil
	}
	return m.Pools.Close()
}

func (m *PlatformModule) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeUserMethodError(w, "GET")
		return
	}
	if m == nil || m.security.Identity == nil {
		writeUserServiceError(w, ErrUserDependencyUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), viewerOperationTimeout)
	defer cancel()
	id, err := m.security.Identity.VerifiedUserID(ctx)
	if errors.Is(err, ErrUnauthenticated) || (err == nil && id <= 0) {
		writeUserServiceError(w, ErrUnauthenticated)
		return
	}
	if err != nil {
		writeUserServiceError(w, ErrUserDependencyUnavailable)
		return
	}
	user, err := m.users.store.GetByID(ctx, id)
	if errors.Is(err, ErrUserNotFound) || (err == nil && !user.IsActive) {
		writeUserServiceError(w, ErrUnauthenticated)
		return
	}
	if err != nil {
		writeUserServiceError(w, err)
		return
	}
	writeUserJSON(w, 200, user, nil)
}

type VerifiedIdentityFunc func(context.Context) (int, error)

func (f VerifiedIdentityFunc) VerifiedUserID(ctx context.Context) (int, error) {
	if f == nil {
		return 0, ErrUserDependencyUnavailable
	}
	return f(ctx)
}

type PasswordHasherFunc func(context.Context, string) (PasswordHash, error)

func (f PasswordHasherFunc) HashPassword(ctx context.Context, password string) (PasswordHash, error) {
	if f == nil {
		return "", ErrUserDependencyUnavailable
	}
	return f(ctx, password)
}

type SessionRevokerFunc func(context.Context, int) error

func (f SessionRevokerFunc) RevokeUserSessions(ctx context.Context, id int) error {
	if f == nil {
		return ErrUserDependencyUnavailable
	}
	return f(ctx, id)
}

type CSRFVerifierFunc func(*http.Request) error

func (f CSRFVerifierFunc) ValidateCSRF(r *http.Request) error {
	if f == nil {
		return ErrUserDependencyUnavailable
	}
	return f(r)
}
