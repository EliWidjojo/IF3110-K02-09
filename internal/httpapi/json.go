// Package httpapi menyediakan format respons dan decoding request tanpa aturan bisnis fitur.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
)

const MaxRequestBytes = 16 * 1024

func DecodeJSON(w http.ResponseWriter, r *http.Request, input any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteJSON(w, http.StatusUnsupportedMediaType, nil, &Error{
			Code: "unsupported_media_type", Message: "Content-Type harus application/json",
		})
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		WriteBodyError(w, err)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		WriteBodyError(w, err)
		return false
	}
	return true
}

func PaginationValue(r *http.Request, name string, fallback int) (int, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(values[0])
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(w http.ResponseWriter, status int, data any, apiError *Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Data  any    `json:"data,omitempty"`
		Error *Error `json:"error,omitempty"`
	}{Data: data, Error: apiError})
}

func WriteMethodError(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	WriteJSON(w, http.StatusMethodNotAllowed, nil, &Error{
		Code: "method_not_allowed", Message: "metode HTTP tidak diizinkan",
	})
}

func WriteBodyError(w http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		WriteJSON(w, http.StatusRequestEntityTooLarge, nil, &Error{
			Code: "request_too_large", Message: "ukuran body maksimum 16 KiB",
		})
		return
	}
	WriteJSON(w, http.StatusBadRequest, nil, &Error{
		Code: "invalid_request", Message: "body request tidak valid",
	})
}
