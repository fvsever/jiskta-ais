package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleVessel serves GET /api/v1/ais/vessel/{mmsi}
// Returns the full position track for a single vessel in the requested time window.
// Required params: mmsi (in path), time_start, time_end (Unix ms)
// Credit cost: 1 credit per track query.
func (s *Server) handleVessel(w http.ResponseWriter, r *http.Request) {
	mmsiStr := mmsiFromPath(r.URL.Path)
	mmsiVal, err := strconv.ParseUint(mmsiStr, 10, 32)
	if err != nil || mmsiVal == 0 {
		jsonError(w, "invalid MMSI in path", http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	tsStart, err1 := strconv.ParseInt(q.Get("time_start"), 10, 64)
	tsEnd, err2 := strconv.ParseInt(q.Get("time_end"), 10, 64)
	if err1 != nil || err2 != nil {
		jsonError(w, "time_start and time_end must be Unix ms integers", http.StatusBadRequest)
		return
	}

	records, err := s.store.QueryMMSI(uint32(mmsiVal), tsStart, tsEnd, 100_000)
	if err != nil {
		jsonError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Deduct 1 credit per vessel track query (async, non-blocking).
	const vesselTrackCost = int64(1)
	ki := keyInfoFromCtx(r)
	rawKey := r.Header.Get("X-API-Key")
	if rawKey == "" {
		rawKey = r.URL.Query().Get("api_key")
	}
	s.auth.UpdateCachedBalance(rawKey, vesselTrackCost)
	s.auth.DeductCredits(ki.APIKEYID, vesselTrackCost, rawKey)

	w.Header().Set("Content-Type", "application/json")
	result := map[string]interface{}{
		"status":            "success",
		"mmsi":              mmsiVal,
		"count":             len(records),
		"credits_used":      vesselTrackCost,
		"credits_remaining": ki.CreditBalance - vesselTrackCost,
		"records":           records,
	}
	_ = json.NewEncoder(w).Encode(result)
}
