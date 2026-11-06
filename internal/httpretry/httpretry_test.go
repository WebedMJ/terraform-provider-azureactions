// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package httpretry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestClassifyStatus(t *testing.T) {
	t.Parallel()

	cases := map[int]Kind{
		http.StatusUnauthorized:        Auth,
		http.StatusRequestTimeout:      Transient,
		http.StatusTooManyRequests:     Transient,
		http.StatusInternalServerError: Transient,
		http.StatusBadGateway:          Transient,
		http.StatusServiceUnavailable:  Transient,
		http.StatusGatewayTimeout:      Transient,
		http.StatusBadRequest:          Fatal,
		http.StatusForbidden:           Fatal,
		http.StatusNotFound:            Fatal,
		http.StatusConflict:            Fatal,
		http.StatusNotImplemented:      Fatal,
	}

	for code, want := range cases {
		if got := ClassifyStatus(code); got != want {
			t.Errorf("ClassifyStatus(%d) = %s, want %s", code, got, want)
		}
	}
}

func TestIsNotSent(t *testing.T) {
	t.Parallel()

	dialErr := &url.Error{Op: "Post", URL: "https://example", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	dnsErr := &url.Error{Op: "Post", URL: "https://example", Err: &net.DNSError{Err: "no such host", Name: "example"}}
	readErr := &url.Error{Op: "Post", URL: "https://example", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}

	if !IsNotSent(dialErr) {
		t.Error("dial error should count as not sent")
	}
	if !IsNotSent(fmt.Errorf("wrapped: %w", dnsErr)) {
		t.Error("DNS error should count as not sent")
	}
	if IsNotSent(readErr) {
		t.Error("read error after the request was sent must not count as not sent")
	}
	if IsNotSent(context.DeadlineExceeded) {
		t.Error("a timeout is ambiguous and must not count as not sent")
	}
	if IsNotSent(nil) {
		t.Error("nil is not an error")
	}

	// net/http wraps a failure to reach an HTTP proxy in a "proxyconnect" error.
	proxyDialErr := &url.Error{Op: "Post", URL: "https://example", Err: &net.OpError{Op: "proxyconnect", Net: "tcp",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}}
	if !IsNotSent(proxyDialErr) {
		t.Error("a refused connection to the proxy should count as not sent")
	}
	proxyOtherErr := &url.Error{Op: "Post", URL: "https://example", Err: &net.OpError{Op: "proxyconnect", Net: "tcp", Err: syscall.ECONNRESET}}
	if IsNotSent(proxyOtherErr) {
		t.Error("a proxy error that does not wrap a dial error must not count as not sent")
	}
}

// TestIsNotSent_RealProxyRefused checks the error net/http actually returns
// when the configured proxy refuses the connection.
func TestIsNotSent_RealProxyRefused(t *testing.T) {
	t.Parallel()

	// A port that nothing listens on: open and close a listener.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	proxyURL, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	_, err = client.Get("https://dev.azure.com.invalid/")
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if !IsNotSent(err) {
		t.Errorf("a refused proxy connection should count as not sent, got %#v", err)
	}
}

func TestIsParentDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	if IsParentDone(ctx) {
		t.Error("live context reported as done")
	}
	cancel()
	if !IsParentDone(ctx) {
		t.Error("cancelled context not reported as done")
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	header := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return h
	}

	if d, ok := RetryAfter(header("7"), now); !ok || d != 7*time.Second {
		t.Errorf("seconds: got %v %v", d, ok)
	}
	if d, ok := RetryAfter(header(" 0 "), now); !ok || d != 0 {
		t.Errorf("zero seconds: got %v %v", d, ok)
	}
	if d, ok := RetryAfter(header(now.Add(90*time.Second).Format(http.TimeFormat)), now); !ok || d != 90*time.Second {
		t.Errorf("http date: got %v %v", d, ok)
	}
	if d, ok := RetryAfter(header(now.Add(-time.Minute).Format(http.TimeFormat)), now); !ok || d != 0 {
		t.Errorf("past http date: got %v %v", d, ok)
	}
	if d, ok := RetryAfter(header("99999999999"), now); !ok || d != 24*time.Hour {
		t.Errorf("huge seconds should be clamped: got %v %v", d, ok)
	}
	for _, bad := range []string{"", "-5", "soon", "1.5"} {
		if _, ok := RetryAfter(header(bad), now); ok {
			t.Errorf("value %q should be rejected", bad)
		}
	}
}

func TestBackoff_DoublesAndCaps(t *testing.T) {
	t.Parallel()

	full := func() float64 { return 0.999999 }
	none := func() float64 { return 0 }
	base, max := time.Second, 10*time.Second

	wantUpper := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, upper := range wantUpper {
		attempt := i + 1
		hi := Backoff(attempt, base, max, full)
		lo := Backoff(attempt, base, max, none)
		if hi > upper || hi < upper-time.Millisecond {
			t.Errorf("attempt %d: upper bound %v, want about %v", attempt, hi, upper)
		}
		if lo != upper/2 {
			t.Errorf("attempt %d: lower bound %v, want %v", attempt, lo, upper/2)
		}
	}
}

func TestBackoff_EdgeCases(t *testing.T) {
	t.Parallel()

	if d := Backoff(3, 0, time.Second, nil); d != 0 {
		t.Errorf("zero base should give zero delay, got %v", d)
	}
	if d := Backoff(0, time.Second, time.Second, func() float64 { return 0 }); d != 500*time.Millisecond {
		t.Errorf("attempt 0 is treated as 1, got %v", d)
	}
	if d := Backoff(1, time.Second, time.Millisecond, func() float64 { return 0 }); d != 500*time.Millisecond {
		t.Errorf("max below base is raised to base, got %v", d)
	}
	if d := Backoff(1000, time.Second, time.Minute, nil); d > time.Minute || d < 30*time.Second {
		t.Errorf("large attempt must stay within [max/2, max], got %v", d)
	}
}

func TestSleep(t *testing.T) {
	t.Parallel()

	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("Sleep did not return promptly when the context was cancelled")
	}

	if err := Sleep(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("zero delay on a cancelled context should report the context error, got %v", err)
	}
}
