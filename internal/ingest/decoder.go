package ingest

import (
	"encoding/json"
	"fmt"
)

// AISStreamMessage is the top-level wrapper from aisstream.io.
type AISStreamMessage struct {
	MessageType string          `json:"MessageType"`
	Message     json.RawMessage `json:"Message"`
	MetaData    MetaData        `json:"MetaData"`
}

type MetaData struct {
	MMSI        uint32  `json:"MMSI"`
	TimeReceived string  `json:"time_utc"` // "2024-01-15 12:34:56.789"
	Latitude    float32 `json:"latitude"`
	Longitude   float32 `json:"longitude"`
}

// PositionReport covers AIS message types 1, 2, 3 (Class A).
type PositionReport struct {
	UserID         uint32  `json:"UserID"`
	Latitude       float32 `json:"Latitude"`
	Longitude      float32 `json:"Longitude"`
	Sog            float32 `json:"Sog"` // knots
	Cog            float32 `json:"Cog"` // degrees
	TrueHeading    int     `json:"TrueHeading"`
	NavigationalStatus int `json:"NavigationalStatus"`
	MessageID      int     `json:"MessageID"`
}

// ClassBPositionReport covers AIS message type 18.
type ClassBPositionReport struct {
	UserID      uint32  `json:"UserID"`
	Latitude    float32 `json:"Latitude"`
	Longitude   float32 `json:"Longitude"`
	Sog         float32 `json:"Sog"`
	Cog         float32 `json:"Cog"`
	TrueHeading int     `json:"TrueHeading"`
	MessageID   int     `json:"MessageID"`
}

// AidToNavReport covers AIS message type 21.
type AidToNavReport struct {
	MMSI      uint32  `json:"UserID"`
	Latitude  float32 `json:"Latitude"`
	Longitude float32 `json:"Longitude"`
	TypeOfAid int     `json:"TypeOfAid"`
	MessageID int     `json:"MessageID"`
}

// StaticVoyageData covers AIS message type 5 (vessel name, type, etc.)
type StaticVoyageData struct {
	UserID     uint32 `json:"UserID"`
	VesselName string `json:"VesselName"`
	ShipType   int    `json:"ShipType"`
	CallSign   string `json:"CallSign"`
	MessageID  int    `json:"MessageID"`
}

// DecodedPosition is the normalised representation after parsing any position message.
// All fields are in SI / standard units.
type DecodedPosition struct {
	MMSI       uint32
	Lat        float32
	Lon        float32
	SOG        uint16  // tenths of a knot (0–1022; 1023 = not available)
	COG        uint16  // tenths of a degree (0–3599)
	Heading    uint16  // degrees true (0–359; 511 = not available)
	NavStatus  uint8   // AIS navigation status code
	MsgType    uint8   // AIS message type: 1,2,3,18,21
	VesselType uint16  // from type 5 cache (best-effort, 0 if unknown)
}

// navStatusMap converts aisstream.io integer codes to our stored uint8.
// AIS spec ITU-R M.1371-5 table 22.
func navStatusUint8(code int) uint8 {
	if code < 0 || code > 15 {
		return 15 // unknown
	}
	return uint8(code)
}

// sogToTenths converts float knots to tenths-of-a-knot uint16.
// Values > 102.2 are clamped to 1022 (the AIS maximum valid SOG).
func sogToTenths(sog float32) uint16 {
	if sog < 0 {
		return 0
	}
	t := uint16(sog * 10)
	if t > 1022 {
		return 1022
	}
	return t
}

// cogToTenths converts float degrees to tenths-of-a-degree uint16.
func cogToTenths(cog float32) uint16 {
	for cog < 0 {
		cog += 360
	}
	for cog >= 360 {
		cog -= 360
	}
	return uint16(cog * 10)
}

// headingUint16 converts an int heading to uint16, clamping to 0–359.
// Returns 511 for "not available" (AIS convention).
func headingUint16(h int) uint16 {
	if h < 0 || h > 359 {
		return 511
	}
	return uint16(h)
}

// DecodeMessage parses an AISStreamMessage and returns a DecodedPosition.
// Returns an error for unsupported message types or malformed payloads.
// vesselTypeCache: optional caller-supplied map[mmsi]vesselType (may be nil).
func DecodeMessage(raw *AISStreamMessage, vesselTypeCache map[uint32]uint16) (*DecodedPosition, error) {
	switch raw.MessageType {
	case "PositionReport":
		var m PositionReport
		if err := json.Unmarshal(raw.Message, &m); err != nil {
			return nil, fmt.Errorf("decode PositionReport: %w", err)
		}
		vtype := uint16(0)
		if vesselTypeCache != nil {
			vtype = vesselTypeCache[m.UserID]
		}
		return &DecodedPosition{
			MMSI:       m.UserID,
			Lat:        m.Latitude,
			Lon:        m.Longitude,
			SOG:        sogToTenths(m.Sog),
			COG:        cogToTenths(m.Cog),
			Heading:    headingUint16(m.TrueHeading),
			NavStatus:  navStatusUint8(m.NavigationalStatus),
			MsgType:    uint8(m.MessageID),
			VesselType: vtype,
		}, nil

	case "StandardClassBPositionReport":
		var m ClassBPositionReport
		if err := json.Unmarshal(raw.Message, &m); err != nil {
			return nil, fmt.Errorf("decode ClassBPositionReport: %w", err)
		}
		vtype := uint16(0)
		if vesselTypeCache != nil {
			vtype = vesselTypeCache[m.UserID]
		}
		return &DecodedPosition{
			MMSI:       m.UserID,
			Lat:        m.Latitude,
			Lon:        m.Longitude,
			SOG:        sogToTenths(m.Sog),
			COG:        cogToTenths(m.Cog),
			Heading:    headingUint16(m.TrueHeading),
			NavStatus:  15, // Class B has no navigational status field
			MsgType:    18,
			VesselType: vtype,
		}, nil

	case "AidToNavigationReport":
		var m AidToNavReport
		if err := json.Unmarshal(raw.Message, &m); err != nil {
			return nil, fmt.Errorf("decode AidToNavReport: %w", err)
		}
		return &DecodedPosition{
			MMSI:    m.MMSI,
			Lat:     m.Latitude,
			Lon:     m.Longitude,
			MsgType: 21,
		}, nil

	case "StaticAndVoyageRelatedData":
		// Type 5: update vessel type cache, do not produce a position record.
		var m StaticVoyageData
		if err := json.Unmarshal(raw.Message, &m); err != nil {
			return nil, fmt.Errorf("decode StaticVoyageData: %w", err)
		}
		if vesselTypeCache != nil && m.ShipType >= 0 {
			vesselTypeCache[m.UserID] = uint16(m.ShipType)
		}
		return nil, nil // caller should skip nil result

	default:
		return nil, fmt.Errorf("unsupported message type: %s", raw.MessageType)
	}
}
