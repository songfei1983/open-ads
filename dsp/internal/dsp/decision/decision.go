// Package decision defines the DSP hot-path decision boundary.
package decision

import "context"

// Request is the minimal validated input needed by a bidder decisioner.
// Protocol-specific OpenRTB DTOs stay in the HTTP adapter.
type Request struct {
	ID              string
	ImpressionCount int
	SiteDomain      string
	SitePage        string
	BannerWidth     int
	BannerHeight    int
	Currencies      []string
}

// Response is the decision result returned to the HTTP adapter.
type Response struct {
	RequestID             string
	NoBid                 bool
	NoBidReason           int
	Candidate             bool
	CandidateLineItemID   string
	CandidateBidCPMMicros int64

	// Valid bid response fields
	BidID             string
	ImpressionID      string
	Price             float64
	AdMarkup          string
	CreativeID        string
	CampaignID        string
	Currency          string
	Width             int
	Height            int
	ReservationID     string
	ReservationToken  string
	WinNoticeURL      string
	BillableNoticeURL string
	LossNoticeURL     string
}

// Decisioner makes a bid decision without coupling the transport to storage.
type Decisioner interface {
	Decide(context.Context, Request) (Response, error)
}

// NoBidDecisioner is the safe default until a published campaign snapshot is available.
type NoBidDecisioner struct{}

// Decide returns a standard technical/no-candidate decision.
func (NoBidDecisioner) Decide(_ context.Context, req Request) (Response, error) {
	return Response{RequestID: req.ID, NoBid: true, NoBidReason: 2}, nil
}
