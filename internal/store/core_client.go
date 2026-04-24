package store

// #cgo LDFLAGS: -L${SRCDIR}/../../../jiskta-core/bin -lcore -Wl,-rpath,${SRCDIR}/../../../jiskta-core/bin -luring
// #include "libcore.h"
// #include <stdlib.h>
import "C"
import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

// StreamType mirrors the JKDB stream-type constants (STREAM_TYPE_* in libcore.h).
type StreamType uint8

const (
	StreamAIS     StreamType = 1
	StreamClimate StreamType = 2
	StreamFlight  StreamType = 3
	StreamIoT     StreamType = 4
	StreamGeneric StreamType = 255
	StreamAll     StreamType = 0 // query wildcard: match all stream types
)

// AISRecord is the pure-Go representation of an AIS vessel position.
// Used by pipeline.go to construct records; packed into EventRecord for write.
type AISRecord struct {
	Timestamp     int64   // Unix milliseconds
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
}

// EventRecord mirrors event_record_t (64 bytes) in the JKDB C ABI.
// Memory layout MUST match the C struct exactly (natural alignment, no gaps).
//
//	offset  0: TimestampMs int64    (8)
//	offset  8: Lat         float32  (4)
//	offset 12: Lon         float32  (4)
//	offset 16: Morton      uint64   (8)  — 0 = auto-compute in core_write_event
//	offset 24: EntityHash  uint32   (4)  — AIS: MMSI as uint32
//	offset 28: StreamType  uint8    (1)
//	offset 29: Flags       uint8    (1)
//	offset 30: SchemaVer   uint16   (2)
//	offset 32: Payload     [32]byte (32)
//	Total: 64 bytes
type EventRecord struct {
	TimestampMs int64
	Lat         float32
	Lon         float32
	Morton      uint64
	EntityHash  uint32
	StreamType  uint8
	Flags       uint8
	SchemaVer   uint16
	Payload     [32]byte
}

// QueryIR mirrors query_ir_t (600 bytes) in the JKDB C ABI.
// MUST match jiskta-core/src/ffi/libcore_ffi.ads C_Query_IR layout exactly.
// AIS does not currently use the Q2-* push-down filters, but the struct must
// match the Ada-side size to avoid uninitialised-memory reads.
type QueryIR struct {
	TStartMs          int64       //   0
	TEndMs            int64       //   8
	LatMin            float32     //  16
	LatMax            float32     //  20
	LonMin            float32     //  24
	LonMax            float32     //  28
	DatasetID         uint32      //  32
	StreamType        uint32      //  36
	EntityHash        uint64      //  40
	Limit             uint32      //  48
	SortDesc          uint8       //  52
	Mode              uint8       //  53
	Reserved          uint16      //  54
	OffsetRows        uint32      //  56
	EntityHashCount   uint8       //  60
	PayloadOffsetB    uint8       //  61
	PayloadOp         uint8       //  62
	Pad3              uint8       //  63
	PayloadValue      float32     //  64
	Pad4              uint32      //  68
	EntityHashList    [64]uint64  //  72
	RouteHashFilter   uint32      // 584
	VehicleTypeFilter uint8       // 588
	OperatorIDFilter  uint8       // 589
	Pad5              uint16      // 590
	Pad6              uint64      // 592 (struct = 600 bytes)
}

// Compile-time layout assertions.
var _ = [64]struct{}{}[unsafe.Sizeof(EventRecord{}) - 64]
var _ = [600]struct{}{}[unsafe.Sizeof(QueryIR{}) - 600]

// Client wraps the libcore.so JKDB C API.
type Client struct {
	dataDir string
}

// Init initialises the jiskta-core JKDB engine at dataDir.
func Init(dataDir string) (*Client, error) {
	cDir := C.CString(dataDir)
	defer C.free(unsafe.Pointer(cDir))
	if ret := C.core_init(cDir); ret < 0 {
		return nil, fmt.Errorf("core_init failed: %d", ret)
	}
	return &Client{dataDir: dataDir}, nil
}

// Close shuts down the engine. core_close is a void procedure in Ada.
func (c *Client) Close() {
	C.core_close()
}

// Flush commits all pending writes to persistent storage.
func (c *Client) Flush() error {
	if ret := C.core_flush(); ret < 0 {
		return fmt.Errorf("core_flush failed: %d", ret)
	}
	return nil
}

// WriteEvent writes a batch of 64-byte event records.
func (c *Client) WriteEvent(records []EventRecord) error {
	if len(records) == 0 {
		return nil
	}
	ret := C.core_write_event(
		(*C.event_record_t)(unsafe.Pointer(&records[0])),
		C.int(len(records)),
	)
	if int(ret) < 0 {
		return fmt.Errorf("core_write_event failed: code %d", ret)
	}
	return nil
}

// Query executes a JKDB query and returns the decoded event records.
// Returns (records, truncated, error).
func (c *Client) Query(ir QueryIR) ([]EventRecord, bool, error) {
	raw := C.core_query((*C.query_ir_t)(unsafe.Pointer(&ir)))
	if raw == nil {
		return nil, false, fmt.Errorf("core_query returned nil")
	}
	defer C.core_free_result(raw)

	n := int(raw.count)
	truncated := raw.truncated != 0
	if n == 0 || raw.records == nil {
		return nil, truncated, nil
	}

	out := make([]EventRecord, n)
	src := unsafe.Pointer(raw.records)
	for i := range out {
		out[i] = *(*EventRecord)(unsafe.Pointer(uintptr(src) + uintptr(i)*64))
	}
	return out, truncated, nil
}

// Stats returns a stub timing JSON (core_last_query_timing has an ABI mismatch;
// kept here for backward compatibility with any callers).
func (c *Client) Stats() string {
	return `{"plan_ns":0,"read_ns":0,"decode_ns":0}`
}

// Coverage returns the raw JSON string returned by core_coverage().
// Format: [{"path":"...","t_min_ms":N,"t_max_ms":N,"dataset_id":N,"is_delta":true},...]
// Returns "[]" if the store is uninitialised or has no segments.
func (c *Client) Coverage() string {
	p := C.core_coverage()
	if p == nil {
		return "[]"
	}
	return C.GoString(p)
}

// Compact compacts the closed Delta segment at deltaPath into a Morton-sorted Base
// segment at the same path with a ".base.jkdb" suffix.
// Returns a non-nil error if compaction fails.
func (c *Client) Compact(deltaPath string) error {
	cs := C.CString(deltaPath)
	defer C.free(unsafe.Pointer(cs))
	if ret := C.core_compact(cs); ret < 0 {
		return fmt.Errorf("core_compact failed: %d", ret)
	}
	return nil
}

// PackAISPayload writes AIS-specific fields into a 32-byte payload array
// using little-endian byte order (matching the JKDB event record layout).
func PackAISPayload(mmsi uint32, sog, cog, heading uint16, navStatus, msgType uint8, vesselType uint16) [32]byte {
	var p [32]byte
	binary.LittleEndian.PutUint32(p[0:4], mmsi)
	binary.LittleEndian.PutUint16(p[4:6], sog)
	binary.LittleEndian.PutUint16(p[6:8], cog)
	binary.LittleEndian.PutUint16(p[8:10], heading)
	p[10] = navStatus
	p[11] = msgType
	binary.LittleEndian.PutUint16(p[12:14], vesselType)
	return p
}

// DecodeAISPayload reads AIS-specific fields from a 32-byte payload array.
func DecodeAISPayload(p [32]byte) (mmsi uint32, sog, cog, heading uint16, navStatus, msgType uint8, vesselType uint16) {
	mmsi = binary.LittleEndian.Uint32(p[0:4])
	sog = binary.LittleEndian.Uint16(p[4:6])
	cog = binary.LittleEndian.Uint16(p[6:8])
	heading = binary.LittleEndian.Uint16(p[8:10])
	navStatus = p[10]
	msgType = p[11]
	vesselType = binary.LittleEndian.Uint16(p[12:14])
	return
}
