// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// Tests for how responses that are not the API's JSON are reported: text cut
// for messages, sign-in pages (203, or a redirect to an HTML page) and the
// header that asks Azure DevOps not to send them.

func TestTruncateText(t *testing.T) {
	t.Parallel()

	t.Run("short text unchanged", func(t *testing.T) {
		t.Parallel()
		if got := truncateText("no problem"); got != "no problem" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("does not split a multi-byte character at the cut", func(t *testing.T) {
		t.Parallel()
		// "€" is 3 bytes and starts one byte before the cut, so a plain byte cut
		// would end the message with half a character.
		text := strings.Repeat("x", maxErrorBodyBytes-1) + "€" + strings.Repeat("y", 100)
		got := truncateText(text)
		if !utf8.ValidString(got) {
			t.Fatalf("result is not valid UTF-8: %q", got[len(got)-40:])
		}
		if !strings.HasPrefix(got, strings.Repeat("x", maxErrorBodyBytes-1)+"...") {
			t.Errorf("expected the cut before the character, got %q", got[maxErrorBodyBytes-5:])
		}
		if !strings.Contains(got, "(614 bytes, truncated)") {
			t.Errorf("expected the original size, got %q", got[maxErrorBodyBytes-5:])
		}
	})

	t.Run("replaces invalid UTF-8", func(t *testing.T) {
		t.Parallel()
		got := truncateText("bad \xff\xfe bytes")
		if !utf8.ValidString(got) || !strings.Contains(got, "�") {
			t.Errorf("expected invalid bytes replaced, got %q", got)
		}
	})

	t.Run("long invalid UTF-8 is valid after the cut", func(t *testing.T) {
		t.Parallel()
		// Each invalid byte becomes a 3-byte replacement character, so the cut
		// can land inside one.
		got := truncateText(strings.Repeat("\xffa", maxErrorBodyBytes))
		if !utf8.ValidString(got) || !strings.Contains(got, "truncated") {
			t.Errorf("expected valid, truncated text, got %q", got)
		}
	})
}

// TestPipelineTriggerAction_ProgressMessagesAreValidUTF8 verifies that error
// bodies with a character across the cut, or with invalid UTF-8, still give
// valid UTF-8 progress messages (Terraform's protocol rejects anything else).
func TestPipelineTriggerAction_ProgressMessagesAreValidUTF8(t *testing.T) {
	t.Parallel()

	bodies := []string{
		strings.Repeat("x", 490) + strings.Repeat("€", 20),
		"\xff\xfe gateway error \xc3",
	}
	srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		if n <= len(bodies) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(bodies[n-1]))
			return
		}
		writeRun(w, "completed", "succeeded")
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %s", diagnosticsText(resp))
	}
	seen := 0
	for _, m := range progress {
		if !utf8.ValidString(m) {
			t.Errorf("progress message is not valid UTF-8: %q", m)
		}
		if strings.Contains(m, "status 502") {
			seen++
		}
	}
	if seen != len(bodies) {
		t.Errorf("expected %d progress messages for the 502s, got %d: %v", len(bodies), seen, progress)
	}
}

// headerRecorder records the X-TFS-FedAuthRedirect header of every request.
type headerRecorder struct {
	mu     sync.Mutex
	values []string
	inner  http.Handler
}

func (h *headerRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.values = append(h.values, r.Header.Get(fedAuthRedirectHeader))
	h.mu.Unlock()
	h.inner.ServeHTTP(w, r)
}

// TestPipelineTriggerAction_RequestsSuppressSignInRedirects verifies that the
// trigger and every poll ask Azure DevOps for a 401 instead of a sign-in page.
func TestPipelineTriggerAction_RequestsSuppressSignInRedirects(t *testing.T) {
	t.Parallel()

	rec := &headerRecorder{inner: &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		if n < 2 {
			writeRun(w, "inProgress", "unknown")
			return
		}
		writeRun(w, "completed", "succeeded")
	}}}
	server := httptest.NewServer(rec)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %s", diagnosticsText(resp))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.values) < 3 {
		t.Fatalf("expected the trigger and at least two polls, got %d requests", len(rec.values))
	}
	for i, v := range rec.values {
		if v != "Suppress" {
			t.Errorf("request %d: %s = %q, want Suppress", i+1, fedAuthRedirectHeader, v)
		}
	}
}

// TestPipelineTriggerAction_Trigger_203SignInPageIsNotAQueuedRun verifies that a
// 203 sign-in page (an expired or invalid PAT) is reported as a credential
// problem, without the warning that a run may have been queued.
func TestPipelineTriggerAction_Trigger_203SignInPageIsNotAQueuedRun(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNonAuthoritativeInfo)
		_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
	}))
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	text := diagnosticsText(resp)
	if !strings.Contains(text, "203") || !strings.Contains(text, "did not accept the credential") {
		t.Errorf("error should explain the 203, got: %s", text)
	}
	if strings.Contains(text, "queued") {
		t.Errorf("a 203 cannot have queued a run, got: %s", text)
	}
}

// TestPipelineTriggerAction_Trigger_RedirectToHTMLMayHaveQueued verifies that a
// trigger that ends at an HTML page (a 302 followed to a 200) does not claim a
// run was queued, only that one may have been.
func TestPipelineTriggerAction_Trigger_RedirectToHTMLMayHaveQueued(t *testing.T) {
	t.Parallel()

	var posts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signin" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
			return
		}
		if r.Method == http.MethodPost {
			mu.Lock()
			posts++
			mu.Unlock()
		}
		http.Redirect(w, r, "/signin", http.StatusFound)
	}))
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	text := diagnosticsText(resp)
	if !strings.Contains(text, "may have been queued") || strings.Contains(text, "was queued") {
		t.Errorf("error should say a run may have been queued (not that it was), got: %s", text)
	}
	if !strings.Contains(text, "text/html") {
		t.Errorf("error should name the content type, got: %s", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Errorf("trigger requests = %d, want 1", posts)
	}
}

// TestPipelineTriggerAction_Trigger_UnparseableJSONWasQueued verifies that an
// accepted JSON response that cannot be parsed still says a run was queued.
func TestPipelineTriggerAction_Trigger_UnparseableJSONWasQueued(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "not a number"`))
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if text := diagnosticsText(resp); !strings.Contains(text, "was queued") {
		t.Errorf("error should say a run was queued, got: %s", text)
	}
}
