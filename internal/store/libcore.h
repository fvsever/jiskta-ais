// C header matching jiskta-core's libcore_ffi.ads exports.
// Included by internal/store/core_client.go via CGo.
// Must be kept in sync with src/ffi/libcore_ffi.ads in jiskta-core.
//
// Design: every record type starts with a common 20-byte spatial envelope.
// Query results are returned as raw bytes; caller reads envelope.stream_type
// to determine which concrete type to cast to.

#ifndef LIBCORE_H
#define LIBCORE_H

#include <stdint.h>

// ---------------------------------------------------------------------------
// Common 20-byte spatial envelope -- at the start of EVERY record type.
// The geohash indexer and query planner only read this struct.
// ---------------------------------------------------------------------------
typedef struct __attribute__((packed)) {
    int64_t  timestamp;       // Unix milliseconds
    float    lat;             // WGS-84 latitude
    float    lon;             // WGS-84 longitude
    uint8_t  stream_type;     // 1=AIS, 2=Climate, 3=Flight, 255=Generic
    uint8_t  flags;           // reserved = 0
    uint16_t schema_version;  // = 1 for all current types
} envelope_t;

// ---------------------------------------------------------------------------
// Stream type constants (match Stream_Types.Stream_Id in Ada)
// ---------------------------------------------------------------------------
#define STREAM_TYPE_AIS      1
#define STREAM_TYPE_CLIMATE  2
#define STREAM_TYPE_FLIGHT   3
#define STREAM_TYPE_IOT      4
#define STREAM_TYPE_GENERIC  255

// ---------------------------------------------------------------------------
// Concrete record types (64 bytes each, envelope at bytes 0-19)
// ---------------------------------------------------------------------------

// AIS position record (64 bytes)
typedef struct __attribute__((packed)) {
    int64_t  timestamp;       // offset  0 -- envelope
    float    lat;             // offset  8
    float    lon;             // offset 12
    uint8_t  stream_type;     // offset 16 -- always STREAM_TYPE_AIS
    uint8_t  flags;           // offset 17
    uint16_t schema_version;  // offset 18
    uint32_t mmsi;            // offset 20 -- AIS payload
    uint16_t sog;             // offset 24 -- tenths of a knot
    uint16_t cog;             // offset 26 -- tenths of a degree
    uint16_t heading;         // offset 28
    uint8_t  nav_status;      // offset 30
    uint8_t  msg_type;        // offset 31
    uint16_t vessel_type;     // offset 32
    uint8_t  pad[30];         // offset 34 -- zero padding to 64 bytes
} ais_record_t;

// Climate point measurement record (64 bytes)
typedef struct __attribute__((packed)) {
    int64_t  timestamp;       // offset  0 -- envelope
    float    lat;             // offset  8
    float    lon;             // offset 12
    uint8_t  stream_type;     // offset 16 -- always STREAM_TYPE_CLIMATE
    uint8_t  flags;           // offset 17
    uint16_t schema_version;  // offset 18
    float    value;           // offset 20 -- measured value in canonical unit
    float    anomaly;         // offset 24 -- deviation from climatological mean
    uint8_t  variable_id;     // offset 28 -- see variable_id table in docs
    uint8_t  source_id;       // offset 29 -- data source (CAMS=1, ERA5=2, ...)
    uint8_t  quality;         // offset 30 -- 0=unknown, 1=NRT, 2=interim, 3=validated
    uint8_t  unit_code;       // offset 31 -- physical unit (ug_m3=1, K=2, m_s=3, ...)
    uint16_t grid_step_e4;    // offset 32 -- grid step in units of 0.0001 degrees
    uint8_t  pad[30];         // offset 34
} climate_record_t;

// ADS-B flight record (64 bytes)
typedef struct __attribute__((packed)) {
    int64_t  timestamp;       // offset  0 -- envelope
    float    lat;             // offset  8
    float    lon;             // offset 12
    uint8_t  stream_type;     // offset 16 -- always STREAM_TYPE_FLIGHT
    uint8_t  flags;           // offset 17
    uint16_t schema_version;  // offset 18
    uint32_t icao24;          // offset 20 -- 24-bit ICAO aircraft address
    int32_t  altitude;        // offset 24 -- barometric altitude in feet
    uint16_t velocity;        // offset 28 -- ground speed in knots
    uint16_t heading;         // offset 30 -- degrees true
    int16_t  vertical_rate;   // offset 32 -- ft/min (positive = climbing)
    char     callsign[8];     // offset 34 -- ICAO flight callsign (null-padded)
    uint16_t squawk;          // offset 42 -- Mode-C transponder code
    uint8_t  on_ground;       // offset 44 -- 1 if on ground
    uint8_t  category;        // offset 45 -- ADS-B emitter category
    uint8_t  pad[18];         // offset 46
} flight_record_t;

// ---------------------------------------------------------------------------
// Query result
// ---------------------------------------------------------------------------
// records: raw bytes (n * 64 bytes).
// Read envelope_t.stream_type to determine which concrete type to cast to.
// Call core_free_result when done.
typedef struct {
    uint8_t  *records;      // raw bytes -- cast to ais_record_t / climate_record_t / etc.
    uint16_t  record_size;  // always 64 for JKST1 v1
    int       count;
    int       truncated;    // 1 if MAX_QUERY_RESULTS was hit
} query_result_t;

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------
int  core_init(const char *data_dir);
int  core_close(void);

// ---------------------------------------------------------------------------
// Write -- typed variants
// ---------------------------------------------------------------------------
int  core_write_ais    (const ais_record_t     *records, int n);
int  core_write_climate(const climate_record_t *records, int n);
int  core_write_flight (const flight_record_t  *records, int n);

// Generic raw write: records is a packed array of record_size-byte records.
// Only the first 20 bytes (common envelope) are parsed for indexing.
int  core_write_raw(uint8_t stream_type,
                    const uint8_t *records, int n, uint16_t record_size);

// ---------------------------------------------------------------------------
// Query -- stream-type agnostic
// ---------------------------------------------------------------------------
// stream_type = 0 means "all stream types".
// mmsi = 0 means no MMSI filter (only relevant for AIS).
query_result_t *core_query_bbox(float lat_min, float lat_max,
                                float lon_min, float lon_max,
                                int64_t t_start, int64_t t_end,
                                uint8_t stream_type, uint32_t mmsi);

query_result_t *core_query_mmsi  (uint32_t mmsi,
                                   int64_t t_start, int64_t t_end);
query_result_t *core_query_icao24(uint32_t icao24,
                                   int64_t t_start, int64_t t_end);

void core_free_result(query_result_t *result);

// ---------------------------------------------------------------------------
// Introspection
// ---------------------------------------------------------------------------
const char *core_stats(void);         // static JSON buffer -- do NOT free
const char *core_stream_types(void);  // static JSON array -- do NOT free

#endif // LIBCORE_H
