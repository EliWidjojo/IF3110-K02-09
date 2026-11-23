package viewer

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
)

// Route, handler daftar database, schema, tabel, dan pemetaan error.

// Seluruh endpoint viewer hanya membaca metadata sehingga tidak memerlukan perubahan state maupun token CSRF.
type ViewerHandler struct {
	service *ViewerService
}

func NewViewerHandler(service *ViewerService) *ViewerHandler {
	return &ViewerHandler{service: service}
}

func RegisterViewerRoutes(mux *http.ServeMux, handler *ViewerHandler) {
	mux.HandleFunc("/api/viewer/databases", handler.HandleDatabases)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas", handler.HandleSchemas)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables", handler.HandleTables)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables/{table}", handler.HandleTableMetadata)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables/{table}/data", handler.HandleTableData)
}

func (h *ViewerHandler) HandleDatabases(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databases, err := h.service.ListDatabases(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, databases, nil)
}

func (h *ViewerHandler) HandleSchemas(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	schemas, err := h.service.ListSchemas(r.Context(), databaseID)
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, schemas, nil)
}

func (h *ViewerHandler) HandleTables(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	// PathValue sudah di-decode oleh router dan nama schema diteruskan sebagai nilai parameter, bukan identifier SQL.
	tables, err := h.service.ListTables(r.Context(), databaseID, r.PathValue("schema"))
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, tables, nil)
}

func (h *ViewerHandler) prepareRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		httpapi.WriteMethodError(w, "GET")
		return false
	}
	if h == nil || h.service == nil {
		WriteError(w, ErrViewerDependencyUnavailable)
		return false
	}
	return true
}

func DatabaseID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || id <= 0 {
		return 0, ErrInvalidViewerInput
	}
	return id, nil
}

// Respons memakai pesan tetap agar error driver, detail koneksi, dan query tidak diteruskan kepada client.
func WriteError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &httpapi.Error{Code: "internal_error", Message: "operasi viewer gagal"}
	switch {
	case errors.Is(err, database.ErrInternalDatabaseTarget):
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "internal_database_target", Message: database.ErrInternalDatabaseTarget.Error()})
		return
	case errors.Is(err, ErrViewerResultTooLarge):
		httpapi.WriteJSON(w, 413, nil, &httpapi.Error{Code: "result_too_large", Message: ErrViewerResultTooLarge.Error()})
		return
	case errors.Is(err, admin.ErrUnauthenticated), errors.Is(err, admin.ErrUserNotFound):
		status, apiError = http.StatusUnauthorized, &httpapi.Error{Code: "unauthenticated", Message: "authentication diperlukan"}
	case errors.Is(err, access.ErrDatabaseAccessDenied), errors.Is(err, access.ErrInactiveUser):
		status, apiError = http.StatusForbidden, &httpapi.Error{Code: "database_access_denied", Message: "akses database ditolak"}
	case errors.Is(err, ErrInvalidViewerInput), errors.Is(err, access.ErrInvalidAccessInput), errors.Is(err, database.ErrInvalidDatabaseConnection):
		status, apiError = http.StatusBadRequest, &httpapi.Error{Code: "invalid_input", Message: "ID database, nama schema, nama tabel, atau pagination tidak valid"}
	case errors.Is(err, ErrViewerTableNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "table_not_found", Message: "tabel tidak ditemukan atau tidak ditampilkan"}
	case errors.Is(err, ErrViewerSchemaNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "schema_not_found", Message: "schema tidak ditemukan atau tidak ditampilkan"}
	case errors.Is(err, database.ErrDatabaseConnectionNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "database_connection_not_found", Message: "konfigurasi database tidak ditemukan"}
	case errors.Is(err, ErrViewerDependencyUnavailable), errors.Is(err, access.ErrAccessDependencyUnavailable),
		errors.Is(err, database.ErrTargetManagerUnavailable), errors.Is(err, database.ErrTargetManagerClosed), errors.Is(err, database.ErrTargetDriverDebug),
		errors.Is(err, database.ErrDatabaseEncryptionKey), errors.Is(err, database.ErrDatabaseDecryption):
		status, apiError = http.StatusServiceUnavailable, &httpapi.Error{Code: "dependency_unavailable", Message: "komponen viewer atau koneksi belum dapat digunakan"}
	case errors.Is(err, context.DeadlineExceeded):
		status, apiError = http.StatusGatewayTimeout, &httpapi.Error{Code: "viewer_timeout", Message: "batas waktu pembacaan database terlampaui"}
	case errors.Is(err, context.Canceled):
		status, apiError = http.StatusRequestTimeout, &httpapi.Error{Code: "request_canceled", Message: "request dibatalkan"}
	case errors.Is(err, ErrViewerMetadata):
		status, apiError = http.StatusBadGateway, &httpapi.Error{Code: "metadata_query_failed", Message: "metadata database target tidak dapat dibaca"}
	case errors.Is(err, database.ErrTargetConnectionFailed):
		status, apiError = http.StatusBadGateway, &httpapi.Error{Code: "target_connection_failed", Message: "koneksi ke database target gagal"}
	case errors.Is(err, ErrViewerTableData):
		apiError = &httpapi.Error{Code: "table_data_failed", Message: "data tabel tidak dapat disajikan"}
	}
	httpapi.WriteJSON(w, status, nil, apiError)
}

// Handler dan representasi respons metadata tabel.

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
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	metadata, err := h.service.GetTableMetadata(r.Context(), databaseID, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, newViewerTableMetadataResponse(metadata), nil)
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

// Handler, pagination, dan serialisasi respons data tabel.

type viewerTableDataResponse struct {
	Columns     []viewerDataColumnResponse `json:"columns"`
	Rows        [][]any                    `json:"rows"`
	Pagination  viewerPaginationResponse   `json:"pagination"`
	OrderBy     []string                   `json:"order_by"`
	StableOrder bool                       `json:"stable_order"`
}

type viewerDataColumnResponse struct {
	Name      string `json:"name"`
	DataType  string `json:"data_type"`
	ValueType string `json:"value_type"`
}

type viewerPaginationResponse struct {
	Limit        int  `json:"limit"`
	Offset       int  `json:"offset"`
	ReturnedRows int  `json:"returned_rows"`
	HasMore      bool `json:"has_more"`
}

// Nilai khusus menggunakan value string agar JSON client tidak mengubah presisi atau menebak encoding biner.
type viewerTypedValueResponse struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding,omitempty"`
	Value    string `json:"value"`
}

func (h *ViewerHandler) HandleTableData(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	schema, table := r.PathValue("schema"), r.PathValue("table")
	if !ValidIdentifier(schema) || !ValidIdentifier(table) {
		WriteError(w, ErrInvalidViewerInput)
		return
	}
	pagination, err := viewerRequestPagination(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	// Identitas dan izin dibaca ulang oleh service tanpa mengambil klaim user atau akses dari request client.
	data, err := h.service.ReadTableData(r.Context(), databaseID, schema, table, pagination)
	if err != nil {
		WriteError(w, err)
		return
	}
	response, err := newViewerTableDataResponse(data)
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, response, nil)
}

func viewerRequestPagination(r *http.Request) (ViewerPagination, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ViewerPagination{}, ErrInvalidViewerInput
	}
	pagination := ViewerPagination{Limit: ViewerDefaultPageSize}
	for name, values := range query {
		if (name != "limit" && name != "offset") || len(values) != 1 {
			return ViewerPagination{}, ErrInvalidViewerInput
		}
		value, err := strconv.Atoi(values[0])
		if err != nil {
			return ViewerPagination{}, ErrInvalidViewerInput
		}
		if name == "limit" {
			pagination.Limit = value
		} else {
			pagination.Offset = value
		}
	}
	return normalizedViewerPagination(pagination)
}

func newViewerTableDataResponse(data ViewerTableData) (viewerTableDataResponse, error) {
	response := viewerTableDataResponse{
		Columns: make([]viewerDataColumnResponse, 0, len(data.Columns)),
		Rows:    make([][]any, 0, len(data.Rows)), OrderBy: append([]string{}, data.OrderBy...),
		StableOrder: data.StableOrder,
		Pagination: viewerPaginationResponse{
			Limit: data.Limit, Offset: data.Offset, ReturnedRows: len(data.Rows), HasMore: data.HasMore,
		},
	}
	for _, column := range data.Columns {
		response.Columns = append(response.Columns, viewerDataColumnResponse{
			Name: column.Name, DataType: column.DataType, ValueType: column.ValueType,
		})
	}
	for _, row := range data.Rows {
		if len(row) != len(data.Columns) {
			return viewerTableDataResponse{}, ErrViewerTableData
		}
		values := make([]any, len(row))
		for index, value := range row {
			var err error
			values[index], err = viewerResponseValue(value, data.Columns[index].ValueType)
			if err != nil {
				return viewerTableDataResponse{}, err
			}
		}
		response.Rows = append(response.Rows, values)
	}
	return response, nil
}

func viewerResponseValue(value any, valueType string) (any, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		if !utf8.ValidString(text) {
			return nil, ErrViewerTableData
		}
		switch valueType {
		case "BIGINT", "INT", "SMALLINT":
			return viewerTypedValueResponse{Type: "integer", Value: text}, nil
		case "NUMERIC":
			return viewerTypedValueResponse{Type: "numeric", Value: text}, nil
		case "JSON", "JSONB":
			// JSON tetap teks agar angka besar, duplikasi key pada json, dan JSON null tidak berubah saat decoding respons.
			return viewerTypedValueResponse{Type: strings.ToLower(valueType), Encoding: "text", Value: text}, nil
		case "DATE", "TIME", "TIMETZ", "TIMESTAMP", "TIMESTAMPTZ", "INTERVAL":
			return viewerTypedValueResponse{Type: "temporal", Value: text}, nil
		case "FLOAT4", "FLOAT8":
			return viewerTypedValueResponse{Type: "float", Value: text}, nil
		default:
			return text, nil
		}
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int64:
		return viewerTypedValueResponse{Type: "integer", Value: strconv.FormatInt(typed, 10)}, nil
	case float64:
		return viewerTypedValueResponse{Type: "float", Value: strconv.FormatFloat(typed, 'g', -1, 64)}, nil
	case []byte:
		// Encoding dipilih eksplisit sehingga []byte tidak pernah diserahkan langsung kepada encoder JSON.
		return viewerTypedValueResponse{Type: "binary", Encoding: "base64", Value: base64.StdEncoding.EncodeToString(typed)}, nil
	default:
		return nil, ErrViewerTableData
	}
}
