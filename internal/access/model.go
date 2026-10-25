package access

// AccessLevel menyatakan izin pada database tertentu dan terpisah dari peran Platform Admin.
type AccessLevel = Level

const (
	AccessViewOnly AccessLevel = View
	AccessViewEdit AccessLevel = Edit
)

// GroupDatabaseAccess mengikat izin ke pasangan group dan database, bukan langsung ke user.
type GroupDatabaseAccess struct {
	GroupID     int         `json:"group_id"`
	DatabaseID  int         `json:"database_id"`
	AccessLevel AccessLevel `json:"access_level"`
}
