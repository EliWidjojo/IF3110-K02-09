package platform

import "net/http"

type viewerTableMetadataResponse struct {
	Schema      string                             `json:"schema"`
	Name        string                             `json:"name"`
	Type        string                             `json:"type"`
	Columns     []viewerColumnMetadataResponse     `json:"columns"`
	Constraints []viewerConstraintMetadataResponse `json:"constraints"`
	Indexes     []viewerIndexMetadataResponse      `json:"indexes"`
}

type viewerColumnMetadataResponse struct {
	Name                 string  `json:"name"`
	OrdinalPosition      int     `json:"ordinal_position"`
	DataType             string  `json:"data_type"`
	Nullable             bool    `json:"nullable"`
	Default              *string `json:"default"`
	Identity             string  `json:"identity"`
	Generated            string  `json:"generated"`
	GenerationExpression *string `json:"generation_expression"`
}

type viewerConstraintMetadataResponse struct {
	Name              string                             `json:"name"`
	Type              string                             `json:"type"`
	Definition        string                             `json:"definition"`
	Columns           []string                           `json:"columns"`
	Reference         *viewerForeignKeyReferenceResponse `json:"reference"`
	Deferrable        bool                               `json:"deferrable"`
	InitiallyDeferred bool                               `json:"initially_deferred"`
	Validated         bool                               `json:"validated"`
}

type viewerForeignKeyReferenceResponse struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

type viewerIndexMetadataResponse struct {
	Name       string  `json:"name"`
	Definition string  `json:"definition"`
	Method     string  `json:"method"`
	Unique     bool    `json:"unique"`
	Primary    bool    `json:"primary"`
	Valid      bool    `json:"valid"`
	Ready      bool    `json:"ready"`
	Predicate  *string `json:"predicate"`
}

func (h *ViewerHandler) HandleTableMetadata(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := viewerDatabaseID(r)
	if err != nil {
		writeViewerError(w, err)
		return
	}
	metadata, err := h.service.GetTableMetadata(r.Context(), databaseID, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		writeViewerError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, newViewerTableMetadataResponse(metadata), nil)
}

// Pemetaan hanya terjadi di lapisan HTTP agar nama properti JSON tidak menjadi kontrak model metadata internal.
func newViewerTableMetadataResponse(metadata ViewerTableMetadata) viewerTableMetadataResponse {
	response := viewerTableMetadataResponse{
		Schema: metadata.Table.Schema, Name: metadata.Table.Name, Type: metadata.Table.Type,
		Columns:     make([]viewerColumnMetadataResponse, 0, len(metadata.Columns)),
		Constraints: make([]viewerConstraintMetadataResponse, 0, len(metadata.Constraints)),
		Indexes:     make([]viewerIndexMetadataResponse, 0, len(metadata.Indexes)),
	}
	for _, column := range metadata.Columns {
		response.Columns = append(response.Columns, viewerColumnMetadataResponse{
			Name: column.Name, OrdinalPosition: column.Position, DataType: column.DataType,
			Nullable: column.Nullable, Default: column.Default, Identity: column.Identity,
			Generated: column.Generated, GenerationExpression: column.GenerationExpression,
		})
	}
	for _, constraint := range metadata.Constraints {
		item := viewerConstraintMetadataResponse{
			Name: constraint.Name, Type: constraint.Type, Definition: constraint.Definition,
			Columns: append([]string{}, constraint.Columns...), Deferrable: constraint.Deferrable,
			InitiallyDeferred: constraint.InitiallyDeferred, Validated: constraint.Validated,
		}
		if constraint.Reference != nil {
			item.Reference = &viewerForeignKeyReferenceResponse{
				Schema: constraint.Reference.Schema, Table: constraint.Reference.Table,
				Columns: append([]string{}, constraint.Reference.Columns...),
			}
		}
		response.Constraints = append(response.Constraints, item)
	}
	for _, index := range metadata.Indexes {
		response.Indexes = append(response.Indexes, viewerIndexMetadataResponse{
			Name: index.Name, Definition: index.Definition, Method: index.Method,
			Unique: index.Unique, Primary: index.Primary, Valid: index.Valid, Ready: index.Ready,
			Predicate: index.Predicate,
		})
	}
	return response
}
