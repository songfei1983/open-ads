// Package runtime publishes and reads the per-instance configuration fan-out
// status. It is observability state with a TTL, never an authority for config.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/songfei1983/open-ads/ssp/internal/ssp/control"
	"github.com/songfei1983/open-ads/ssp/internal/ssp/valkey"
)

const heartbeatTTL = 30 * time.Second

// Heartbeat is the value an instance publishes every refresh cycle.
type Heartbeat struct {
	InstanceID      string `json:"instance_id"`
	ManifestVersion string `json:"manifest_version"`
	ObservedVersion string `json:"observed_version"`
	Ready           bool   `json:"ready"`
	HeartbeatUnix   int64  `json:"heartbeat_unix"`
}

// Writer records heartbeats in the tenant-scoped Valkey keyspace.
type Writer struct {
	Client *valkey.Client
	Tenant string
}

// Report stores the heartbeat and prunes stale index entries.
func (w *Writer) Report(ctx context.Context, heartbeat Heartbeat) error {
	if w.Client == nil || w.Tenant == "" || heartbeat.InstanceID == "" {
		return fmt.Errorf("runtime heartbeat is not configured")
	}
	payload, err := json.Marshal(heartbeat)
	if err != nil {
		return err
	}
	key := w.key(heartbeat.InstanceID)
	if _, err := w.Client.Do(ctx, "SET", key, string(payload), "EX", strconv.Itoa(int(heartbeatTTL.Seconds()))); err != nil {
		return fmt.Errorf("write SSP heartbeat: %w", err)
	}
	index := "ssp:runtime-index:{" + w.Tenant + "}"
	if _, err := w.Client.Do(ctx, "ZADD", index, strconv.FormatInt(heartbeat.HeartbeatUnix, 10), heartbeat.InstanceID); err != nil {
		return fmt.Errorf("index SSP heartbeat: %w", err)
	}
	cutoff := strconv.FormatInt(time.Now().UTC().Add(-2*heartbeatTTL).Unix(), 10)
	_, _ = w.Client.Do(ctx, "ZREMRANGEBYSCORE", index, "-inf", cutoff)
	return nil
}

func (w *Writer) key(instanceID string) string {
	return "ssp:runtime:{" + w.Tenant + "}:" + instanceID
}

// Reader lists the heartbeats observed by the control plane.
type Reader struct {
	Client *valkey.Client
	Tenant string
}

// Instances returns every live heartbeat. Missing keys or expired entries are
// reported as stale instead of being silently dropped.
func (r *Reader) Instances(ctx context.Context) ([]control.RuntimeInstance, error) {
	if r.Client == nil || r.Tenant == "" {
		return nil, fmt.Errorf("runtime status is not configured")
	}
	index := "ssp:runtime-index:{" + r.Tenant + "}"
	raw, err := r.Client.Do(ctx, "ZRANGEBYSCORE", index, "-inf", "+inf")
	if err != nil {
		return nil, fmt.Errorf("list SSP instance index: %w", err)
	}
	ids, ok := raw.([]any)
	if !ok || len(ids) == 0 {
		return []control.RuntimeInstance{}, nil
	}
	keys := make([]string, 0, len(ids))
	instanceIDs := make([]string, 0, len(ids))
	for _, value := range ids {
		id, ok := value.(string)
		if !ok || id == "" {
			continue
		}
		instanceIDs = append(instanceIDs, id)
		keys = append(keys, r.key(id))
	}
	if len(keys) == 0 {
		return []control.RuntimeInstance{}, nil
	}
	args := append([]string{"MGET"}, keys...)
	values, err := r.Client.Do(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("read SSP instance heartbeats: %w", err)
	}
	rows, _ := values.([]any)
	now := time.Now().UTC()
	instances := make([]control.RuntimeInstance, 0, len(instanceIDs))
	for i, id := range instanceIDs {
		instance := control.RuntimeInstance{InstanceID: id, Stale: true}
		if i < len(rows) {
			if payload, ok := rows[i].(string); ok && payload != "" {
				var heartbeat Heartbeat
				if json.Unmarshal([]byte(payload), &heartbeat) == nil {
					instance.ManifestVersion = heartbeat.ManifestVersion
					instance.ObservedVersion = heartbeat.ObservedVersion
					instance.Ready = heartbeat.Ready
					instance.HeartbeatUnix = heartbeat.HeartbeatUnix
					instance.Stale = !heartbeat.Ready || now.Sub(time.Unix(heartbeat.HeartbeatUnix, 0).UTC()) > heartbeatTTL
				}
			}
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].InstanceID < instances[j].InstanceID })
	return instances, nil
}

func (r *Reader) key(instanceID string) string {
	return "ssp:runtime:{" + r.Tenant + "}:" + instanceID
}
