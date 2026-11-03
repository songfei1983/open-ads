package internalapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/observability"
)

// ErrIdempotencyKeyReuse reports a key that was already used for a different
// request body: replaying it could hide a second, different mutation.
var ErrIdempotencyKeyReuse = errors.New("idempotency key reused with a different request")

// idempotencyTTL bounds how long a recorded response can be replayed.
const idempotencyTTL = 24 * time.Hour

// IdempotencyRecord is one stored response keyed by caller, route and key.
type IdempotencyRecord struct {
	Key         string
	Route       string
	RequestHash string
	Status      int
	Body        string
	RecordedAt  time.Time
}

// IdempotencyStore persists idempotency keys for exactly one tenant.
type IdempotencyStore struct {
	conn     clickhouse.Conn
	tenantID uuid.UUID
	now      func() time.Time
}

// NewIdempotencyStore binds the store to one tenant.
func NewIdempotencyStore(conn clickhouse.Conn, tenantID uuid.UUID) *IdempotencyStore {
	return &IdempotencyStore{conn: conn, tenantID: tenantID, now: func() time.Time { return time.Now().UTC() }}
}

// Lookup returns the newest record for key and route.
func (s *IdempotencyStore) Lookup(ctx context.Context, key, route string) (IdempotencyRecord, bool, error) {
	rows, err := s.conn.Query(ctx, `SELECT idempotency_key, route, request_hash, response_status, response_body, recorded_at
		FROM dmp_idempotency WHERE tenant_id = ? AND idempotency_key = ? AND route = ?
		ORDER BY recorded_at DESC LIMIT 1`, s.tenantID, key, route)
	if err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return IdempotencyRecord{}, false, rows.Err()
	}
	var (
		record IdempotencyRecord
		status uint16
	)
	if err := rows.Scan(&record.Key, &record.Route, &record.RequestHash, &status, &record.Body, &record.RecordedAt); err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("scan idempotency key: %w", err)
	}
	record.Status = int(status)
	return record, true, nil
}

// Save records the response for a key.
func (s *IdempotencyStore) Save(ctx context.Context, record IdempotencyRecord) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO dmp_idempotency (tenant_id, idempotency_key, route,
		request_hash, response_status, response_body, recorded_at)`)
	if err != nil {
		return fmt.Errorf("prepare idempotency insert: %w", err)
	}
	if err := batch.Append(s.tenantID, record.Key, record.Route, record.RequestHash,
		uint16(record.Status), record.Body, s.now()); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("append idempotency row: %w", err)
	}
	return batch.Send()
}

// maxIdempotentBody bounds the body kept for replay.
const maxIdempotentBody = 8 << 20

// readBody reads and restores the request body so the wrapped handler still sees
// it.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotentBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxIdempotentBody {
		return nil, errors.New("request body is too large")
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body, nil
}

// hashBody fingerprints the request so a replayed key can be refused when the
// payload differs.
func hashBody(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// recordingWriter captures the handler response so it can be replayed.
type recordingWriter struct {
	header http.Header
	status int
	body   []byte
}

func (w *recordingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *recordingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *recordingWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body = append(w.body, data...)
	return len(data), nil
}

// withIdempotency replays a stored response for a repeated Idempotency-Key.
func (h *handler) withIdempotency(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			next(w, r)
			return
		}
		requestID := observability.RequestIDFromContext(r.Context())
		if len(key) > 128 {
			writeError(w, requestID, http.StatusBadRequest, "invalid_request", "Idempotency-Key must be at most 128 characters")
			return
		}
		if h.idempotency == nil {
			writeError(w, requestID, http.StatusServiceUnavailable, "internal_api_unconfigured", "idempotency keys are not configured")
			return
		}

		body, replay, err := h.replayIdempotent(w, r, route, key)
		if err != nil || replay {
			return
		}

		recorder := &recordingWriter{}
		next(recorder, r)
		for name, values := range recorder.header {
			w.Header()[name] = values
		}
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		w.WriteHeader(recorder.status)
		if _, err := w.Write(recorder.body); err != nil {
			return
		}
		// Only successful answers are replayable: a failure must be retryable.
		if recorder.status >= http.StatusOK && recorder.status < http.StatusMultipleChoices {
			if err := h.idempotency.Save(r.Context(), IdempotencyRecord{
				Key: key, Route: route, RequestHash: body, Status: recorder.status, Body: string(recorder.body),
			}); err != nil {
				h.logger.Error("DMP idempotency record failed", slog.String("request_id", requestID), slog.Any("error", err))
			}
		}
	}
}

// replayIdempotent returns the request fingerprint and whether it answered.
func (h *handler) replayIdempotent(w http.ResponseWriter, r *http.Request, route, key string) (string, bool, error) {
	requestID := observability.RequestIDFromContext(r.Context())
	body, err := readBody(r)
	if err != nil {
		writeError(w, requestID, http.StatusBadRequest, "invalid_request", "request body is required")
		return "", true, err
	}
	hash := hashBody(body)

	record, found, err := h.idempotency.Lookup(r.Context(), key, route)
	if err != nil {
		h.logger.Error("DMP idempotency lookup failed", slog.String("request_id", requestID), slog.Any("error", err))
		writeError(w, requestID, http.StatusServiceUnavailable, "idempotency_unavailable", "idempotency state is unavailable")
		return "", true, err
	}
	if !found {
		return hash, false, nil
	}
	if record.RequestHash != hash {
		writeError(w, requestID, http.StatusConflict, "idempotency_key_reuse", ErrIdempotencyKeyReuse.Error())
		return "", true, nil
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(record.Status)
	_, _ = w.Write([]byte(record.Body))
	return "", true, nil
}
