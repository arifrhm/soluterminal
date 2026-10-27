package main

import (
	"encoding/json"
	"net/http"
)

// defaultJSONBodyBytes mirrors the global maxRequestBodyMiddleware cap
// installed in main.go so per-handler decoding preserves today's limit.
const defaultJSONBodyBytes int64 = 32 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	// Keep decoding as lenient as the hand-rolled handlers: unknown fields and
	// trailing data are accepted because this intentionally performs one Decode.
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		// Preserve createPane's byte-identical plain-text error. The web client
		// displays this body raw, and MaxBytesError deliberately folds into this
		// same 400 rather than becoming a 413.
		http.Error(w, "invalid json", http.StatusBadRequest)
		return false
	}
	return true
}

// jsonError is an opt-in helper for endpoints whose clients parse JSON error
// bodies. It must not replace plain-text http.Error responses that the web
// client displays raw, which is why createPane does not use it.
func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
