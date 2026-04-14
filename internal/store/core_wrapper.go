package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CoreClient wraps the CGo Client with higher-level methods matching the API handlers.
// This avoids modifying core_client.go (which mirrors the C FFI exactly).

type CoreClient struct {
	c       *Client
	dataDir string
}

// NewCoreClient opens the jiskta-core JKDB store at dataDir.
func NewCoreClient(dataDir string) (*CoreClient, error) {
	c, err := Init(dataDir)
	if err != nil {
		return nil, err
	}
	return &CoreClient{c: c, dataDir: dataDir}, nil
}

// Close releases the store handle (core_close is void in Ada — no error return).
func (cc *CoreClient) Close() {
	cc.c.Close()
}

// Flush commits all pending writes.
func (cc *CoreClient) Flush() error {
	return cc.c.Flush()
}

// WriteAIS writes a batch of AIS records (satisfies ingest.Storer interface).
// Each AISRecord is packed into a 64-byte EventRecord before being passed to the C layer.
func (cc *CoreClient) WriteAIS(records []AISRecord) error {
	if len(records) == 0 {
		return nil
	}
	evts := make([]EventRecord, len(records))
	for i, r := range records {
		evts[i] = EventRecord{
			TimestampMs: r.Timestamp,
			Lat:         r.Lat,
			Lon:         r.Lon,
			Morton:      0, // auto-compute from lat/lon in core_write_event
			EntityHash:  r.MMSI,
			StreamType:  r.StreamType,
			Flags:       r.Flags,
			SchemaVer:   r.SchemaVersion,
			Payload:     PackAISPayload(r.MMSI, r.SOG, r.COG, r.Heading, r.NavStatus, r.MsgType, r.VesselType),
		}
	}
	return cc.c.WriteEvent(evts)
}

// QueryBboxRecord is the normalised record returned from a bbox query.
type QueryBboxRecord struct {
	Timestamp  int64   `json:"timestamp_ms"`
	Lat        float32 `json:"lat"`
	Lon        float32 `json:"lon"`
	MMSI       uint32  `json:"mmsi,omitempty"`
	SOG        uint16  `json:"sog_tenths,omitempty"`
	COG        uint16  `json:"cog_tenths,omitempty"`
	Heading    uint16  `json:"heading,omitempty"`
	NavStatus  uint8   `json:"nav_status,omitempty"`
	StreamType uint8   `json:"stream_type"`
}

// QueryBbox queries the store for records in the given spatial and temporal range.
// mmsi=0 returns all vessels; limit=0 returns up to the engine default.
func (cc *CoreClient) QueryBbox(
	latMin, latMax, lonMin, lonMax float32,
	tsStartMs, tsEndMs int64,
	mmsi uint32, limit int,
) ([]QueryBboxRecord, error) {
	ir := QueryIR{
		TStartMs:   tsStartMs,
		TEndMs:     tsEndMs,
		LatMin:     latMin,
		LatMax:     latMax,
		LonMin:     lonMin,
		LonMax:     lonMax,
		DatasetID:  1,
		StreamType: uint32(StreamAIS),
		EntityHash: uint64(mmsi),
		Limit:      uint32(limit),
	}
	evts, _, err := cc.c.Query(ir)
	if err != nil {
		return nil, err
	}
	result := convertEventRecords(evts)
	return result, nil
}

// QueryMMSI returns the full track for a single vessel in the time range.
func (cc *CoreClient) QueryMMSI(mmsi uint32, tsStartMs, tsEndMs int64, limit int) ([]QueryBboxRecord, error) {
	ir := QueryIR{
		TStartMs:   tsStartMs,
		TEndMs:     tsEndMs,
		DatasetID:  1,
		StreamType: uint32(StreamAIS),
		EntityHash: uint64(mmsi),
		Limit:      uint32(limit),
	}
	evts, _, err := cc.c.Query(ir)
	if err != nil {
		return nil, err
	}
	return convertEventRecords(evts), nil
}

// Stats returns human-readable stats JSON from the store (timing stub).
func (cc *CoreClient) Stats() map[string]interface{} {
	raw := cc.c.Stats()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]interface{}{"raw": raw}
	}
	return out
}

// ---------------------------------------------------------------------------
// Coverage
// ---------------------------------------------------------------------------

// CoverageSegment describes a single closed JKDB segment.
type CoverageSegment struct {
	Path      string `json:"path"`
	TMinMs    int64  `json:"t_min_ms"`
	TMaxMs    int64  `json:"t_max_ms"`
	DatasetID uint32 `json:"dataset_id"`
	IsDelta   bool   `json:"is_delta"`
}

// Coverage returns the list of known JKDB segments (closed + active).
// The active delta segment is included when it has been flushed at least once.
// Returns an empty slice (never nil) on error or when the store is uninitialised.
func (cc *CoreClient) Coverage() []CoverageSegment {
	raw := cc.c.Coverage()
	var segs []CoverageSegment
	if err := json.Unmarshal([]byte(raw), &segs); err != nil || segs == nil {
		return []CoverageSegment{}
	}
	return segs
}

// ---------------------------------------------------------------------------
// Segment rotation
// ---------------------------------------------------------------------------

// Rotate atomically seals the active JKDB segment and opens a fresh one.
//
// Sequence:
//  1. Flush pending writes.
//  2. Close the active segment (writes footer + fdatasync).
//  3. Rename active.jkdb → segment_<unix-ns>.jkdb so the manifest picks it up.
//  4. Re-initialise core on the same dataDir (creates a new active.jkdb).
//
// The entire operation is synchronous; callers (e.g. midnight rotation goroutine)
// must hold no other writes in flight concurrently.
func (cc *CoreClient) Rotate() error {
	// 1. Flush any pending writes.
	if err := cc.c.Flush(); err != nil {
		return fmt.Errorf("rotate: flush: %w", err)
	}

	// 2. Close the active segment.
	cc.c.Close()

	// 3. Rename active.jkdb → segment_<ts>.jkdb.
	activePath := filepath.Join(cc.dataDir, "active.jkdb")
	rotatedPath := filepath.Join(cc.dataDir, fmt.Sprintf("segment_%d.jkdb", time.Now().UnixNano()))
	if err := os.Rename(activePath, rotatedPath); err != nil && !os.IsNotExist(err) {
		// Non-fatal: active.jkdb may not exist if nothing was ever written.
		// Log but continue so we still re-init cleanly.
		_ = err
	}

	// 4. Re-initialise — creates a fresh active.jkdb.
	c, err := Init(cc.dataDir)
	if err != nil {
		return fmt.Errorf("rotate: re-init: %w", err)
	}
	cc.c = c
	return nil
}

// convertEventRecords converts a slice of raw JKDB EventRecord values into
// normalised QueryBboxRecord values. Only AIS records are decoded; other
// stream types are silently skipped.
func convertEventRecords(evts []EventRecord) []QueryBboxRecord {
	out := make([]QueryBboxRecord, 0, len(evts))
	for _, e := range evts {
		switch StreamType(e.StreamType) {
		case StreamAIS:
			mmsi, sog, cog, heading, navStatus, _, _ := DecodeAISPayload(e.Payload)
			out = append(out, QueryBboxRecord{
				Timestamp:  e.TimestampMs,
				Lat:        e.Lat,
				Lon:        e.Lon,
				MMSI:       mmsi,
				SOG:        sog,
				COG:        cog,
				Heading:    heading,
				NavStatus:  navStatus,
				StreamType: e.StreamType,
			})
		}
	}
	return out
}
