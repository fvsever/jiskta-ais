package ingest

// pipeline.go — AISStream ties together StreamClient, decoder, cleaner, and store.
// This is the top-level component used by cmd/ais-server/main.go.

import (
	"context"
	"log"
	"time"

	"github.com/fvsever/jiskta-ais/internal/store"
)

// Storer is the interface required by the ingest pipeline — satisfied by *store.CoreClient.
type Storer interface {
	WriteAIS([]store.AISRecord) error
}

// AISStream manages the full ingest pipeline: WebSocket → decode → clean → write.
type AISStream struct {
	client  *StreamClient
	cleaner *Cleaner
	storer  Storer
	rawCh   chan RawMessage
	cancel  context.CancelFunc

	// vessel-type cache for type 5 messages
	vtCache map[uint32]uint16

	// batch config
	batchSize    int
	flushEvery   time.Duration
}

// NewAISStream creates a ready-to-run ingest pipeline.
// dataDir is only used for the store; all other state is in-memory.
func NewAISStream(apiKey string, storer Storer, cleaner *Cleaner) *AISStream {
	rawCh := make(chan RawMessage, 4096)
	cfg := StreamConfig{APIKey: apiKey}
	return &AISStream{
		client:     NewStreamClient(cfg, rawCh),
		cleaner:    cleaner,
		storer:     storer,
		rawCh:      rawCh,
		vtCache:    make(map[uint32]uint16),
		batchSize:  512,
		flushEvery: 100 * time.Millisecond,
	}
}

// Run starts the pipeline and blocks until ctx is cancelled.
func (a *AISStream) Run(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	go a.client.Run(ctx)
	a.processBatches(ctx)
	return ctx.Err()
}

// Stop gracefully halts the pipeline.
func (a *AISStream) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *AISStream) processBatches(ctx context.Context) {
	ticker := time.NewTicker(a.flushEvery)
	defer ticker.Stop()

	batch := make([]store.AISRecord, 0, a.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := a.storer.WriteAIS(batch); err != nil {
			log.Printf("[ais-ingest] write error: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return

		case <-ticker.C:
			flush()

		case raw, ok := <-a.rawCh:
			if !ok {
				flush()
				return
			}

			// Convert RawMessage to AISStreamMessage (same structure, different type name)
			msg := &AISStreamMessage{
				MessageType: raw.MessageType,
				Message:     raw.Message,
			}

			pos, err := DecodeMessage(msg, a.vtCache)
			if err != nil || pos == nil {
				continue
			}

			now := time.Now()
			if !a.cleaner.Accept(pos, now) {
				continue
			}

			rec := store.AISRecord{
				Timestamp:     now.UnixMilli(),
				Lat:           pos.Lat,
				Lon:           pos.Lon,
				StreamType:    uint8(store.StreamAIS),
				SchemaVersion: 1,
				MMSI:          pos.MMSI,
				SOG:           pos.SOG,
				COG:           pos.COG,
				Heading:       pos.Heading,
				NavStatus:     pos.NavStatus,
				MsgType:       pos.MsgType,
				VesselType:    pos.VesselType,
			}
			batch = append(batch, rec)

			if len(batch) >= a.batchSize {
				flush()
			}
		}
	}
}
