# jiskta-ais

Real-time AIS (Automatic Identification System) vessel tracking service.

**Status: Research / MVP in development. NOT production.**

## Architecture

```
aisstream.io WebSocket
      │
      ▼
ingest.AISStream (pipeline.go)
      │  decode + clean + batch
      ▼
store.CoreClient (CGo → jiskta-core/bin/libcore.so)
      │  append-log, geohash index
      ▼
/data/ais/*.jkst1  (JKST1 binary format)
      │
      ▼
HTTP API (port 8081)
  GET /api/v1/ais/query      bbox + time range
  GET /api/v1/ais/vessel/:mmsi  vessel track
  GET /api/v1/ais/live       SSE real-time stream
  GET /api/v1/ais/coverage   available date ranges
```

## Prerequisites

1. Build `jiskta-core`:

```bash
cd ../jiskta-core
gprbuild -P jiskta_core.gpr
# → produces bin/libcore.so
```

2. Set environment variables:

```bash
export SUPABASE_URL=https://your-project.supabase.co
export SUPABASE_SERVICE_KEY=eyJ...
export AISSTREAM_API_KEY=your-aisstream-key   # optional; omit for API-only mode
export AIS_DATA_DIR=/data/ais                  # default: /data/ais
export PORT=8081                               # default: 8081
```

## Build

```bash
cd jiskta-ais
CGO_ENABLED=1 \
CGO_LDFLAGS="-L../jiskta-core/bin -lcore -Wl,-rpath,../jiskta-core/bin" \
go build -o bin/ais-server ./cmd/ais-server
```

## Run

```bash
./bin/ais-server
```

## Credit model

| Endpoint | Cost |
|----------|------|
| `GET /api/v1/ais/query` | `ceil(records_returned / 1000)`, minimum 1 |
| `GET /api/v1/ais/vessel/{mmsi}` | 1 credit per track query |
| `GET /api/v1/ais/live` | 0 (free) |
| `GET /api/v1/ais/coverage` | 0 (free) |

Pass `dry_run=true` to `/api/v1/ais/query` to get the estimated cost without executing the query.

All credits use the same Supabase `deduct_credits` RPC as the climate API. Deduction is
async (fires after the HTTP response is written) and the in-process cache is updated
optimistically to prevent double-spending.

## Data cleaning rules

- MMSI: must be 1–999,999,999
- Coordinates: lat ∈ [-90, 90], lon ∈ [-180, 180]
- SOG: max 102.2 knots (AIS maximum valid)
- Dedup: same MMSI + identical position within 2 seconds → dropped

## Notes

- Port 8081 (climate API uses 8080 — must not conflict)
- Auth: same `X-API-Key` header + Supabase as climate API
- `jiskta-core` is a research project — **do not use this service in production** before jiskta-core is hardened

---

## Deploy (example — Linux/systemd)

```bash
# 1. Install binaries
sudo mkdir -p /opt/jiskta-ais/bin /opt/jiskta-ais/lib /data/ais
sudo cp ../jiskta-core/bin/libcore.so /opt/jiskta-ais/lib/
sudo cp bin/ais-server /opt/jiskta-ais/bin/

# 2. Create env file
sudo tee /opt/jiskta-ais/.env <<'ENV'
PORT=8081
AIS_DATA_DIR=/data/ais
CORE_LIB_PATH=/opt/jiskta-ais/lib/libcore.so
SUPABASE_URL=https://ectmafeuxxcsdsxlfumz.supabase.co
SUPABASE_SERVICE_KEY=eyJ...
AISSTREAM_API_KEY=your_aisstream_key_here
ENV

# 3. Install systemd unit
sudo tee /etc/systemd/system/jiskta-ais.service <<'UNIT'
[Unit]
Description=Jiskta AIS Server
After=network.target

[Service]
Type=simple
User=ubuntu
EnvironmentFile=/opt/jiskta-ais/.env
WorkingDirectory=/opt/jiskta-ais
ExecStart=/opt/jiskta-ais/bin/ais-server
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536
MemoryHigh=4G
MemoryMax=8G

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable --now jiskta-ais
journalctl -u jiskta-ais -f
```

**Caddy reverse proxy** (add to `/etc/caddy/Caddyfile`):
```
ais.jiskta.com {
    reverse_proxy localhost:8081
}
```
