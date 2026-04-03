package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleLive serves GET /api/v1/ais/live as a Server-Sent Events stream.
// Required params: lat_min, lat_max, lon_min, lon_max
// Optional: interval_ms (poll interval, default 1000, min 500, max 10000)
//
// The server polls the store for new records every interval_ms and pushes
// them as SSE events. The client should read the stream until disconnect.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	// Check SSE support.
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	latMin, err1 := strconv.ParseFloat(q.Get("lat_min"), 32)
	latMax, err2 := strconv.ParseFloat(q.Get("lat_max"), 32)
	lonMin, err3 := strconv.ParseFloat(q.Get("lon_min"), 32)
	lonMax, err4 := strconv.ParseFloat(q.Get("lon_max"), 32)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		jsonError(w, "lat_min, lat_max, lon_min, lon_max must be numeric", http.StatusBadRequest)
		return
	}

	intervalMs := 1000
	if s := q.Get("interval_ms"); s != "" {
		v, err := strconv.Atoi(s)
		if err == nil && v >= 500 && v <= 10_000 {
			intervalMs = v
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	// Track the last timestamp we sent to avoid re-sending records.
	lastTs := time.Now().UnixMilli() - int64(intervalMs)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			nowMs := now.UnixMilli()
			records, err := s.store.QueryBbox(
				float32(latMin), float32(latMax),
				float32(lonMin), float32(lonMax),
				lastTs, nowMs,
				0, // mmsi=0 = wildcard
				500,
			)
			if err == nil && len(records) > 0 {
				data, _ := json.Marshal(map[string]interface{}{
					"ts":      nowMs,
					"count":   len(records),
					"records": records,
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
			lastTs = nowMs
		}
	}
}
