package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/queryanalysis"
	"IF3110-K02-09/internal/viewer"
)

// SecurityDependencies wajib berasal dari modul A yang memakai identitas PostgreSQL yang sama dengan administrasi B.
type SecurityDependencies struct {
	Identity       admin.VerifiedUserProvider
	PasswordHasher admin.UserPasswordHasher
	Sessions       admin.UserSessionRevoker
	CSRF           admin.UserCSRFVerifier
}

type PlatformModule struct {
	Pools          *database.TargetConnectionManager
	Access         *access.EffectiveAccessService
	Authentication *admin.AuthenticationRepository
	users          *admin.UserService
	grants         *access.GrantService
	groups         *admin.GroupService
	connections    *database.DatabaseConnectionService
	viewer         *viewer.ViewerService
	analysis       *queryanalysis.QueryAnalysisService
	migration      *migration.Engine
	security       SecurityDependencies
}

// Komposisi memakai satu repository konfigurasi agar penulisan admin dan pool target berbagi invalidasi yang sama.
func NewPlatformModule(db *sql.DB, cipher *database.DatabasePasswordCipher, security SecurityDependencies) (*PlatformModule, error) {
	if db == nil {
		return nil, viewer.ErrViewerDependencyUnavailable
	}
	repository := database.NewDatabaseConnectionRepository(db, cipher)
	pools, err := database.NewTargetConnectionManager(repository, database.TargetPoolOptions{})
	if err != nil {
		return nil, err
	}
	users := admin.NewUserService(admin.NewUserRepository(db), security.Identity, security.PasswordHasher, security.Sessions)
	effectiveAccess := access.NewEffectiveAccessService(access.NewEffectiveAccessRepository(db), security.Identity)
	return &PlatformModule{
		Pools:          pools,
		Access:         effectiveAccess,
		Authentication: admin.NewAuthenticationRepository(db),
		users:          users,
		grants:         access.NewGrantService(access.NewGrantRepository(db), users),
		groups:         admin.NewGroupService(admin.NewGroupRepository(db), users),
		connections:    database.NewDatabaseConnectionService(repository, pools, users),
		viewer:         viewer.NewViewerService(effectiveAccess, pools, viewer.NewViewerMetadataRepository()),
		analysis:       queryanalysis.NewQueryAnalysisService(effectiveAccess, pools),
		migration:      migration.NewEngine(effectiveAccess, pools, security.Identity),
		security:       security,
	}, nil
}

func (m *PlatformModule) RegisterRoutes(mux *http.ServeMux) {
	admin.RegisterUserRoutes(mux, admin.NewUserHandler(m.users, m.security.CSRF))
	admin.RegisterGroupRoutes(mux, admin.NewGroupHandler(m.groups, m.security.CSRF))
	access.RegisterGrantRoutes(mux, access.NewGrantHandler(m.grants, m.security.CSRF))
	database.RegisterDatabaseConnectionRoutes(mux, database.NewDatabaseConnectionHandler(m.connections, m.security.CSRF))
	viewer.RegisterViewerRoutes(mux, viewer.NewViewerHandler(m.viewer))
	queryanalysis.RegisterQueryAnalysisRoutes(mux, queryanalysis.NewQueryAnalysisHandler(m.analysis, m.security.CSRF))
	migration.RegisterRoutes(mux, migration.NewHandler(m.migration, m.security.CSRF))
	mux.HandleFunc("/api/platform/me", m.handleIdentity)
}
func (m *PlatformModule) Close() error {
	if m == nil {
		return nil
	}
	return m.Pools.Close()
}

func (m *PlatformModule) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteMethodError(w, "GET")
		return
	}
	if m == nil || m.security.Identity == nil {
		admin.WriteUserServiceError(w, admin.ErrUserDependencyUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id, err := m.security.Identity.VerifiedUserID(ctx)
	if errors.Is(err, admin.ErrUnauthenticated) || (err == nil && id <= 0) {
		admin.WriteUserServiceError(w, admin.ErrUnauthenticated)
		return
	}
	if err != nil {
		admin.WriteUserServiceError(w, admin.ErrUserDependencyUnavailable)
		return
	}
	user, err := m.Authentication.CurrentUser(ctx, id)
	if errors.Is(err, admin.ErrUserNotFound) || (err == nil && !user.IsActive) {
		admin.WriteUserServiceError(w, admin.ErrUnauthenticated)
		return
	}
	if err != nil {
		admin.WriteUserServiceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, user, nil)
}

type VerifiedIdentityFunc func(context.Context) (int, error)

func (f VerifiedIdentityFunc) VerifiedUserID(ctx context.Context) (int, error) {
	if f == nil {
		return 0, admin.ErrUserDependencyUnavailable
	}
	return f(ctx)
}

type PasswordHasherFunc func(context.Context, string) (admin.PasswordHash, error)

func (f PasswordHasherFunc) HashPassword(ctx context.Context, password string) (admin.PasswordHash, error) {
	if f == nil {
		return "", admin.ErrUserDependencyUnavailable
	}
	return f(ctx, password)
}

type SessionRevokerFunc func(context.Context, int) error

func (f SessionRevokerFunc) RevokeUserSessions(ctx context.Context, id int) error {
	if f == nil {
		return admin.ErrUserDependencyUnavailable
	}
	return f(ctx, id)
}

type CSRFVerifierFunc func(*http.Request) error

func (f CSRFVerifierFunc) ValidateCSRF(r *http.Request) error {
	if f == nil {
		return admin.ErrUserDependencyUnavailable
	}
	return f(r)
}
