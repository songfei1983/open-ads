package snapshot

import (
	"errors"
	"sync"
	"sync/atomic"
)

// ErrNoSnapshot reports that no validated manifest has been activated yet.
var ErrNoSnapshot = errors.New("no SSP configuration snapshot is active")

var (
	currentMu       sync.RWMutex
	currentManifest *Manifest
	loads           atomic.Uint64
)

// Activate validates and atomically activates a complete manifest. The caller
// must treat the manifest as immutable afterwards (copy-on-write).
func Activate(manifest *Manifest) error {
	if err := Validate(manifest); err != nil {
		return err
	}
	currentMu.Lock()
	currentManifest = manifest
	currentMu.Unlock()
	loads.Add(1)
	return nil
}

// Current returns the active manifest and its version.
func Current() (*Manifest, string, error) {
	currentMu.RLock()
	manifest := currentManifest
	currentMu.RUnlock()
	if manifest == nil {
		return nil, "", ErrNoSnapshot
	}
	return manifest, manifest.Version, nil
}

// CurrentVersion returns the active manifest version or an empty string.
func CurrentVersion() string {
	currentMu.RLock()
	defer currentMu.RUnlock()
	if currentManifest == nil {
		return ""
	}
	return currentManifest.Version
}

// Loads reports how many manifests were activated in this process.
func Loads() uint64 { return loads.Load() }

// Reset clears the process snapshot. It exists for tests only.
func Reset() {
	currentMu.Lock()
	currentManifest = nil
	currentMu.Unlock()
}
