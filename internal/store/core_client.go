package store

// #cgo LDFLAGS: -L../../../../jiskta-core/bin -lcore -Wl,-rpath,../../../../jiskta-core/bin
// #include "libcore.h"
// #include <stdlib.h>
import "C"
import (
"fmt"
"time"
"unsafe"
)

// StreamType mirrors the Ada Stream_Types.Stream_Id values and the C constants.
type StreamType uint8

const (
StreamAIS     StreamType = 1
StreamClimate StreamType = 2
StreamFlight  StreamType = 3
StreamIoT     StreamType = 4
StreamGeneric StreamType = 255
StreamAll     StreamType = 0 // query wildcard: match all stream types
)

// AISRecord is the Go representation of ais_record_t (64 bytes).
// Field order and types MUST match the C struct exactly.
type AISRecord struct {
Timestamp     int64
Lat           float32
Lon           float32
StreamType    uint8
Flags         uint8
SchemaVersion uint16
MMSI          uint32
SOG           uint16 // tenths of a knot
COG           uint16 // tenths of a degree
Heading       uint16
NavStatus     uint8
MsgType       uint8
VesselType    uint16
_             [30]uint8 // pad
}

// FlightRecord is the Go representation of flight_record_t (64 bytes).
type FlightRecord struct {
Timestamp     int64
Lat           float32
Lon           float32
StreamType    uint8
Flags         uint8
SchemaVersion uint16
ICAO24        uint32
Altitude      int32
Velocity      uint16
Heading       uint16
VerticalRate  int16
Callsign      [8]byte
Squawk        uint16
OnGround      uint8
Category      uint8
_             [18]uint8 // pad
}

// Client wraps the libcore.so C API.
type Client struct {
dataDir string
}

// Init initialises the jiskta-core engine against the given data directory.
// Call once at startup.
func Init(dataDir string) (*Client, error) {
cDir := C.CString(dataDir)
defer C.free(unsafe.Pointer(cDir))

ret := C.core_init(cDir)
if ret < 0 {
return nil, fmt.Errorf("core_init failed: %d", ret)
}
return &Client{dataDir: dataDir}, nil
}

// Close flushes pending writes and shuts down the engine.
func (c *Client) Close() error {
if ret := C.core_close(); ret < 0 {
return fmt.Errorf("core_close failed: %d", ret)
}
return nil
}

// WriteAIS writes a batch of AIS records using core_write_ais.
func (c *Client) WriteAIS(records []AISRecord) error {
if len(records) == 0 {
return nil
}
ret := C.core_write_ais(
(*C.ais_record_t)(unsafe.Pointer(&records[0])),
C.int(len(records)),
)
if int(ret) != len(records) {
return fmt.Errorf("core_write_ais: wrote %d of %d records", ret, len(records))
}
return nil
}

// WriteFlight writes a batch of ADS-B flight records using core_write_flight.
func (c *Client) WriteFlight(records []FlightRecord) error {
if len(records) == 0 {
return nil
}
ret := C.core_write_flight(
(*C.flight_record_t)(unsafe.Pointer(&records[0])),
C.int(len(records)),
)
if int(ret) != len(records) {
return fmt.Errorf("core_write_flight: wrote %d of %d records", ret, len(records))
}
return nil
}

// WriteRaw writes arbitrary records using the generic core_write_raw path.
// records must be a packed slice of record_size-byte elements, each starting
// with the common 20-byte envelope.
func (c *Client) WriteRaw(streamType StreamType, records []byte, recordSize uint16) error {
if len(records) == 0 {
return nil
}
n := len(records) / int(recordSize)
ret := C.core_write_raw(
C.uint8_t(streamType),
(*C.uint8_t)(unsafe.Pointer(&records[0])),
C.int(n),
C.uint16_t(recordSize),
)
if int(ret) != n {
return fmt.Errorf("core_write_raw: wrote %d of %d records", ret, n)
}
return nil
}

// QueryResult holds the decoded result of a bbox or track query.
type QueryResult struct {
AIS    []AISRecord
Flight []FlightRecord
// Raw holds records of stream types not yet decoded by this client.
Raw [][]byte
}

// QueryBbox queries all streams for positions within the given bbox and time range.
// Pass streamType=StreamAll (0) to return all stream types.
// Pass mmsi=0 to skip MMSI filtering.
func (c *Client) QueryBbox(
latMin, latMax, lonMin, lonMax float32,
tStart, tEnd time.Time,
streamType StreamType,
mmsi uint32,
) (*QueryResult, error) {
tsStart := C.int64_t(tStart.UnixMilli())
tsEnd := C.int64_t(tEnd.UnixMilli())

raw := C.core_query_bbox(
C.float(latMin), C.float(latMax),
C.float(lonMin), C.float(lonMax),
tsStart, tsEnd,
C.uint8_t(streamType),
C.uint32_t(mmsi),
)
if raw == nil {
return nil, fmt.Errorf("core_query_bbox returned nil")
}
defer C.core_free_result(raw)
return decodeQueryResult(raw), nil
}

// QueryMMSI returns the full track for a single MMSI over the given time range.
func (c *Client) QueryMMSI(mmsi uint32, tStart, tEnd time.Time) (*QueryResult, error) {
raw := C.core_query_mmsi(
C.uint32_t(mmsi),
C.int64_t(tStart.UnixMilli()),
C.int64_t(tEnd.UnixMilli()),
)
if raw == nil {
return nil, fmt.Errorf("core_query_mmsi returned nil")
}
defer C.core_free_result(raw)
return decodeQueryResult(raw), nil
}

// QueryICAO24 returns the full track for a single ICAO24 over the given time range.
func (c *Client) QueryICAO24(icao24 uint32, tStart, tEnd time.Time) (*QueryResult, error) {
raw := C.core_query_icao24(
C.uint32_t(icao24),
C.int64_t(tStart.UnixMilli()),
C.int64_t(tEnd.UnixMilli()),
)
if raw == nil {
return nil, fmt.Errorf("core_query_icao24 returned nil")
}
defer C.core_free_result(raw)
return decodeQueryResult(raw), nil
}

// Stats returns a JSON string with engine stats (buffer fill, flushed count, etc.)
func (c *Client) Stats() string {
return C.GoString(C.core_stats())
}

// decodeQueryResult reads the raw byte array returned by the C query functions
// and dispatches each record to the appropriate Go slice based on stream_type
// (byte 16 of the common envelope).
func decodeQueryResult(raw *C.query_result_t) *QueryResult {
n := int(raw.count)
recSize := int(raw.record_size) // always 64 for JKST1 v1
result := &QueryResult{}
if n == 0 || raw.records == nil {
return result
}

base := uintptr(unsafe.Pointer(raw.records))
for i := 0; i < n; i++ {
ptr := base + uintptr(i*recSize)
// Byte 16 = stream_type (offset 16 in the common envelope).
stype := *(*uint8)(unsafe.Pointer(ptr + 16))
switch StreamType(stype) {
case StreamAIS:
rec := *(*AISRecord)(unsafe.Pointer(ptr))
result.AIS = append(result.AIS, rec)
case StreamFlight:
rec := *(*FlightRecord)(unsafe.Pointer(ptr))
result.Flight = append(result.Flight, rec)
default:
raw := make([]byte, recSize)
copy(raw, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), recSize))
result.Raw = append(result.Raw, raw)
}
}
return result
}
