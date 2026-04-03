package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// KeyInfo holds the validated key data returned by Supabase.
type KeyInfo struct {
	APIKEYID       string
	UserEmail      string
	CreditBalance  int64
	IsActive       bool
}

const (
	validTTL   = 60 * time.Second
	invalidTTL = 5 * time.Second
)

type cacheEntry struct {
	ki       *KeyInfo
	err      error
	cachedAt time.Time
}

// SupabaseAuth validates X-API-Key headers against Supabase RPC.
// Same 60s cache pattern as api/climate_server.go.
type SupabaseAuth struct {
	supabaseURL  string
	serviceKey   string
	client       http.Client
	cache        sync.Map // keyHash (string) -> *cacheEntry
}

// NewSupabaseAuth creates an auth cache client.
func NewSupabaseAuth(supabaseURL, serviceKey string) *SupabaseAuth {
	return &SupabaseAuth{
		supabaseURL: supabaseURL,
		serviceKey:  serviceKey,
		client:      http.Client{Timeout: 5 * time.Second},
	}
}

// ValidateKey checks the raw API key (from X-API-Key header) against Supabase.
// Returns nil error on success; non-nil on invalid / expired / no credits.
func (a *SupabaseAuth) ValidateKey(rawKey string) (*KeyInfo, error) {
	// Use raw key as cache key (hash computed server-side by Supabase RPC).
	if entry, ok := a.cache.Load(rawKey); ok {
		e := entry.(*cacheEntry)
		ttl := validTTL
		if e.err != nil {
			ttl = invalidTTL
		}
		if time.Since(e.cachedAt) < ttl {
			return e.ki, e.err
		}
	}

	ki, err := a.fetchFromSupabase(rawKey)
	a.cache.Store(rawKey, &cacheEntry{ki: ki, err: err, cachedAt: time.Now()})
	return ki, err
}

type validateReq struct {
	PKEYHash string `json:"p_key_hash"`
}

type validateResp struct {
	APIKEYID      string `json:"api_key_id"`
	UserEmail     string `json:"user_email"`
	CreditBalance int64  `json:"credit_balance"`
	IsActive      bool   `json:"is_active"`
}

func (a *SupabaseAuth) fetchFromSupabase(rawKey string) (*KeyInfo, error) {
	body, _ := json.Marshal(validateReq{PKEYHash: rawKey})
	req, err := http.NewRequest("POST",
		a.supabaseURL+"/rest/v1/rpc/validate_and_check_key",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", a.serviceKey)
	req.Header.Set("Authorization", "Bearer "+a.serviceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid API key (HTTP %d)", resp.StatusCode)
	}

	var r validateResp
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("supabase response parse: %w", err)
	}
	if !r.IsActive {
		return nil, fmt.Errorf("API key is inactive")
	}
	if r.CreditBalance <= 0 {
		return nil, fmt.Errorf("insufficient credits")
	}
	return &KeyInfo{
		APIKEYID:      r.APIKEYID,
		UserEmail:     r.UserEmail,
		CreditBalance: r.CreditBalance,
		IsActive:      r.IsActive,
	}, nil
}

// Invalidate removes a cached entry (e.g. after credit deduction failure).
func (a *SupabaseAuth) Invalidate(rawKey string) {
	a.cache.Delete(rawKey)
}

// DeductCredits asynchronously deducts credits via Supabase RPC.
// Does not block the HTTP response path.
// apiKeyID: UUID from KeyInfo.APIKEYID
// cost: number of credits to deduct
// rawKey: used to refresh the cached balance afterwards
func (a *SupabaseAuth) DeductCredits(apiKeyID string, cost int64, rawKey string) {
	go func() {
		type deductReq struct {
			PKEYID   string `json:"p_api_key_id"`
			PCredits int64  `json:"p_credits_used"`
			PTiles   int    `json:"p_tiles_scanned"`
			PQueryMs int    `json:"p_query_time_ms"`
			PParams  string `json:"p_query_params"`
		}
		body, _ := json.Marshal(deductReq{
			PKEYID:   apiKeyID,
			PCredits: cost,
			PTiles:   0,
			PQueryMs: 0,
			PParams:  `{"service":"ais"}`,
		})
		req, err := http.NewRequest("POST",
			a.supabaseURL+"/rest/v1/rpc/deduct_credits",
			bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("apikey", a.serviceKey)
		req.Header.Set("Authorization", "Bearer "+a.serviceKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := a.client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()

		// Refresh the cache entry so the next request sees the new balance.
		if resp.StatusCode == 200 {
			a.cache.Delete(rawKey) // force re-fetch on next request
		}
	}()
}

// UpdateCachedBalance subtracts cost from the cached credit balance optimistically.
// Call this before responding so subsequent rapid requests don't over-spend.
func (a *SupabaseAuth) UpdateCachedBalance(rawKey string, cost int64) {
	if v, ok := a.cache.Load(rawKey); ok {
		e := v.(*cacheEntry)
		if e.ki != nil {
			newKI := *e.ki
			newKI.CreditBalance -= cost
			a.cache.Store(rawKey, &cacheEntry{
				ki:       &newKI,
				err:      e.err,
				cachedAt: time.Now(),
			})
		}
	}
}
