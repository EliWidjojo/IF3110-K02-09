package access

import "context"

type Level string

const (
	None Level = ""
	View Level = "View Only"
	Edit Level = "View + Edit"
)

type Action string

const (
	ViewSchema     Action = "schema.read"
	ViewData       Action = "data.read"
	ViewHistory    Action = "migration.read"
	WriteMigration Action = "migration.write"
	ManagePlatform Action = "platform.manage"
)

type Principal struct {
	ID        int              `json:"id"`
	Username  string           `json:"username"`
	Admin     bool             `json:"platform_admin"`
	Databases map[string]Level `json:"database_access"`
}

// Platform admin is deliberately not a database permission.
func Allowed(p Principal, database string, action Action) bool {
	if p.ID <= 0 {
		return false
	}
	if action == ManagePlatform {
		return p.Admin
	}
	level := p.Databases[database]
	switch action {
	case ViewSchema, ViewData, ViewHistory:
		return level == View || level == Edit
	case WriteMigration:
		return level == Edit
	default:
		return false
	}
}

// Group grants combine by choosing the highest level.
func Merge(a, b Level) Level {
	if a == Edit || b == Edit {
		return Edit
	}
	if a == View || b == View {
		return View
	}
	return None
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok && p.ID > 0
}
