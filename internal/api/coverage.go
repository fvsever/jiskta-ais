package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleCoverage serves GET /api/v1/ais/coverage
// Returns available date ranges derived from the jiskta-core segment manifest.
// No authentication required.
func (s *Server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	segs := s.store.Coverage()

	type dateRange struct {
		Path      string `json:"path"`
		DateFrom  string `json:"date_from"`
		DateTo    string `json:"date_to"`
		DatasetID uint32 `json:"dataset_id"`
		IsDelta   bool   `json:"is_delta"`
	}
	ranges := make([]dateRange, 0, len(segs))
	for _, seg := range segs {
		from := time.UnixMilli(seg.TMinMs).UTC().Format("2006-01-02")
		to := time.UnixMilli(seg.TMaxMs).UTC().Format("2006-01-02")
		ranges = append(ranges, dateRange{
			Path:      seg.Path,
			DateFrom:  from,
			DateTo:    to,
			DatasetID: seg.DatasetID,
			IsDelta:   seg.IsDelta,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"segments": ranges,
	})
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := map[string]string{"error": msg}
	_ = json.NewEncoder(w).Encode(resp)
}
