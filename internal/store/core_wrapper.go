package store

import (
	"encoding/json"
)

// CoreClient wraps the CGo Client with higher-level methods matching the API handlers.
// This avoids modifying core_client.go (which mirrors the C FFI exactly).

type CoreClient struct {
	c *Client
}

// NewCoreClient opens the jiskta-core JKDB store at dataDir.
func NewCoreClient(dataDir string) (*CoreClient, error) {
	c, err := Init(dataDir)
	if err != nil {
		return nil, err
	}
	return &CoreClient{c: c}, nil
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

// Stats returns human-readable coverage/stats JSON from the store.
func (cc *CoreClient) Stats() map[string]interface{} {
	raw := cc.c.Stats()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]interface{}{"raw": raw}
	}
	return out
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
