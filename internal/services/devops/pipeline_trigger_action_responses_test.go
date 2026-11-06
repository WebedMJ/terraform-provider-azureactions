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

	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/testhelpers"
)

// Tests for how responses that are not the API's JSON are reported: text cut
// for messages, sign-in pages (203, or a redirect, which is not followed), the
// header that asks Azure DevOps not to send them, and failed requests whose
// body cannot be read in full.

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

// TestPipelineTriggerAction_Trigger_RedirectIsNotFollowed verifies that a
// redirect answering the trigger is not followed: the error says a run was
// most likely not queued and how to check, and the trigger is sent once.
func TestPipelineTriggerAction_Trigger_RedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	var posts, signIns int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/signin" {
			signIns++
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
			return
		}
		if r.Method == http.MethodPost {
			posts++
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
	for _, want := range []string{"status 302", "redirect to", "most likely not queued", "check the pipeline's runs"} {
		if !strings.Contains(text, want) {
			t.Errorf("error should contain %q, got: %s", want, text)
		}
	}
	if strings.Contains(text, "may have been queued") {
		t.Errorf("a redirect should not be reported as a possibly queued run, got: %s", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Errorf("trigger requests = %d, want 1", posts)
	}
	if signIns != 0 {
		t.Errorf("the redirect should not be followed, but the sign-in page was requested %d times", signIns)
	}
}

// devOpsOnlyTransport sends requests for dev.azure.com to devOps and every
// other request to the real network.
type devOpsOnlyTransport struct {
	devOps http.RoundTripper
}

func (d *devOpsOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == "dev.azure.com" {
		return d.devOps.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// TestPipelineTriggerAction_Trigger_RedirectTargetUnreachableNotRetried
// verifies that a trigger answered with a redirect to a host that refuses
// connections is not sent again: the trigger request reached a server, so a
// connection failure further on must not make it look unsent.
func TestPipelineTriggerAction_Trigger_RedirectTargetUnreachableNotRetried(t *testing.T) {
	t.Parallel()

	// A port nothing listens on.
	closed := httptest.NewServer(http.NotFoundHandler())
	unreachable := closed.URL + "/signin"
	closed.Close()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) {
		w.Header().Set("Location", unreachable)
		w.WriteHeader(http.StatusFound)
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	// Only Azure DevOps requests go to the test server; the redirect target is
	// dialled for real and refuses the connection.
	a := newRetryTestAction(server, nil, &devOpsOnlyTransport{
		devOps: &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)},
	})
	resp, progress := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("trigger requests = %d, want 1", posts)
	}
	if containsAny(progress, "retrying") {
		t.Errorf("the trigger should not be retried, got progress %v", progress)
	}
}

// TestPipelineTriggerAction_Wait_RedirectStopsWait verifies that a status poll
// answered with a redirect is not followed and ends the wait with the run's
// link.
func TestPipelineTriggerAction_Wait_RedirectStopsWait(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) {
		w.Header().Set("Location", "https://login.example.com/signin")
		w.WriteHeader(http.StatusFound)
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	text := diagnosticsText(resp)
	for _, want := range []string{"status 302", "login.example.com", "may still be running", testRunWebURL} {
		if !strings.Contains(text, want) {
			t.Errorf("error should contain %q, got: %s", want, text)
		}
	}
	if _, gets := srv.counts(); gets != 1 {
		t.Errorf("polls = %d, want 1", gets)
	}
}

// writeTruncated answers with status but promises more body than it writes, so
// the client cannot read the body in full.
func writeTruncated(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Length", "1000")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"message":"trunc`))
}

// TestPipelineTriggerAction_Wait_TruncatedErrorBodyKeepsStatus verifies that a
// failed poll whose body cannot be read in full is still treated by its status
// (and Retry-After), not as a generic passing problem.
func TestPipelineTriggerAction_Wait_TruncatedErrorBodyKeepsStatus(t *testing.T) {
	t.Parallel()

	t.Run("401 with Entra ID re-requests the token", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
			if n == 1 {
				writeTruncated(w, http.StatusUnauthorized)
				return
			}
			writeRun(w, "completed", "succeeded")
		}}
		server := httptest.NewServer(srv)
		defer server.Close()

		credential := &countingCredential{lifetime: time.Hour}
		a := newRetryTestAction(server, credential, nil)
		resp, progress := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %s", diagnosticsText(resp))
		}
		if credential.count() != 2 {
			t.Errorf("token requests = %d, want 2 (one forced by the 401)", credential.count())
		}
		if !containsAny(progress, "HTTP 401") {
			t.Errorf("expected a progress message about the 401, got %v", progress)
		}
	})

	t.Run("401 with a PAT stops", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeTruncated(w, http.StatusUnauthorized) }}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		if text := diagnosticsText(resp); !strings.Contains(text, "personal access token") {
			t.Errorf("error should say the personal access token was rejected, got: %s", text)
		}
		if _, gets := srv.counts(); gets != 1 {
			t.Errorf("polls = %d, want 1", gets)
		}
	})

	t.Run("403 stops", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeTruncated(w, http.StatusForbidden) }}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		text := diagnosticsText(resp)
		if !strings.Contains(text, "status 403") || !strings.Contains(text, "could not be read in full") {
			t.Errorf("error should give the 403 and the read failure, got: %s", text)
		}
		if _, gets := srv.counts(); gets != 1 {
			t.Errorf("polls = %d, want 1", gets)
		}
	})

	t.Run("429 honours Retry-After", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.Header().Set("Retry-After", "30") // capped to maxRetryAfter (100ms) in the test action
				writeTruncated(w, http.StatusTooManyRequests)
				return
			}
			writeRun(w, "completed", "succeeded")
		}}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %s", diagnosticsText(resp))
		}
		srv.mu.Lock()
		gap := srv.getTimes[1].Sub(srv.getTimes[0])
		srv.mu.Unlock()
		if gap < 90*time.Millisecond || gap > 2*time.Second {
			t.Errorf("expected the retry about 100ms after the 429 (Retry-After capped), got %v", gap)
		}
	})
}

// hangingStatusServer queues a run, then answers status polls with the given
// statuses in order; once they run out, each poll hangs until the client gives up.
func hangingStatusServer(t *testing.T, statuses ...int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			writeRun(w, "inProgress", "unknown")
			return
		}
		mu.Lock()
		gets++
		n := gets
		mu.Unlock()
		if n <= len(statuses) {
			writeStatus(w, statuses[n-1])
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return gets
	}
}

// TestPipelineTriggerAction_Wait_DeadlineDuringPollIsReported verifies that
// when the wait's deadline expires during a status poll, that poll is recorded:
// the error reports it as the last poll error and counts it.
func TestPipelineTriggerAction_Wait_DeadlineDuringPollIsReported(t *testing.T) {
	t.Parallel()

	t.Run("first poll", func(t *testing.T) {
		t.Parallel()

		server, _ := hangingStatusServer(t)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		a.timeoutUnit = 300 * time.Millisecond
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		text := diagnosticsText(resp)
		for _, want := range []string{"timed out", "last poll error (1 in a row)", "deadline exceeded"} {
			if !strings.Contains(text, want) {
				t.Errorf("error should contain %q, got: %s", want, text)
			}
		}
	})

	t.Run("later poll", func(t *testing.T) {
		t.Parallel()

		server, gets := hangingStatusServer(t, http.StatusServiceUnavailable)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		a.timeoutUnit = 300 * time.Millisecond
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		text := diagnosticsText(resp)
		if n := gets(); n != 2 {
			t.Fatalf("polls = %d, want 2 (a 503, then one that hangs)", n)
		}
		if !strings.Contains(text, "last poll error (2 in a row)") {
			t.Errorf("error should count both failed polls, got: %s", text)
		}
		if strings.Contains(text, "in a row): Azure DevOps API returned status 503") {
			t.Errorf("error should report the poll that ran into the deadline, not the earlier 503, got: %s", text)
		}
	})
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
