package ingest

import (
	"sync"
	"time"
)

// Cleaner applies data quality rules and deduplication to a stream of
// DecodedPositions. It is safe to call from a single goroutine.
//
// Rules applied (in order):
//  1. MMSI validity:   must be 1–999_999_999
//  2. Coordinate bounds: lat ∈ [-90, 90], lon ∈ [-180, 180]
//  3. SOG cap:          SOG > 1022 tenths-of-knot (102.2 kn) → rejected
//  4. 2-second dedup:   same MMSI + identical (lat, lon) within 2s → skip
//
// Counters are exported for metrics / logging.

const (
	minMMSI        = 1
	maxMMSI        = 999_999_999
	maxSOGTenths   = 1022          // 102.2 knots × 10
	dedupWindow    = 2 * time.Second
)

// dedupKey identifies a duplicate: same vessel, same position.
type dedupKey struct {
	MMSI uint32
	Lat  float32
	Lon  float32
}

type dedupEntry struct {
	seenAt time.Time
}

// Cleaner holds the dedup state and rejection counters.
type Cleaner struct {
	mu sync.Mutex

	// dedup cache: dedupKey → last time seen
	cache map[dedupKey]dedupEntry

	// counters
	Accepted    int64
	RejMMSI     int64 // invalid MMSI
	RejCoords   int64 // out-of-range lat/lon
	RejSOG      int64 // SOG > 102.2 kn
	RejDuplicate int64 // dedup within window

	// eviction: we prune the cache periodically to bound memory.
	evictEvery int
	seen       int
}

// NewCleaner returns a Cleaner ready for use.
func NewCleaner() *Cleaner {
	return &Cleaner{
		cache:      make(map[dedupKey]dedupEntry),
		evictEvery: 10_000,
	}
}

// Accept returns true if the record passes all quality rules and should be
// written to storage. Call this before converting to an AISRecord.
func (c *Cleaner) Accept(pos *DecodedPosition, now time.Time) bool {
	// Rule 1: MMSI
	if pos.MMSI < minMMSI || pos.MMSI > maxMMSI {
		c.mu.Lock()
		c.RejMMSI++
		c.mu.Unlock()
		return false
	}

	// Rule 2: coordinate bounds
	if pos.Lat < -90 || pos.Lat > 90 || pos.Lon < -180 || pos.Lon > 180 {
		c.mu.Lock()
		c.RejCoords++
		c.mu.Unlock()
		return false
	}

	// Rule 3: SOG cap
	if pos.SOG > maxSOGTenths {
		c.mu.Lock()
		c.RejSOG++
		c.mu.Unlock()
		return false
	}

	// Rule 4: 2-second dedup
	key := dedupKey{MMSI: pos.MMSI, Lat: pos.Lat, Lon: pos.Lon}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.cache[key]; ok {
		if now.Sub(entry.seenAt) < dedupWindow {
			c.RejDuplicate++
			return false
		}
	}
	c.cache[key] = dedupEntry{seenAt: now}
	c.Accepted++

	// Periodic eviction of old cache entries.
	c.seen++
	if c.seen >= c.evictEvery {
		c.evict(now)
		c.seen = 0
	}
	return true
}

// evict removes entries older than dedupWindow. Must be called with c.mu held.
func (c *Cleaner) evict(now time.Time) {
	cutoff := now.Add(-dedupWindow)
	for k, v := range c.cache {
		if v.seenAt.Before(cutoff) {
			delete(c.cache, k)
		}
	}
}

// ResetCounters zeroes all rejection/acceptance counters (for metrics scraping).
func (c *Cleaner) ResetCounters() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Accepted = 0
	c.RejMMSI = 0
	c.RejCoords = 0
	c.RejSOG = 0
	c.RejDuplicate = 0
}
