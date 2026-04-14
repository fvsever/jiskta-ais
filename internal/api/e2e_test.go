// e2e_test.go — HTTP-level integration test for the jiskta-ais API.
//
// Tests the full request/response path: HTTP server → auth middleware →
// handler → store (jiskta-core CGo) → JSON response.
//
// Build tag: integration (excluded from unit-test runs unless -tags integration is set).
// Run with:
//   cd jiskta-ais &&
//   CGO_ENABLED=1 CGO_LDFLAGS="-L../../jiskta-core/bin -Wl,-rpath,../../jiskta-core/bin -luring" \
//     go test ./internal/api/... -v -tags integration -run TestE2E -timeout 60s

//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/fvsever/jiskta-ais/internal/api"
	"github.com/fvsever/jiskta-ais/internal/auth"
	"github.com/fvsever/jiskta-ais/internal/store"
)

// -------------------------------------------------------------------------
// Mock auth that always succeeds with a fixed balance.
// -------------------------------------------------------------------------

type mockAuth struct{}

func (m *mockAuth) ValidateKey(_ string) (*auth.KeyInfo, error) {
	return &auth.KeyInfo{
		APIKEYID:      "test-key-id",
		UserEmail:     "test@example.com",
		CreditBalance: 1_000_000,
		IsActive:      true,
	}, nil
}

func (m *mockAuth) UpdateCachedBalance(_ string, _ int64) {}
func (m *mockAuth) DeductCredits(_ string, _ int64, _ string) {}

// -------------------------------------------------------------------------
// newTestServer creates an httptest.Server backed by a temp JKDB store.
// -------------------------------------------------------------------------

func newTestServer(t *testing.T) (*httptest.Server, *store.CoreClient, func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "ais_e2e_*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	cc, err := store.NewCoreClient(dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewCoreClient: %v", err)
	}

	srv := api.NewServerWithAuth(&mockAuth{}, cc)
	ts := httptest.NewServer(srv.Handler())

	cleanup := func() {
		ts.Close()
		cc.Close()
		os.RemoveAll(dir)
	}
	return ts, cc, cleanup
}

// -------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------

// TestE2E_Health verifies /health returns 200 + correct body without auth.
func TestE2E_Health(t *testing.T) {
	ts, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "healthy" {
		t.Errorf("want status=healthy, got %q", body["status"])
	}
}

// TestE2E_AuthRequired verifies that protected endpoints reject requests without an API key.
func TestE2E_AuthRequired(t *testing.T) {
	ts, _, cleanup := newTestServer(t)
	defer cleanup()

	endpoints := []string{
		"/api/v1/ais/query?lat_min=0&lat_max=10&lon_min=0&lon_max=10&time_start=0&time_end=99999999999",
		"/api/v1/ais/vessel/123456789",
	}
	for _, ep := range endpoints {
		resp, err := http.Get(ts.URL + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", ep, resp.StatusCode)
		}
	}
}

// TestE2E_QueryReturnsWrittenRecords writes AIS records, flushes, and verifies
// that a bbox+time query over the HTTP API returns those records.
func TestE2E_QueryReturnsWrittenRecords(t *testing.T) {
	ts, cc, cleanup := newTestServer(t)
	defer cleanup()

	const (
		nRecords   = 50
		testMMSI   = uint32(123_456_789)
		centerLat  = 52.0
		centerLon  = 4.5
	)

	baseMS := time.Now().Add(-2 * time.Hour).UnixMilli()
	records := make([]store.AISRecord, nRecords)
	for i := range records {
		records[i] = store.AISRecord{
			Timestamp:     baseMS + int64(i)*60_000,
			Lat:           centerLat + float32(i)*0.001,
			Lon:           centerLon + float32(i)*0.001,
			StreamType:    1, // AIS
			MMSI:          testMMSI,
			SOG:           100,
			COG:           900,
			Heading:       180,
			NavStatus:     0,
			MsgType:       1,
			VesselType:    70,
		}
	}

	if err := cc.WriteAIS(records); err != nil {
		t.Fatalf("WriteAIS: %v", err)
	}
	if err := cc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Build query URL: bbox covers all written points, time covers the window.
	tStart := baseMS - 1000
	tEnd := baseMS + int64(nRecords)*60_000 + 1000
	url := fmt.Sprintf(
		"%s/api/v1/ais/query?lat_min=51&lat_max=53&lon_min=4&lon_max=5&time_start=%d&time_end=%d&limit=10000",
		ts.URL, tStart, tEnd,
	)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-API-Key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /query: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status  string           `json:"status"`
		Records []map[string]any `json:"records"`
		Count   int              `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != "success" {
		t.Errorf("want status=success, got %q", result.Status)
	}
	if len(result.Records) < nRecords {
		t.Errorf("want ≥%d records, got %d", nRecords, len(result.Records))
	}
}

// TestE2E_Coverage verifies /api/v1/ais/coverage returns a JSON array after writing.
func TestE2E_Coverage(t *testing.T) {
	ts, cc, cleanup := newTestServer(t)
	defer cleanup()

	// Write at least one record so the active segment exists.
	if err := cc.WriteAIS([]store.AISRecord{{
		Timestamp:  time.Now().UnixMilli(),
		Lat:        52.0,
		Lon:        4.5,
		StreamType: 1,
		MMSI:       999,
	}}); err != nil {
		t.Fatalf("WriteAIS: %v", err)
	}
	if err := cc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/ais/coverage")
	if err != nil {
		t.Fatalf("GET /coverage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var result struct {
		Status   string `json:"status"`
		Segments []any  `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Segments) == 0 {
		t.Error("want ≥1 coverage segment, got 0")
	}
}

// TestE2E_VesselTrack writes records for a vessel and queries via /vessel/{mmsi}.
func TestE2E_VesselTrack(t *testing.T) {
	ts, cc, cleanup := newTestServer(t)
	defer cleanup()

	const testMMSI = uint32(246_813_579)
	baseMS := time.Now().Add(-1 * time.Hour).UnixMilli()

	batch := make([]store.AISRecord, 30)
	for i := range batch {
		batch[i] = store.AISRecord{
			Timestamp:  baseMS + int64(i)*60_000,
			Lat:        48.85 + float32(i)*0.01,
			Lon:        2.35 + float32(i)*0.01,
			StreamType: 1,
			MMSI:       testMMSI,
		}
	}
	if err := cc.WriteAIS(batch); err != nil {
		t.Fatalf("WriteAIS: %v", err)
	}
	if err := cc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	url := fmt.Sprintf("%s/api/v1/ais/vessel/%d?time_start=%d&time_end=%d",
		ts.URL, testMMSI, baseMS-1000, baseMS+int64(len(batch))*60_000+1000)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-API-Key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /vessel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status  string           `json:"status"`
		Records []map[string]any `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != "success" {
		t.Errorf("want status=success, got %q", result.Status)
	}
	if len(result.Records) < 30 {
		t.Errorf("want ≥30 records, got %d", len(result.Records))
	}
}
