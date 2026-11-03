package netguard

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.5.4", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "fe80::1", "fd00::1",
		"224.0.0.1",
	}
	for _, value := range blocked {
		if !IsBlockedIP(net.ParseIP(value)) {
			t.Errorf("IsBlockedIP(%s) = false, want true", value)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, value := range allowed {
		if IsBlockedIP(net.ParseIP(value)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", value)
		}
	}
}

func TestIsBlockedHost(t *testing.T) {
	blocked := []string{"", "localhost", "api.localhost", "printer.local", "dsp.internal", "127.0.0.1", "[::1]"}
	for _, value := range blocked {
		if !IsBlockedHost(value) {
			t.Errorf("IsBlockedHost(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"dsp.example.com", "8.8.8.8"} {
		if IsBlockedHost(value) {
			t.Errorf("IsBlockedHost(%q) = true, want false", value)
		}
	}
}

func TestDialContextRefusesLoopbackAndPrivateLiterals(t *testing.T) {
	dial := DialContext(time.Second, false)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.5:80", "localhost:80"} {
		if _, err := dial(context.Background(), "tcp", addr); err == nil {
			t.Fatalf("dial %s succeeded, want blocked", addr)
		}
	}
}

func TestDialContextAllowsPrivateTargetsWhenOptedIn(t *testing.T) {
	// The sandbox opts out of the guard; a closed loopback port proves the dial
	// reached the network layer instead of being rejected by netguard.
	dial := DialContext(200*time.Millisecond, true)
	_, err := dial(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("dial to a closed port reported success")
	}
	var blocked bool = err != nil && strings.Contains(err.Error(), ErrBlockedAddress.Error())
	if blocked {
		t.Fatalf("opted-in dial was still refused by netguard: %v", err)
	}
}
