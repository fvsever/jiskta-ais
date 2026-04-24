// JKDB C ABI header — jiskta-core v2 (JKDB engine)
// Included by internal/store/core_client.go via CGo.
// Must be kept in sync with src/ffi/libcore_ffi.ads in jiskta-core.

#ifndef LIBCORE_H
#define LIBCORE_H

#include <stdint.h>

// ---------------------------------------------------------------------------
// Stream type constants
// ---------------------------------------------------------------------------
#define STREAM_TYPE_AIS      1
#define STREAM_TYPE_CLIMATE  2
#define STREAM_TYPE_FLIGHT   3
#define STREAM_TYPE_IOT      4
#define STREAM_TYPE_GENERIC  255

// ---------------------------------------------------------------------------
// Query output modes
// ---------------------------------------------------------------------------
#define QUERY_MODE_RAW     0
#define QUERY_MODE_STATS   1
#define QUERY_MODE_DAILY   2
#define QUERY_MODE_MONTHLY 3

// ---------------------------------------------------------------------------
// event_record_t — 64-byte unified JKDB event record (all stream types)
// Morton=0 triggers auto-compute from lat/lon in core_write_event.
//
// Offsets (naturally aligned, no __packed needed):
//   0:  timestamp_ms  int64    (8)
//   8:  lat           float32  (4)
//  12:  lon           float32  (4)
//  16:  morton        uint64   (8)
//  24:  entity_hash   uint32   (4)   AIS: MMSI
//  28:  stream_type   uint8    (1)
//  29:  flags         uint8    (1)
//  30:  schema_ver    uint16   (2)
//  32:  payload       uint8[32]
// Total: 64 bytes
// ---------------------------------------------------------------------------
typedef struct {
    int64_t  timestamp_ms;
    float    lat;
    float    lon;
    uint64_t morton;
    uint32_t entity_hash;
    uint8_t  stream_type;
    uint8_t  flags;
    uint16_t schema_ver;
    uint8_t  payload[32];
} event_record_t;

// AIS payload layout (payload[0..17]):
//   [0..3]:  mmsi         uint32
//   [4..5]:  sog          uint16   tenths of a knot
//   [6..7]:  cog          uint16   tenths of a degree
//   [8..9]:  heading      uint16
//   [10]:    nav_status   uint8
//   [11]:    msg_type     uint8
//   [12..13]: vessel_type uint16
//   [14..31]: zero pad

// ---------------------------------------------------------------------------
// raster_meta_t — metadata for a RASTER_BLOCK ingest
// ---------------------------------------------------------------------------
typedef struct {
    uint32_t variable_id;
    int64_t  time_start;
    uint32_t time_step_ms;
    uint32_t n_times;
    uint32_t n_lats;
    uint32_t n_lons;
    float    lat_origin;
    float    lon_origin;
    float    lat_step;
    float    lon_step;
    float    scale;
} raster_meta_t;

// raster_row_t — 24-byte decoded raster cell returned by core_query_raster
typedef struct {
    int64_t  timestamp_ms;
    float    lat;
    float    lon;
    float    value;
    uint32_t variable_id;
} raster_row_t;

typedef struct {
    raster_row_t *rows;
    uint32_t      count;
    uint8_t       truncated;
    uint8_t       pad[3];
} raster_result_t;

// ---------------------------------------------------------------------------
// query_ir_t — query intermediate representation (600 bytes)
// MUST mirror Ada side libcore_ffi.ads C_Query_IR exactly.
// AIS does not currently use the Q2-* push-down filters, but the struct
// layout MUST match what Ada reads to avoid uninitialized-memory reads.
// All fields beyond byte 56 are zero-initialised by Go cgo struct literals.
// ---------------------------------------------------------------------------
typedef struct {
    int64_t  t_start_ms;            //   0
    int64_t  t_end_ms;              //   8
    float    lat_min;               //  16
    float    lat_max;               //  20
    float    lon_min;               //  24
    float    lon_max;               //  28
    uint32_t dataset_id;            //  32
    uint32_t stream_type;           //  36
    uint64_t entity_hash;           //  40
    uint32_t limit;                 //  48
    uint8_t  sort_desc;             //  52
    uint8_t  mode;                  //  53
    uint16_t reserved;              //  54
    uint32_t offset_rows;           //  56
    uint8_t  entity_hash_count;     //  60  Q2-B: 0 = disabled
    uint8_t  payload_offset_b;      //  61
    uint8_t  payload_op;            //  62
    uint8_t  pad3;                  //  63
    float    payload_value;         //  64
    uint32_t pad4;                  //  68
    uint64_t entity_hash_list[64];  //  72
    uint32_t route_hash_filter;     // 584  transit-only, ignored for AIS (=0)
    uint8_t  vehicle_type_filter;   // 588  transit-only (sentinel 0xFF)
    uint8_t  operator_id_filter;    // 589  transit-only (=0)
    uint16_t pad5;                  // 590
    uint64_t pad6;                  // 592 (struct = 600 bytes)
} query_ir_t;

// ---------------------------------------------------------------------------
// query_result_t — event query result (16 bytes)
// ---------------------------------------------------------------------------
typedef struct {
    event_record_t *records;
    uint32_t        count;
    uint8_t         truncated;
    uint8_t         pad[3];
} query_result_t;

// agg_result_t — server-side aggregation result
typedef struct {
    uint64_t count;
    double   sum;
    float    min_val;
    float    max_val;
    float    mean;
    float    p50;
    float    p95;
    uint8_t  truncated;
    uint8_t  pad[3];
    char     error[64];
} agg_result_t;

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------
int  core_init (const char *data_dir);
void core_close(void);
int  core_flush(void);

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------
int core_write_event (const event_record_t *records, int n);
int core_write_raster(const float *data, raster_meta_t *meta);

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------
query_result_t  *core_query         (query_ir_t *q);
agg_result_t    *core_query_agg     (query_ir_t *q);
raster_result_t *core_query_raster  (query_ir_t *q);

void core_free_result        (query_result_t  *r);
void core_free_agg_result    (agg_result_t    *r);
void core_free_raster_result (raster_result_t *r);

// ---------------------------------------------------------------------------
// Timing introspection
// ---------------------------------------------------------------------------
// Returns a struct with plan_ns / read_ns / decode_ns for the last query.
// NOTE: Ada FFI returns a C_Query_Timing struct by value; unused in Go.
// const char *core_last_query_timing(void);

// ---------------------------------------------------------------------------
// Coverage
// ---------------------------------------------------------------------------
// Returns a static heap-allocated JSON string describing all known segments.
// Format: [{"path":"...","t_min_ms":N,"t_max_ms":N,"dataset_id":N,"is_delta":true}, ...]
// The returned pointer must NOT be freed — it is managed by jiskta-core.
const char *core_coverage(void);

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------
// Compact a closed Delta segment at delta_path into a Morton-sorted Base
// segment written as <delta_path>_base.jkdb.
// Returns 0=ok, -1=bad argument / not initialised, -2=compaction error.
int core_compact(const char *delta_path);

#endif // LIBCORE_H
