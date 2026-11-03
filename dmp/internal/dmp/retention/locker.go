package retention

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/songfei1983/open-ads/dmp/internal/dmp/valkey"
)

// releaseScript only deletes the lease when it is still ours, so a slow sweep
// cannot release a lease another instance has already taken over.
const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// LockerValkey adapts a Valkey client to the sweep lease.
func LockerValkey(client *valkey.Client) Locker {
	return ValkeyLocker{Client: client}
}

// ValkeyLocker is the multi-replica sweep lease.
type ValkeyLocker struct {
	Client *valkey.Client
}

// TryAcquire takes the lease with SET NX PX.
func (l ValkeyLocker) TryAcquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	if l.Client == nil {
		return "", true, nil
	}
	token, err := leaseToken()
	if err != nil {
		return "", false, err
	}
	reply, err := l.Client.Do(ctx, "SET", key, token, "NX", "PX", fmt.Sprintf("%d", ttl.Milliseconds()))
	if err != nil {
		return "", false, fmt.Errorf("lease acquire: %w", err)
	}
	if _, ok := reply.(string); !ok {
		return "", false, nil
	}
	return token, true, nil
}

// Release deletes the lease only if this instance still owns it.
func (l ValkeyLocker) Release(ctx context.Context, key, token string) error {
	if l.Client == nil {
		return nil
	}
	if _, err := l.Client.Do(ctx, "EVAL", releaseScript, "1", key, token); err != nil {
		return fmt.Errorf("lease release: %w", err)
	}
	return nil
}

func leaseToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate lease token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
