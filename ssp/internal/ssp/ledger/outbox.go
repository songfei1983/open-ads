package ledger

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Outbox statuses.
const (
	OutboxPending     = "pending"
	OutboxSending     = "sending"
	OutboxDelivered   = "delivered"
	OutboxQuarantined = "quarantined"
	OutboxRejected    = "rejected"
)

// OutboxItem is one due billable notice.
type OutboxItem struct {
	EventID        string
	SourceEventKey string
	DSPID          string
	AuctionID      string
	BidID          string
	ImpID          string
	ReservationRef string
	BURLTarget     string
	AmountMicros   int64
	Currency       string
	OccurredAt     time.Time
	AttemptCount   uint32
}

// OutboxView is the operator-facing projection.
type OutboxView struct {
	EventID        string `json:"event_id"`
	SourceEventKey string `json:"source_event_key"`
	DSPID          string `json:"dsp_id"`
	AuctionID      string `json:"auction_id"`
	BidID          string `json:"bid_id"`
	ImpID          string `json:"imp_id"`
	AmountMicros   int64  `json:"amount_micros"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	AttemptCount   uint32 `json:"attempt_count"`
	NextAttemptAt  string `json:"next_attempt_at"`
	LastHTTPStatus int    `json:"last_http_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	TargetHost     string `json:"target_host"`
	ResolvedAt     string `json:"resolved_at,omitempty"`
	ResolutionText string `json:"resolution_reason,omitempty"`
}

// ClaimDue reserves up to limit due outbox rows for this sender. Claimed rows
// are marked "sending" and re-eligible after the lease, so a crashed sender
// cannot strand them.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]OutboxItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT event_id, source_event_key, dsp_id, auction_id, bid_id, imp_id,
		reservation_ref, burl_target_url, amount_micros, currency, occurred_at, attempt_count
		FROM ssp_billable_outbox
		WHERE tenant_id = ? AND published_at IS NULL AND status IN ('pending', 'sending') AND next_attempt_at <= ?
		ORDER BY next_attempt_at LIMIT ? FOR UPDATE SKIP LOCKED`, s.tenant, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("select due outbox rows: %w", err)
	}
	items := []OutboxItem{}
	for rows.Next() {
		var item OutboxItem
		var eventID, sourceKey, dspID, reservation []byte
		if err := rows.Scan(&eventID, &sourceKey, &dspID, &item.AuctionID, &item.BidID, &item.ImpID,
			&reservation, &item.BURLTarget, &item.AmountMicros, &item.Currency, &item.OccurredAt,
			&item.AttemptCount); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan due outbox row: %w", err)
		}
		item.EventID = hex.EncodeToString(eventID)
		item.SourceEventKey = hex.EncodeToString(sourceKey)
		item.DSPID = hex.EncodeToString(dspID)
		item.ReservationRef = string(reservation)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, item := range items {
		eventBytes, ok := encodeID(item.EventID)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ssp_billable_outbox
			SET status = 'sending', attempt_count = attempt_count + 1, next_attempt_at = ?
			WHERE tenant_id = ? AND event_id = ?`, now.UTC().Add(lease), s.tenant, eventBytes); err != nil {
			return nil, fmt.Errorf("claim outbox row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return items, nil
}

// MarkDelivered records a confirmed DSP settlement.
func (s *Store) MarkDelivered(ctx context.Context, eventID string, httpStatus int) error {
	return s.mutateOutbox(ctx, eventID, `UPDATE ssp_billable_outbox
		SET status = 'delivered', published_at = ?, last_http_status = ?, last_error_sanitized = NULL
		WHERE tenant_id = ? AND event_id = ?`, time.Now().UTC(), httpStatus, s.tenant)
}

// MarkRetry schedules another attempt with a bounded backoff.
func (s *Store) MarkRetry(ctx context.Context, eventID string, httpStatus int, sanitized string, next time.Time) error {
	return s.mutateOutbox(ctx, eventID, `UPDATE ssp_billable_outbox
		SET status = 'pending', last_http_status = ?, last_error_sanitized = ?, next_attempt_at = ?
		WHERE tenant_id = ? AND event_id = ?`, httpStatus, sanitized, next.UTC(), s.tenant)
}

// MarkQuarantined parks a row for operator disposition.
func (s *Store) MarkQuarantined(ctx context.Context, eventID string, httpStatus int, sanitized string) error {
	return s.mutateOutbox(ctx, eventID, `UPDATE ssp_billable_outbox
		SET status = 'quarantined', last_http_status = ?, last_error_sanitized = ?
		WHERE tenant_id = ? AND event_id = ?`, httpStatus, sanitized, s.tenant)
}

// ListOutbox returns outbox rows, optionally filtered by status.
func (s *Store) ListOutbox(ctx context.Context, status string, limit int) ([]OutboxView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT event_id, source_event_key, dsp_id, auction_id, bid_id, imp_id, amount_micros,
		currency, status, attempt_count, next_attempt_at, COALESCE(last_http_status, 0),
		COALESCE(last_error_sanitized, ''), burl_target_url, resolved_at, resolution_reason
		FROM ssp_billable_outbox WHERE tenant_id = ?`
	args := []any{s.tenant}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	query += " ORDER BY next_attempt_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list billable outbox: %w", err)
	}
	defer rows.Close()

	items := []OutboxView{}
	for rows.Next() {
		var view OutboxView
		var eventID, sourceKey, dspID []byte
		var nextAttempt time.Time
		var resolvedAt sql.NullTime
		var resolutionReason sql.NullString
		var target string
		if err := rows.Scan(&eventID, &sourceKey, &dspID, &view.AuctionID, &view.BidID, &view.ImpID,
			&view.AmountMicros, &view.Currency, &view.Status, &view.AttemptCount, &nextAttempt,
			&view.LastHTTPStatus, &view.LastError, &target, &resolvedAt, &resolutionReason); err != nil {
			return nil, fmt.Errorf("scan billable outbox: %w", err)
		}
		view.EventID = hex.EncodeToString(eventID)
		view.SourceEventKey = hex.EncodeToString(sourceKey)
		view.DSPID = hex.EncodeToString(dspID)
		view.NextAttemptAt = nextAttempt.UTC().Format(time.RFC3339)
		view.TargetHost = targetHost(target)
		if resolvedAt.Valid {
			view.ResolvedAt = resolvedAt.Time.UTC().Format(time.RFC3339)
		}
		view.ResolutionText = resolutionReason.String
		items = append(items, view)
	}
	return items, rows.Err()
}

// ResolveOutbox applies an operator decision to a quarantined row.
func (s *Store) ResolveOutbox(ctx context.Context, eventID string, accept bool, reason string) error {
	eventBytes, ok := encodeID(eventID)
	if !ok {
		return fmt.Errorf("%w: event_id must be a 32-hex UUID", ErrEntryNotFound)
	}
	if reason == "" {
		return fmt.Errorf("resolution reason is required")
	}
	status := OutboxRejected
	var published any
	if accept {
		status = OutboxDelivered
		published = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ssp_billable_outbox
		SET status = ?, resolved_at = ?, resolution_reason = ?, published_at = COALESCE(?, published_at)
		WHERE tenant_id = ? AND event_id = ? AND status = 'quarantined'`,
		status, time.Now().UTC(), reason, published, s.tenant, eventBytes)
	if err != nil {
		return fmt.Errorf("resolve billable outbox: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrEntryNotFound
	}
	return nil
}

func (s *Store) mutateOutbox(ctx context.Context, eventID, query string, args ...any) error {
	eventBytes, ok := encodeID(eventID)
	if !ok {
		return fmt.Errorf("event_id must be a 32-hex UUID")
	}
	args = append(args, eventBytes)
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("update billable outbox: %w", err)
	}
	return nil
}

// OutboxStore is the persistence the sender needs. *Store implements it.
type OutboxStore interface {
	ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]OutboxItem, error)
	MarkDelivered(ctx context.Context, eventID string, httpStatus int) error
	MarkRetry(ctx context.Context, eventID string, httpStatus int, sanitized string, next time.Time) error
	MarkQuarantined(ctx context.Context, eventID string, httpStatus int, sanitized string) error
}

// Sender delivers due billable notices to the DSP callback.
type Sender struct {
	Store       OutboxStore
	Client      *http.Client
	Logger      *slog.Logger
	Now         func() time.Time
	MaxAttempts uint32
}

// Drain claims and delivers one batch, returning how many notices were sent.
func (s *Sender) Drain(ctx context.Context) (int, error) {
	if s.Store == nil {
		return 0, errors.New("billable outbox store is not configured")
	}
	now := s.now()
	items, err := s.Store.ClaimDue(ctx, now, 20, 60*time.Second)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, item := range items {
		status, sendErr := s.send(ctx, item)
		switch {
		case sendErr == nil && status >= 200 && status < 300:
			if err := s.Store.MarkDelivered(ctx, item.EventID, status); err != nil {
				s.log().Warn("failed to mark billable notice delivered", slog.String("event_id", item.EventID), slog.Any("error", err))
			}
			sent++
		case status == http.StatusTooManyRequests || status >= 500 || sendErr != nil:
			if item.AttemptCount >= s.maxAttempts() {
				if err := s.Store.MarkQuarantined(ctx, item.EventID, status, "retry_budget_exhausted"); err != nil {
					s.log().Warn("failed to quarantine billable notice", slog.String("event_id", item.EventID), slog.Any("error", err))
				}
				continue
			}
			if err := s.Store.MarkRetry(ctx, item.EventID, status, "transport_error", now.Add(backoff(item.AttemptCount))); err != nil {
				s.log().Warn("failed to reschedule billable notice", slog.String("event_id", item.EventID), slog.Any("error", err))
			}
		default:
			// Any other 4xx is a permanent rejection; retrying cannot help.
			if err := s.Store.MarkQuarantined(ctx, item.EventID, status, fmt.Sprintf("http_%d", status)); err != nil {
				s.log().Warn("failed to quarantine billable notice", slog.String("event_id", item.EventID), slog.Any("error", err))
			}
		}
	}
	return sent, nil
}

// Run polls the outbox until the context is cancelled.
func (s *Sender) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.Drain(ctx); err != nil && ctx.Err() == nil {
			s.log().Warn("billable outbox drain failed", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Sender) send(ctx context.Context, item OutboxItem) (int, error) {
	if s.Client == nil {
		return 0, errors.New("billable outbox client is not configured")
	}
	body, err := json.Marshal(map[string]any{
		"event_id":         item.EventID,
		"source_event_key": item.SourceEventKey,
		"auction_id":       item.AuctionID,
		"bid_id":           item.BidID,
		"imp_id":           item.ImpID,
		"reservation_ref":  item.ReservationRef,
		"amount_micros":    item.AmountMicros,
		"currency":         item.Currency,
		"occurred_at":      item.OccurredAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return 0, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, item.BURLTarget, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	// A callback host must never bounce the delivery into an unvalidated
	// target, so redirects are refused even if the injected client allows them.
	client := s.Client
	if client.CheckRedirect == nil {
		clone := *client
		clone.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &clone
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return response.StatusCode, nil
}

func (s *Sender) maxAttempts() uint32 {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return 8
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Sender) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func backoff(attempt uint32) time.Duration {
	seconds := 1 << min(attempt, 8)
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func targetHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
