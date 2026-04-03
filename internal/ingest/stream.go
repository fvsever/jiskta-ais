// AIS stream client for aisstream.io.
// Connects via WebSocket, subscribes to the global feed (or a configured bbox),
// and pushes raw messages to a channel.
// Reconnects with exponential backoff on disconnect or error.

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const aisstreamURL = "wss://stream.aisstream.io/v0/stream"

// subscription is sent to aisstream.io on connect to select message types
// and geographic scope.
type subscription struct {
	APIKey         string        `json:"APIKey"`
	BoundingBoxes  [][][2]float64 `json:"BoundingBoxes"`  // [[[lat_min,lon_min],[lat_max,lon_max]], ...]
	FiltersShipMMSI []string      `json:"FiltersShipMMSI,omitempty"`
	FilterMessageTypes []string   `json:"FilterMessageTypes,omitempty"`
}

// RawMessage is one envelope from aisstream.io.
type RawMessage struct {
	MessageType string          `json:"MessageType"`
	MetaData    json.RawMessage `json:"MetaData"`
	Message     json.RawMessage `json:"Message"`
}

// StreamConfig configures the WebSocket client.
type StreamConfig struct {
	APIKey        string
	BoundingBoxes [][][2]float64 // nil = global (whole world)
	MessageTypes  []string       // nil = all position types
	MaxReconnects int            // 0 = unlimited
}

// globalBBox is the whole-world subscription — used when BoundingBoxes is nil.
var globalBBox = [][][2]float64{
	{{-90, -180}, {90, 180}},
}

// StreamClient connects to aisstream.io and emits raw messages.
type StreamClient struct {
	cfg StreamConfig
	out chan<- RawMessage
}

// NewStreamClient creates a client that will write messages to out.
func NewStreamClient(cfg StreamConfig, out chan<- RawMessage) *StreamClient {
	if cfg.BoundingBoxes == nil {
		cfg.BoundingBoxes = globalBBox
	}
	if cfg.MessageTypes == nil {
		cfg.MessageTypes = []string{
			"PositionReport",
			"StandardClassBPositionReport",
			"AidToNavigationReport",
			"StaticAndVoyageRelatedData",
		}
	}
	if cfg.MaxReconnects == 0 {
		cfg.MaxReconnects = -1 // unlimited
	}
	return &StreamClient{cfg: cfg, out: out}
}

// Run connects and streams messages until ctx is cancelled.
// Reconnects automatically with exponential backoff.
func (sc *StreamClient) Run(ctx context.Context) {
	attempts := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := sc.connect(ctx)
		if err != nil && ctx.Err() == nil {
			attempts++
			if sc.cfg.MaxReconnects > 0 && attempts >= sc.cfg.MaxReconnects {
				log.Printf("[ais-stream] max reconnects (%d) reached: %v", sc.cfg.MaxReconnects, err)
				return
			}
			backoff := backoffDuration(attempts)
			log.Printf("[ais-stream] disconnected (attempt %d), reconnect in %s: %v",
				attempts, backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}
}

func (sc *StreamClient) connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, aisstreamURL, http.Header{})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("ws dial: HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	log.Printf("[ais-stream] connected to %s", aisstreamURL)

	sub := subscription{
		APIKey:             sc.cfg.APIKey,
		BoundingBoxes:      sc.cfg.BoundingBoxes,
		FilterMessageTypes: sc.cfg.MessageTypes,
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var msg RawMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // skip unparseable frames
		}

		select {
		case sc.out <- msg:
		case <-ctx.Done():
			return nil
		default:
			// drop if consumer is too slow (prevents backpressure kill)
		}
	}
}

func backoffDuration(attempt int) time.Duration {
	const maxBackoff = 60 * time.Second
	d := time.Duration(math.Pow(2, float64(attempt))) * 500 * time.Millisecond
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}
