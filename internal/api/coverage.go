package api

import (
	"encoding/json"
	"net/http"
)

// handleCoverage serves GET /api/v1/ais/coverage
// Returns available date ranges in the jiskta-core store.
// No authentication required.
func (s *Server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	stats := s.store.Stats()
	w.Header().Set("Content-Type", "application/json")
	result := map[string]interface{}{
		"status": "ok",
		"stats":  stats,
	}
	_ = json.NewEncoder(w).Encode(result)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := map[string]string{"error": msg}
	_ = json.NewEncoder(w).Encode(resp)
}
