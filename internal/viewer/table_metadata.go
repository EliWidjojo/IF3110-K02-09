package viewer

import (
	"context"
	"database/sql"
)

// Model metadata tidak bergantung pada bentuk respons HTTP sehingga dapat digunakan pemanggil service lain.
type ViewerTableMetadata struct {
	Table       ViewerTable
	Columns     []ViewerColumnMetadata
	Constraints []ViewerConstraintMetadata
	Indexes     []ViewerIndexMetadata
}

type ViewerColumnMetadata struct {
	ViewerColumn
	Position             int
	Nullable             bool
	Default              *string
	Identity             string
	Generated            string
	GenerationExpression *string
}

type ViewerConstraintMetadata struct {
	Name              string
	Type              string
	Definition        string
	Columns           []string
	Reference         *ViewerForeignKeyReference
	Deferrable        bool
	InitiallyDeferred bool
	Validated         bool
}

type ViewerForeignKeyReference struct {
	Schema  string
	Table   string
	Columns []string
}

type ViewerIndexMetadata struct {
	Name       string
	Definition string
	Method     string
	Unique     bool
	Primary    bool
	Valid      bool
	Ready      bool
	Predicate  *string
}

type ViewerTableMetadataStore interface {
	GetTableMetadata(context.Context, *sql.DB, string, string) (ViewerTableMetadata, error)
}
