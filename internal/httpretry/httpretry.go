// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

// Package httpretry holds the retry policy shared by actions that call REST
// APIs directly over net/http: how a failed request is classified, how long to
// back off, and how to read a Retry-After header.
package httpretry

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind classifies a failed HTTP request.
type Kind int

const (
	// Fatal errors are not retried.
	Fatal Kind = iota
	// Transient errors (network problems, timeouts, throttling, server errors)
	// are retried with backoff.
	Transient
	// Auth errors (HTTP 401) are retried once with a freshly acquired token.
	Auth
)

// String returns a readable name for the kind.
func (k Kind) String() string {
	switch k {
	case Transient:
		return "transient"
	case Auth:
		return "auth"
	default:
		return "fatal"
	}
}

// ClassifyStatus classifies a non-success HTTP status code.
//
// 408, 429, 500, 502, 503 and 504 are transient, 401 is an auth error and
// everything else (including 403 and 404) is fatal.
func ClassifyStatus(code int) Kind {
	switch code {
	case http.StatusUnauthorized:
		return Auth
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return Transient
	default:
		return Fatal
	}
}

// IsNotSent reports whether err shows that a request never reached the server:
// the connection could not be opened (for example connection refused) or the
// host name could not be resolved. Only these errors make it safe to retry a
// request that is not idempotent. Through an HTTP proxy, a failure to connect to
// the proxy ("proxyconnect" wrapping a dial error) also counts.
func IsNotSent(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	var opErr *net.OpError
	for target := err; errors.As(target, &opErr); target = opErr.Err {
		switch opErr.Op {
		case "dial":
			return true
		case "proxyconnect":
			continue
		default:
			return false
		}
	}

	return false
}

// IsParentDone reports whether ctx (the caller's overall context, not a
// per-request one) has ended. A request error while the parent context is
// still alive is a transport problem or a per-request timeout and can be
// retried; once the parent context is done the caller must stop.
func IsParentDone(ctx context.Context) bool {
	return ctx.Err() != nil
}

// RetryAfter parses a Retry-After header, which is either a number of seconds
// or an HTTP date. It returns false when the header is absent or invalid.
// Dates in the past give a zero duration.
func RetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		// Guard against overflow for absurd values; callers cap the result anyway.
		if seconds > int64(24*time.Hour/time.Second) {
			seconds = int64(24 * time.Hour / time.Second)
		}
		return time.Duration(seconds) * time.Second, true
	}

	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}

	return 0, false
}

// Backoff returns the delay before retry number attempt (1 for the first
// retry): base doubled per attempt, capped at maxDelay, with "equal jitter" (a
// random value between half and all of the capped delay) so that many clients
// do not retry in step. jitter returns a value in [0, 1); nil uses math/rand.
func Backoff(attempt int, base, maxDelay time.Duration, jitter func() float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if maxDelay < base {
		maxDelay = base
	}
	if attempt < 1 {
		attempt = 1
	}

	delay := base
	for i := 1; i < attempt && delay < maxDelay; i++ {
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}

	if jitter == nil {
		jitter = rand.Float64
	}
	half := delay / 2
	return half + time.Duration(jitter()*float64(delay-half))
}

// Sleep waits for d or until ctx is done, whichever comes first. It returns
// ctx.Err() when the context ended first.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
