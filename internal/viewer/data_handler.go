package viewer

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"IF3110-K02-09/internal/httpapi"
)

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
