// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/testhelpers"
)

// Tests for credential errors, token refresh failures, timeouts and the error
// messages. The helpers are in pipeline_trigger_action_retry_test.go.

// scriptedCredential answers each GetToken call (1-based) with next(n).
type scriptedCredential struct {
	mu    sync.Mutex
	calls int
	next  func(n int) (azcore.AccessToken, error)
}

func (c *scriptedCredential) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	return c.next(n)
}

// shortLivedToken is valid for less than tokenRefreshMargin, so it is never reused.
func shortLivedToken(n int) azcore.AccessToken {
	return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: time.Now().Add(time.Minute)}
}

// TestPipelineTriggerAction_Wait_CredentialErrorIsFatal verifies that a
// credential that can no longer authenticate ends the wait straight away with a
// clear message, instead of being retried until the timeout.
func TestPipelineTriggerAction_Wait_CredentialErrorIsFatal(t *testing.T) {
	t.Parallel()

	for name, credErr := range map[string]error{
		"credential unavailable": azidentity.NewCredentialUnavailableError("AzureCLICredential: please run az login to set up an account"),
		"authentication failed":  &azidentity.AuthenticationFailedError{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
			server := httptest.NewServer(srv)
			defer server.Close()

			credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
				if n <= 2 { // the up-front check and the trigger
					return shortLivedToken(n), nil
				}
				return azcore.AccessToken{}, credErr
			}}
			a := newRetryTestAction(server, credential, nil)

			resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error when the credential cannot get a token")
			}
			if text := diagnosticsText(resp); !strings.Contains(text, "cannot get an Azure DevOps access token") {
				t.Errorf("error should name the credential problem, got: %s", text)
			}
			if _, gets := srv.counts(); gets != 0 {
				t.Errorf("no poll can be sent without a token, got %d", gets)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_TransientCredentialErrorRetried verifies that a
// passing failure to get a token while waiting is retried.
func TestPipelineTriggerAction_Wait_TransientCredentialErrorRetried(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "completed", "succeeded") }}
	server := httptest.NewServer(srv)
	defer server.Close()

	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if n == 3 { // the first poll
			return azcore.AccessToken{}, fmt.Errorf("dial tcp: lookup login.microsoftonline.com: no such host")
		}
		return shortLivedToken(n), nil
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a passing token failure should be retried, got: %s", diagnosticsText(resp))
	}
	if !containsAny(progress, "failed (attempt 1)") {
		t.Errorf("expected a progress message for the retried poll, got %v", progress)
	}
}

// TestPipelineTriggerAction_Wait_FailedRefreshNeverResendsRejectedToken
// verifies that when re-requesting the token after a 401 fails, the rejected
// token is not sent again: the next attempt gets a token first.
func TestPipelineTriggerAction_Wait_FailedRefreshNeverResendsRejectedToken(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeStatus(w, http.StatusUnauthorized)
			return
		}
		writeRun(w, "completed", "succeeded")
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if n == 2 { // the refresh after the 401
			return azcore.AccessToken{}, fmt.Errorf("connection reset while getting a token")
		}
		return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: time.Now().Add(time.Hour)}, nil
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("the wait should recover once a token is obtained, got: %s", diagnosticsText(resp))
	}
	// The trigger and the first poll use token-1; the refresh fails; the next poll uses token-3.
	want := []string{"Bearer token-1", "Bearer token-1", "Bearer token-3"}
	if got := srv.authHeaders(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("expected headers %v, got %v", want, got)
	}
}

// TestPipelineTriggerAction_Wait_TimeoutMinutes verifies the timeout_minutes
// path: the wait ends with the run id, its link and "may still be running".
func TestPipelineTriggerAction_Wait_TimeoutMinutes(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	a.timeoutUnit = 200 * time.Millisecond // timeout_minutes = 1 lasts 200ms

	start := time.Now()
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the wait to time out")
	}
	text := diagnosticsText(resp)
	for _, want := range []string{"pipeline run 42", "timed out", "may still be running", testRunWebURL} {
		if !strings.Contains(text, want) {
			t.Errorf("error should contain %q, got: %s", want, text)
		}
	}
	if strings.Contains(text, "last poll error") {
		t.Errorf("no poll failed, so no poll error should be reported, got: %s", text)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout_minutes was not honoured: %v", elapsed)
	}
}

// TestPipelineTriggerAction_Wait_TimeoutReportsLastPollError verifies that when
// polls keep failing until the wait ends, the error gives the last poll error
// and how many polls in a row failed.
func TestPipelineTriggerAction_Wait_TimeoutReportsLastPollError(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusServiceUnavailable) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	a.timeoutUnit = 300 * time.Millisecond

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	text := diagnosticsText(resp)
	for _, want := range []string{"timed out", "last poll error (", "in a row): Azure DevOps API returned status 503"} {
		if !strings.Contains(text, want) {
			t.Errorf("error should contain %q, got: %s", want, text)
		}
	}
}

// TestPipelineTriggerAction_Wait_HonoursRetryAfterDate verifies that a
// Retry-After given as an HTTP date is honoured (capped) at the action level.
func TestPipelineTriggerAction_Wait_HonoursRetryAfterDate(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{
		onPost: queued,
		onGet: func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.Header().Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
				writeStatus(w, http.StatusServiceUnavailable)
				return
			}
			writeRun(w, "completed", "succeeded")
		},
	}
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
	// The backoff alone is at most maxRetryDelay (40ms); the Retry-After date raises it to the 100ms cap.
	if gap < 90*time.Millisecond || gap > 2*time.Second {
		t.Errorf("expected the retry about 100ms after the 503 (Retry-After date capped), got %v", gap)
	}
}

// TestPipelineTriggerAction_PAT401NotRetried verifies that a 401 with a
// Personal Access Token is not retried: the same header would be sent again.
func TestPipelineTriggerAction_PAT401NotRetried(t *testing.T) {
	t.Parallel()

	t.Run("trigger", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error")
		}
		if posts, _ := srv.counts(); posts != 1 {
			t.Errorf("a PAT 401 should not be retried, got %d trigger requests", posts)
		}
	})

	t.Run("poll", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error")
		}
		if text := diagnosticsText(resp); !strings.Contains(text, "personal access token") {
			t.Errorf("error should say the personal access token was rejected, got: %s", text)
		}
		if _, gets := srv.counts(); gets != 1 {
			t.Errorf("a PAT 401 should not be retried, got %d polls", gets)
		}
	})
}

// TestPipelineTriggerAction_Trigger_AmbiguousFailureWarnsAboutQueuedRun
// verifies that a trigger failure that may have queued a run says so, and a
// clear rejection does not.
func TestPipelineTriggerAction_Trigger_AmbiguousFailureWarnsAboutQueuedRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status int
		warns  bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, tc.status) }}
			server := httptest.NewServer(srv)
			defer server.Close()

			a := newRetryTestAction(server, nil, nil)
			resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
			text := diagnosticsText(resp)
			if warns := strings.Contains(text, "may have been queued"); warns != tc.warns {
				t.Errorf("status %d: warning shown = %v, want %v: %s", tc.status, warns, tc.warns, text)
			}
			if tc.warns && !strings.Contains(text, "pipeline 1 in project my-project") {
				t.Errorf("the warning should name the pipeline, got: %s", text)
			}
		})
	}

	t.Run("timeout after send", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) {
			time.Sleep(300 * time.Millisecond)
			writeRun(w, "inProgress", "unknown")
		}}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, nil, nil)
		a.requestTimeout = 50 * time.Millisecond
		resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
		if text := diagnosticsText(resp); !strings.Contains(text, "may have been queued") {
			t.Errorf("a timeout after sending should warn about a queued run, got: %s", text)
		}
	})
}

// TestPipelineTriggerAction_Trigger_AuthRetryCountsTowardsMaxAttempts verifies
// that the 401 retry is one of the triggerMaxAttempts attempts.
func TestPipelineTriggerAction_Trigger_AuthRetryCountsTowardsMaxAttempts(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	transport := &failingTransport{
		inner:    &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)},
		method:   http.MethodPost,
		failures: triggerMaxAttempts - 1,
		err:      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
	a := newRetryTestAction(server, &countingCredential{lifetime: time.Hour}, transport)

	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodDAC), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	transport.mu.Lock()
	attempts := transport.seen
	transport.mu.Unlock()
	if attempts != triggerMaxAttempts {
		t.Errorf("expected %d attempts in total, got %d", triggerMaxAttempts, attempts)
	}
}

// TestPipelineTriggerAction_ErrorBodiesAreTruncated verifies that a large error
// response body is cut short in progress messages.
func TestPipelineTriggerAction_ErrorBodiesAreTruncated(t *testing.T) {
	t.Parallel()

	page := "<html>" + strings.Repeat("x", 5000) + "</html>"
	srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(page))
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
	for _, m := range progress {
		if strings.Contains(m, "status 502") {
			if len(m) > maxErrorBodyBytes+300 || !strings.Contains(m, "truncated") {
				t.Errorf("the 502 body should be truncated, got a %d-byte message", len(m))
			}
			return
		}
	}
	t.Errorf("expected a progress message for the 502, got %v", progress)
}

// TestPipelineTriggerAction_HTTPClient_UsesProxyFromEnvironment verifies that
// the default client honours the proxy environment variables.
func TestPipelineTriggerAction_HTTPClient_UsesProxyFromEnvironment(t *testing.T) {
	t.Parallel()

	transport, ok := (&PipelineTriggerAction{}).getHTTPClient().Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatal("the default transport should use the proxy from the environment")
	}
}
