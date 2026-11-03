// Package dmp adapts the SSP audience lookup to the DMP internal API. The SSP
// only ever sends a namespace-scoped HMAC token, never the publisher pseudonym.
package dmp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnavailable reports that the DMP could not answer safely. Callers must
// fail closed (no fill) instead of continuing with an unknown audience state.
var ErrUnavailable = errors.New("dmp audience state unavailable")

// Segment is the sanitized audience reference handed back to the SSP.
type Segment struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type resolveRequest struct {
	RequestID    string   `json:"request_id"`
	TenantID     string   `json:"tenant_id,omitempty"`
	ProviderID   string   `json:"provider_id,omitempty"`
	Namespace    string   `json:"namespace"`
	SubjectToken string   `json:"subject_token"`
	Purposes     []string `json:"purposes,omitempty"`
	MaxSegments  int      `json:"max_segments,omitempty"`
}

type resolveResponse struct {
	Segments []Segment `json:"segments"`
}

// Adapter calls the DMP audience resolver.
type Adapter struct {
	BaseURL    string
	TenantID   string
	Namespace  string
	ProviderID string
	HMACKey    []byte
	Timeout    time.Duration
	Client     *http.Client
	Purposes   []string
}

// Enabled reports whether identity lookups are configured.
func (a *Adapter) Enabled() bool {
	return a != nil && a.BaseURL != "" && len(a.HMACKey) >= 32 && a.Namespace != ""
}

// SubjectToken derives the namespace-scoped pseudonymous token.
func (a *Adapter) SubjectToken(pseudonym string) (string, error) {
	if len(a.HMACKey) < 32 {
		return "", errors.New("subject HMAC key is not configured")
	}
	mac := hmac.New(sha256.New, a.HMACKey)
	mac.Write([]byte(a.Namespace))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.TrimSpace(pseudonym)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Resolve returns the audience segments for a consented pseudonym. An empty
// pseudonym means "no identity" and never triggers a lookup.
func (a *Adapter) Resolve(ctx context.Context, requestID, pseudonym string, maxSegments int) ([]Segment, error) {
	if !a.Enabled() || strings.TrimSpace(pseudonym) == "" {
		return nil, nil
	}
	token, err := a.SubjectToken(pseudonym)
	if err != nil {
		return nil, ErrUnavailable
	}
	purposes := a.Purposes
	if len(purposes) == 0 {
		purposes = []string{"personalized_ads"}
	}
	payload := resolveRequest{
		RequestID:    requestID,
		TenantID:     a.TenantID,
		ProviderID:   a.ProviderID,
		Namespace:    a.Namespace,
		SubjectToken: token,
		Purposes:     purposes,
		MaxSegments:  maxSegments,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrUnavailable
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, a.BaseURL+"/internal/v1/audiences:resolve", bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: dmp status %d", ErrUnavailable, response.StatusCode)
	}
	var decoded resolveResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: invalid dmp response", ErrUnavailable)
	}
	segments := make([]Segment, 0, len(decoded.Segments))
	for _, segment := range decoded.Segments {
		if strings.TrimSpace(segment.ID) == "" {
			continue
		}
		segments = append(segments, Segment{ID: segment.ID, Name: segment.Name})
		if maxSegments > 0 && len(segments) >= maxSegments {
			break
		}
	}
	return segments, nil
}
