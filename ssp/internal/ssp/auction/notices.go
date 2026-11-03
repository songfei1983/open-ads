package auction

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Notify delivers win and loss notices after the auction response is written.
// Iteration 5 keeps delivery best effort; durable retry/quarantine belongs to
// the billable outbox iteration.
func (e *Engine) Notify(ctx context.Context, outcome Outcome) {
	if len(outcome.Notices) == 0 {
		return
	}
	client := e.NoticeClient
	if client == nil {
		client = &http.Client{Timeout: 500 * time.Millisecond}
	}
	timeout := e.NoticeTimeout
	if timeout <= 0 {
		timeout = 300 * time.Millisecond
	}
	semaphore := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, notice := range outcome.Notices {
		notice := notice
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-semaphore }()
			deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			request, err := http.NewRequestWithContext(deliveryCtx, http.MethodGet, expandNoticeURL(notice.URL, notice), nil)
			if err != nil {
				e.observeNotice(false, notice.NoticeID, err)
				return
			}
			request.Header.Set("X-Notice-ID", notice.NoticeID)
			request.Header.Set("Accept", "application/json")
			response, err := client.Do(request)
			if err != nil {
				e.observeNotice(false, notice.NoticeID, err)
				return
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				e.observeNotice(false, notice.NoticeID, nil)
				return
			}
			e.observeNotice(true, notice.NoticeID, nil)
		}()
	}
	wg.Wait()
}

func (e *Engine) observeNotice(ok bool, noticeID string, err error) {
	if e.Metrics != nil {
		e.Metrics.ObserveNotice(ok)
	}
	if !ok && e.Logger != nil {
		// Never log the callback URL: it carries a reservation reference.
		attributes := []any{"notice_id", noticeID}
		if err != nil {
			attributes = append(attributes, "error", err)
		}
		e.Logger.Warn("DSP notice delivery failed", attributes...)
	}
}

func expandNoticeURL(raw string, notice Notice) string {
	expanded := strings.ReplaceAll(raw, "${AUCTION_PRICE}", FormatCPMMicros(notice.PriceMicros))
	return strings.ReplaceAll(expanded, "${AUCTION_LOSS}", strconv.Itoa(notice.Reason))
}
