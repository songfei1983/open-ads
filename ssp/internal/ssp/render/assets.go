package render

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Asset store failures mapped onto the renderer contract.
var (
	ErrAssetUnavailable = errors.New("asset_unavailable")
	ErrAssetTampered    = errors.New("asset_tampered_detected")
)

const maxAssetBytes = 10 << 20

var assetRefPattern = regexp.MustCompile(`^as:([0-9a-f]{32}):([0-9a-f]{64})$`)

// ReadStore is the SSP's read-only view of the content-addressed asset volume
// written by the DSP. It never trusts a path from the request.
type ReadStore struct {
	root string
}

// NewReadStore validates the configured root directory.
func NewReadStore(root string) (*ReadStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("asset store root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("asset store root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("asset store root %q is not a directory", root)
	}
	return &ReadStore{root: root}, nil
}

// Read loads one immutable asset and verifies its digest before returning it.
// The returned MIME type is caller supplied (from the signed token), never
// derived from the file name.
func (s *ReadStore) Read(assetRef, expectedSHA256 string) ([]byte, error) {
	tenant, digest, err := ParseAssetRef(assetRef)
	if err != nil {
		return nil, err
	}
	expected := strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expected != digest {
		return nil, ErrAssetUnavailable
	}
	path := filepath.Join(s.root, tenant, "assets", digest[0:2], digest[2:4], digest)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrAssetUnavailable
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxAssetBytes {
		return nil, ErrAssetUnavailable
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrAssetUnavailable
	}
	actual := sha256.Sum256(payload)
	if hex.EncodeToString(actual[:]) != digest {
		return nil, ErrAssetTampered
	}
	return payload, nil
}

// RootForTest exposes the configured root to tests in this package family.
func (s *ReadStore) RootForTest() string { return s.root }

// ParseAssetRef validates the immutable reference shape and returns its parts.
func ParseAssetRef(assetRef string) (tenant, digest string, err error) {
	matches := assetRefPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(assetRef)))
	if matches == nil {
		return "", "", ErrAssetUnavailable
	}
	return matches[1], matches[2], nil
}
