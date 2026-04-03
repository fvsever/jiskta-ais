package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Credit model:
//   - Bbox query: 1 credit per 1,000 records returned (minimum 1).
//   - Dry run (dry_run=true): 0 credits, returns cost estimate only.
const creditsPerThousandRecords = int64(1)

// handleQuery serves GET /api/v1/ais/query
// Required params: lat_min, lat_max, lon_min, lon_max, time_start, time_end
// Optional: mmsi (filter by vessel), limit (default 10000, max 100000), dry_run=true
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	latMin, err1 := strconv.ParseFloat(q.Get("lat_min"), 32)
	latMax, err2 := strconv.ParseFloat(q.Get("lat_max"), 32)
	lonMin, err3 := strconv.ParseFloat(q.Get("lon_min"), 32)
	lonMax, err4 := strconv.ParseFloat(q.Get("lon_max"), 32)
	tsStart, err5 := strconv.ParseInt(q.Get("time_start"), 10, 64)
	tsEnd, err6 := strconv.ParseInt(q.Get("time_end"), 10, 64)

	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		jsonError(w, "lat_min, lat_max, lon_min, lon_max must be numeric", http.StatusBadRequest)
		return
	}
	if err5 != nil || err6 != nil {
		jsonError(w, "time_start and time_end must be Unix ms integers", http.StatusBadRequest)
		return
	}
	if latMin > latMax || lonMin > lonMax || tsStart > tsEnd {
		jsonError(w, "invalid range: min must be <= max", http.StatusBadRequest)
		return
	}

	mmsi := uint32(0)
	if s := q.Get("mmsi"); s != "" {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			jsonError(w, "mmsi must be a positive integer", http.StatusBadRequest)
			return
		}
		mmsi = uint32(v)
	}

	limit := 10_000
	if s := q.Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err == nil && v > 0 && v <= 100_000 {
			limit = v
		}
	}

	dryRun := q.Get("dry_run") == "true"

	// Estimate credit cost before running (dry_run skips the actual query).
	// We don't know record count before the query, so for dry_run we return
	// the minimum cost (1 credit). Actual cost is deducted after the query.
	if dryRun {
		w.Header().Set("Content-Type", "application/json")
		ki := keyInfoFromCtx(r)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":            "dry_run",
			"credits_cost_min":  1,
			"credits_remaining": ki.CreditBalance,
			"note":              "actual cost = ceil(records / 1000), minimum 1",
		})
		return
	}

	records, err := s.store.QueryBbox(
		float32(latMin), float32(latMax),
		float32(lonMin), float32(lonMax),
		tsStart, tsEnd,
		mmsi, limit,
	)
	if err != nil {
		jsonError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Compute and deduct credits (async, non-blocking — same pattern as climate API).
	ki := keyInfoFromCtx(r)
	rawKey := r.Header.Get("X-API-Key")
	if rawKey == "" {
		rawKey = r.URL.Query().Get("api_key")
	}
	cost := creditCost(int64(len(records)))
	s.auth.UpdateCachedBalance(rawKey, cost)
	s.auth.DeductCredits(ki.APIKEYID, cost, rawKey)

	w.Header().Set("Content-Type", "application/json")
	result := map[string]interface{}{
		"status":            "success",
		"count":             len(records),
		"credits_used":      cost,
		"credits_remaining": ki.CreditBalance - cost,
		"records":           records,
	}
	_ = json.NewEncoder(w).Encode(result)
}

// creditCost computes credit cost from a record count.
// Cost = ceil(n / 1000), minimum 1.
func creditCost(n int64) int64 {
	if n <= 0 {
		return 1
	}
	cost := (n + 999) / 1000 * creditsPerThousandRecords
	if cost < 1 {
		cost = 1
	}
	return cost
}
