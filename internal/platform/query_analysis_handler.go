package platform

import (
	"errors"
	"net/http"
	"strings"
)

type QueryAnalysisHandler struct {
	service *QueryAnalysisService
	csrf    UserCSRFVerifier
}

func NewQueryAnalysisHandler(service *QueryAnalysisService, csrf UserCSRFVerifier) *QueryAnalysisHandler {
	return &QueryAnalysisHandler{service, csrf}
}
func RegisterQueryAnalysisRoutes(mux *http.ServeMux, h *QueryAnalysisHandler) {
	mux.HandleFunc("/api/viewer/databases/{database_id}/explain", h.HandleExplain)
}
func (h *QueryAnalysisHandler) HandleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeUserMethodError(w, "POST")
		return
	}
	if h == nil || h.service == nil || h.csrf == nil {
		writeViewerError(w, ErrViewerDependencyUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUserRequestBytes)
	if r.ContentLength > maxUserRequestBytes {
		writeUserBodyError(w, &http.MaxBytesError{Limit: maxUserRequestBytes})
		return
	}
	if err := h.csrf.ValidateCSRF(r); err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeUserBodyError(w, err)
		} else if errors.Is(err, ErrInvalidUserCSRF) {
			writeUserServiceError(w, err)
		} else {
			writeViewerError(w, ErrViewerDependencyUnavailable)
		}
		return
	}
	id, err := viewerDatabaseID(r)
	if err != nil {
		writeViewerError(w, err)
		return
	}
	var input QueryAnalysisInput
	if !decodeUserJSON(w, r, &input) {
		return
	}
	result, err := h.service.Analyze(r.Context(), id, input)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidAnalysis):
			writeUserJSON(w, 400, nil, &userAPIError{Code: "invalid_analysis", Message: ErrInvalidAnalysis.Error()})
		case errors.Is(err, ErrUnsupportedAnalysis):
			writeUserJSON(w, 422, nil, &userAPIError{Code: "unsupported_analysis", Message: ErrUnsupportedAnalysis.Error()})
		case errors.Is(err, ErrUnsafeAnalyze):
			writeUserJSON(w, 422, nil, &userAPIError{Code: "unsafe_analyze", Message: ErrUnsafeAnalyze.Error()})
		case errors.Is(err, ErrAnalysisQuery):
			var target *TargetConnectionError
			if errors.As(err, &target) && target.SQLState != "" && !strings.HasPrefix(target.SQLState, "08") && !strings.HasPrefix(target.SQLState, "28") {
				writeUserJSON(w, 422, nil, &userAPIError{Code: "analysis_failed", Message: ErrAnalysisQuery.Error()})
			} else {
				writeViewerError(w, err)
			}
		default:
			writeViewerError(w, err)
		}
		return
	}
	writeUserJSON(w, 200, result, nil)
}
