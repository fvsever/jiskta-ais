package store

import (
	"encoding/json"
	"time"
)

// CoreClient wraps the CGo Client with higher-level methods matching the API handlers.
// This avoids modifying core_client.go (which mirrors the C FFI exactly).

type CoreClient struct {
	c *Client
}

// NewCoreClient opens the jiskta-core store at dataDir.
func NewCoreClient(dataDir string) (*CoreClient, error) {
	c, err := Init(dataDir)
	if err != nil {
		return nil, err
	}
	return &CoreClient{c: c}, nil
}

// Close releases the store handle.
func (cc *CoreClient) Close() error {
	return cc.c.Close()
}

// WriteAIS writes a batch of AIS records to the store (satisfies ingest.Storer interface).
func (cc *CoreClient) WriteAIS(records []AISRecord) error {
	return cc.c.WriteAIS(records)
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
// mmsi=0 returns all vessels; limit caps the result count.
func (cc *CoreClient) QueryBbox(
	latMin, latMax, lonMin, lonMax float32,
	tsStartMs, tsEndMs int64,
	mmsi uint32, limit int,
) ([]QueryBboxRecord, error) {
	tStart := time.UnixMilli(tsStartMs)
	tEnd := time.UnixMilli(tsEndMs)

	qr, err := cc.c.QueryBbox(latMin, latMax, lonMin, lonMax, tStart, tEnd, StreamAll, mmsi)
	if err != nil {
		return nil, err
	}
	if qr == nil {
		return []QueryBboxRecord{}, nil
	}
	result := convertQueryResult(qr)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// QueryMMSI returns the full track for a single vessel in the time range.
func (cc *CoreClient) QueryMMSI(mmsi uint32, tsStartMs, tsEndMs int64, limit int) ([]QueryBboxRecord, error) {
	tStart := time.UnixMilli(tsStartMs)
	tEnd := time.UnixMilli(tsEndMs)

	qr, err := cc.c.QueryMMSI(mmsi, tStart, tEnd)
	if err != nil {
		return nil, err
	}
	if qr == nil {
		return []QueryBboxRecord{}, nil
	}
	result := convertQueryResult(qr)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Stats returns human-readable coverage / stats JSON from the store.
func (cc *CoreClient) Stats() map[string]interface{} {
	raw := cc.c.Stats()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]interface{}{"raw": raw}
	}
	return out
}

func convertQueryResult(qr *QueryResult) []QueryBboxRecord {
	out := make([]QueryBboxRecord, 0, len(qr.AISRecords))
	for _, r := range qr.AISRecords {
		out = append(out, QueryBboxRecord{
			Timestamp:  r.Timestamp,
			Lat:        r.Lat,
			Lon:        r.Lon,
			MMSI:       r.MMSI,
			SOG:        r.SOG,
			COG:        r.COG,
			Heading:    r.Heading,
			NavStatus:  r.NavStatus,
			StreamType: uint8(StreamAIS),
		})
	}
	for _, r := range qr.FlightRecords {
		out = append(out, QueryBboxRecord{
			Timestamp:  r.Timestamp,
			Lat:        r.Lat,
			Lon:        r.Lon,
			StreamType: uint8(StreamFlight),
		})
	}
	return out
}
