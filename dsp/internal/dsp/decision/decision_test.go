package decision

import "testing"

func TestNoBidDecisioner(t *testing.T) {
	response, err := (NoBidDecisioner{}).Decide(t.Context(), Request{ID: "request-1", ImpressionCount: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.RequestID != "request-1" || !response.NoBid || response.NoBidReason != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
}
