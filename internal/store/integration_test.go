package store

import (
	"testing"
	"time"
)

// TestCoreClientIntegration tests the full store layer end-to-end without an HTTP server:
// write → flush → query (bbox) → query (MMSI) → coverage.
func TestCoreClientIntegration(t *testing.T) {
	dir := t.TempDir()

	cc, err := NewCoreClient(dir)
	if err != nil {
		t.Fatalf("NewCoreClient: %v", err)
	}
	defer cc.Close()

	// Build 120 synthetic AIS records: 100 for MMSI=123456789, 20 for MMSI=987654321
	now := time.Now().UnixMilli()
	records := make([]AISRecord, 0, 120)
	for i := 0; i < 100; i++ {
		records = append(records, AISRecord{
			Timestamp:     now - int64(i)*5000, // 5s apart
			Lat:           51.5 + float32(i)*0.001,
			Lon:           0.1 + float32(i)*0.001,
			MMSI:          123456789,
			SOG:           80, // 8.0 knots (tenths)
			COG:           900,
			Heading:       90,
			NavStatus:     0,
			MsgType:       1,
			StreamType:    1, // StreamAIS
			SchemaVersion: 1,
		})
	}
	for i := 0; i < 20; i++ {
		records = append(records, AISRecord{
			Timestamp:     now - int64(i)*10000,
			Lat:           48.8 + float32(i)*0.001,
			Lon:           2.3 + float32(i)*0.001,
			MMSI:          987654321,
			SOG:           120,
			COG:           1800,
			Heading:       180,
			NavStatus:     0,
			MsgType:       1,
			StreamType:    1,
			SchemaVersion: 1,
		})
	}

	// Write all records
	if err := cc.WriteAIS(records); err != nil {
		t.Fatalf("WriteAIS: %v", err)
	}

	// Flush to disk
	if err := cc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// --- Test 1: bbox query returns all records in the broad bbox ---
	tsStart := now - 200*5000
	tsEnd := now + 1000
	bboxResults, err := cc.QueryBbox(48.0, 52.5, 0.0, 3.5, tsStart, tsEnd, 0, 0)
	if err != nil {
		t.Fatalf("QueryBbox: %v", err)
	}
	if len(bboxResults) < 120 {
		t.Errorf("QueryBbox: expected ≥120 records, got %d", len(bboxResults))
	}

	// --- Test 2: MMSI filter returns only the target vessel's records ---
	mmsiResults, err := cc.QueryMMSI(123456789, tsStart, tsEnd, 0)
	if err != nil {
		t.Fatalf("QueryMMSI: %v", err)
	}
	if len(mmsiResults) < 100 {
		t.Errorf("QueryMMSI(123456789): expected ≥100 records, got %d", len(mmsiResults))
	}
	for _, r := range mmsiResults {
		if r.MMSI != 123456789 {
			t.Errorf("QueryMMSI returned wrong MMSI: got %d", r.MMSI)
		}
	}

	// --- Test 3: second MMSI only returns its own records ---
	mmsiResults2, err := cc.QueryMMSI(987654321, tsStart, tsEnd, 0)
	if err != nil {
		t.Fatalf("QueryMMSI(987654321): %v", err)
	}
	for _, r := range mmsiResults2 {
		if r.MMSI != 987654321 {
			t.Errorf("QueryMMSI(987654321) returned wrong MMSI: got %d", r.MMSI)
		}
	}

	// --- Test 4: Coverage returns ≥1 segment after flush ---
	segs := cc.Coverage()
	if len(segs) == 0 {
		t.Error("Coverage: expected ≥1 segment after flush, got 0")
	}
	for _, s := range segs {
		if s.Path == "" {
			t.Error("Coverage: segment has empty path")
		}
		if s.TMaxMs < s.TMinMs {
			t.Errorf("Coverage: segment %s has t_max < t_min", s.Path)
		}
	}
}
