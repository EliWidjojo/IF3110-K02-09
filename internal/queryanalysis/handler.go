package queryanalysis

import (
	"errors"
	"net/http"
	"strings"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/viewer"
)

type QueryAnalysisHandler struct {
	service *QueryAnalysisService
	csrf    admin.UserCSRFVerifier
}

func NewQueryAnalysisHandler(service *QueryAnalysisService, csrf admin.UserCSRFVerifier) *QueryAnalysisHandler {
	return &QueryAnalysisHandler{service, csrf}
}
func RegisterQueryAnalysisRoutes(mux *http.ServeMux, h *QueryAnalysisHandler) {
	mux.HandleFunc("/api/viewer/databases/{database_id}/explain", h.HandleExplain)
}
func (h *QueryAnalysisHandler) HandleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteMethodError(w, "POST")
		return
	}
	if h == nil || h.service == nil || h.csrf == nil {
		viewer.WriteError(w, viewer.ErrViewerDependencyUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if r.ContentLength > httpapi.MaxRequestBytes {
		httpapi.WriteBodyError(w, &http.MaxBytesError{Limit: httpapi.MaxRequestBytes})
		return
	}
	if err := h.csrf.ValidateCSRF(r); err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			httpapi.WriteBodyError(w, err)
		} else if errors.Is(err, admin.ErrInvalidUserCSRF) {
			admin.WriteUserServiceError(w, err)
		} else {
			viewer.WriteError(w, viewer.ErrViewerDependencyUnavailable)
		}
		return
	}
	id, err := viewer.DatabaseID(r)
	if err != nil {
		viewer.WriteError(w, err)
		return
	}
	var input QueryAnalysisInput
	if !httpapi.DecodeJSON(w, r, &input) {
		return
	}
	result, err := h.service.Analyze(r.Context(), id, input)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidAnalysis):
			httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_analysis", Message: ErrInvalidAnalysis.Error()})
		case errors.Is(err, ErrUnsupportedAnalysis):
			httpapi.WriteJSON(w, 422, nil, &httpapi.Error{Code: "unsupported_analysis", Message: ErrUnsupportedAnalysis.Error()})
		case errors.Is(err, ErrUnsafeAnalyze):
			httpapi.WriteJSON(w, 422, nil, &httpapi.Error{Code: "unsafe_analyze", Message: ErrUnsafeAnalyze.Error()})
		case errors.Is(err, ErrAnalysisQuery):
			var target *database.TargetConnectionError
			if errors.As(err, &target) && target.SQLState != "" && !strings.HasPrefix(target.SQLState, "08") && !strings.HasPrefix(target.SQLState, "28") {
				httpapi.WriteJSON(w, 422, nil, &httpapi.Error{Code: "analysis_failed", Message: ErrAnalysisQuery.Error()})
			} else {
				viewer.WriteError(w, err)
			}
		default:
			viewer.WriteError(w, err)
		}
		return
	}
	httpapi.WriteJSON(w, 200, result, nil)
}
