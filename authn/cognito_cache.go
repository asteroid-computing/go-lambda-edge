package authn

import (
	"context"
	"crypto/rsa"
	"io"
	"net/http"
	"time"
)

type jwksSnapshot struct {
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

type jwksFlight struct {
	done chan struct{}
	ctx  context.Context
	err  error // Published by closing done; never contains provider diagnostics.
}

type jwksKey struct {
	public  *rsa.PublicKey
	expires time.Time
}

// An empty kid means Warm. Every path performs at most one fetch/join. A
// successful fetch missing the requested kid must not start a refresh loop.
func (v *CognitoVerifier) key(ctx context.Context, kid string) (jwksKey, error) {
	v.mu.Lock()
	now := time.Now()
	fresh := now.Before(v.snapshot.expires)
	if fresh && (kid == "" || v.snapshot.keys[kid] != nil) {
		key := jwksKey{public: v.snapshot.keys[kid], expires: v.snapshot.expires}
		v.mu.Unlock()
		return key, nil
	}
	flight := v.flight
	if flight == nil {
		if now.Before(v.retryAfter) || fresh && now.Before(v.unknownAfter) {
			err := ErrUnavailable
			if fresh && !v.failed {
				err = ErrInvalidCredentials
			}
			v.mu.Unlock()
			return jwksKey{}, err
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), v.cfg.Timeout)
		flight = &jwksFlight{done: make(chan struct{}), ctx: fetchCtx}
		v.flight = flight
		if fresh {
			v.unknownAfter = now.Add(30 * time.Second)
		}
		go v.refresh(flight, cancel)
	}
	v.mu.Unlock()
	select {
	case <-ctx.Done():
		return jwksKey{}, ctx.Err()
	case <-flight.ctx.Done():
		// A misbehaving transport may still be running. Retain its flight so
		// subsequent callers cannot accumulate replacement fetch goroutines.
		// Completion also cancels the fetch context; prefer its published
		// result when both signals are ready.
		select {
		case <-flight.done:
		default:
			return jwksKey{}, ErrUnavailable
		}
	case <-flight.done:
	}
	if flight.err != nil {
		return jwksKey{}, ErrUnavailable
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !time.Now().Before(v.snapshot.expires) {
		return jwksKey{}, ErrUnavailable
	}
	key := v.snapshot.keys[kid]
	if kid != "" && key == nil {
		return jwksKey{}, ErrInvalidCredentials
	}
	return jwksKey{public: key, expires: v.snapshot.expires}, nil
}

func (v *CognitoVerifier) refresh(flight *jwksFlight, cancel context.CancelFunc) {
	defer cancel()
	keys, err := v.fetchKeys(flight.ctx)
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	deadline, _ := flight.ctx.Deadline()
	if flight.ctx.Err() != nil || !now.Before(deadline) {
		err = ErrUnavailable
	}
	if err != nil {
		v.failed = true
		v.backoff = min(max(v.backoff*2, 5*time.Second), time.Minute)
		v.retryAfter = now.Add(v.backoff)
		flight.err = ErrUnavailable
	} else {
		// Publish only a complete validated set. Removed keys do not survive
		// in a union, and failure never extends the old snapshot's lifetime.
		v.snapshot = jwksSnapshot{keys: keys, expires: now.Add(v.cfg.CacheTTL)}
		v.unknownAfter = now.Add(30 * time.Second)
		v.failed = false
		v.backoff = 0
		v.retryAfter = time.Time{}
	}
	v.flight = nil
	close(flight.done)
}

func (v *CognitoVerifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.Issuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return nil, ErrUnavailable
	}
	return parseJWKS(body)
}
