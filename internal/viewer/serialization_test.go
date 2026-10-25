package viewer

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestTypedSerializationPreservesMeaning(t *testing.T) {
	for _, tc := range []struct {
		value     any
		typ, want string
	}{{int64(9007199254740993), "INT8", `"value":"9007199254740993"`}, {[]byte{255, 0}, "BYTEA", `"value":"/wA="`}, {"12345678901234567890.123", "NUMERIC", `"value":"12345678901234567890.123"`}, {`{"n":9007199254740993,"n":null}`, "JSON", `9007199254740993`}, {"0000-00-00", "TIMESTAMP", `0000-00-00`}, {nil, "TEXT", `null`}} {
		value, err := viewerResponseValue(tc.value, tc.typ)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil || !strings.Contains(string(raw), tc.want) {
			t.Fatal("nilai berubah", tc.typ, string(raw), err)
		}
	}
	if _, err := viewerResponseValue(string([]byte{255}), "TEXT"); err == nil {
		t.Fatal("UTF8 invalid diterima")
	}
	value, err := viewerJSONValue(math.Inf(1), "FLOAT8")
	if err != nil || value != "+Inf" {
		t.Fatal("float nonfinite hilang")
	}
	date := time.Date(2026, 10, 25, 10, 30, 0, 0, time.FixedZone("WIB", 7*3600))
	if viewerTemporalValue(date, "TIMESTAMPTZ") != "2026-10-25T03:30:00Z" || viewerTemporalValue(date, "TIMESTAMP") != "2026-10-25T10:30:00" {
		t.Fatal("semantik zona waktu berubah")
	}
}
