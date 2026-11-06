// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

import (
	"context"
	"fmt"
	"io"
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

// entraResponseBody builds the HTTP response an AuthenticationFailedError
// carries when Microsoft Entra ID answered the token request with body.
func entraResponseBody(status int, body string) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, "https://login.microsoftonline.com/tenant/oauth2/v2.0/token", nil)
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// entraOAuthResponse is an Entra ID token endpoint error response with the
// given OAuth error code.
func entraOAuthResponse(status int, code string) *http.Response {
	return entraResponseBody(status, fmt.Sprintf(`{"error":%q,"error_description":"AADSTS00000: test","error_codes":[0]}`, code))
}

// entraResponse is an Entra ID rejection of an expired client secret.
func entraResponse(status int) *http.Response {
	return entraOAuthResponse(status, "invalid_client")
}

// TestPipelineTriggerAction_Wait_CredentialRejectedIsFatal verifies that a
// definitive rejection by Microsoft Entra ID ends the wait straight away: an
// AuthenticationFailedError carrying a 401 or 403, or a 400 with any OAuth
// error other than temporarily_unavailable and server_error (including a code
// the provider does not know and a body that is not an OAuth error).
func TestPipelineTriggerAction_Wait_CredentialRejectedIsFatal(t *testing.T) {
	t.Parallel()

	cases := map[string]*http.Response{
		"401 invalid_client":          entraOAuthResponse(http.StatusUnauthorized, "invalid_client"),
		"403":                         entraOAuthResponse(http.StatusForbidden, "access_denied"),
		"400 invalid_client":          entraOAuthResponse(http.StatusBadRequest, "invalid_client"),
		"400 unauthorized_client":     entraOAuthResponse(http.StatusBadRequest, "unauthorized_client"),
		"400 invalid_grant":           entraOAuthResponse(http.StatusBadRequest, "invalid_grant"),
		"400 invalid_request":         entraOAuthResponse(http.StatusBadRequest, "invalid_request"),
		"400 invalid_scope":           entraOAuthResponse(http.StatusBadRequest, "invalid_scope"),
		"400 unknown code":            entraOAuthResponse(http.StatusBadRequest, "some_new_error"),
		"400 not an OAuth error body": entraResponseBody(http.StatusBadRequest, "<html>Bad Request</html>"),
	}
	for name, entraResp := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
			server := httptest.NewServer(srv)
			defer server.Close()

			credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
				if n <= 2 { // the up-front check and the trigger
					return shortLivedToken(n), nil
				}
				return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{RawResponse: entraResp}
			}}
			a := newRetryTestAction(server, credential, nil)

			resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error when Entra ID rejects the credential")
			}
			text := diagnosticsText(resp)
			if !strings.Contains(text, "Microsoft Entra ID rejected") {
				t.Errorf("error should say Entra ID rejected the credential, got: %s", text)
			}
			if !strings.Contains(text, "may still be running") {
				t.Errorf("error should say the run may still be running, got: %s", text)
			}
			if credential.calls != 3 {
				t.Errorf("a rejection should not be retried, got %d token requests", credential.calls)
			}
			if _, gets := srv.counts(); gets != 0 {
				t.Errorf("no poll can be sent without a token, got %d", gets)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_CredentialProblemRetried verifies that the
// errors the Azure identity library returns for passing problems are retried:
// an AuthenticationFailedError without a response (a network failure reaching
// Entra ID, an Azure CLI failure), one carrying an Entra ID server error, and a
// credential that is unavailable (for example the managed identity endpoint
// cannot be reached). All of them are marked not retriable by the library.
func TestPipelineTriggerAction_Wait_CredentialProblemRetried(t *testing.T) {
	t.Parallel()

	for name, credErr := range map[string]error{
		"authentication failed without a response": &azidentity.AuthenticationFailedError{},
		"Entra ID server error":                    &azidentity.AuthenticationFailedError{RawResponse: entraResponse(http.StatusServiceUnavailable)},
		"Entra ID 400 temporarily_unavailable":     &azidentity.AuthenticationFailedError{RawResponse: entraOAuthResponse(http.StatusBadRequest, "temporarily_unavailable")},
		"Entra ID 400 server_error":                &azidentity.AuthenticationFailedError{RawResponse: entraOAuthResponse(http.StatusBadRequest, "server_error")},
		"credential unavailable":                   azidentity.NewCredentialUnavailableError("ManagedIdentityCredential: no response from the IMDS endpoint"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "completed", "succeeded") }}
			server := httptest.NewServer(srv)
			defer server.Close()

			credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
				if n == 3 || n == 4 { // the first two polls
					return azcore.AccessToken{}, credErr
				}
				return shortLivedToken(n), nil
			}}
			a := newRetryTestAction(server, credential, nil)

			resp, progress := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
			if resp.Diagnostics.HasError() {
				t.Fatalf("a passing credential problem should be retried, got: %s", diagnosticsText(resp))
			}
			if !containsAny(progress, "failed (attempt 2)") {
				t.Errorf("expected progress messages for the retried polls, got %v", progress)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_CredentialFailuresInARowStop verifies that a
// credential that keeps failing (for example an expired Azure CLI sign-in)
// stops the wait after credentialFailureLimit polls in a row, long before
// timeout_minutes runs out.
func TestPipelineTriggerAction_Wait_CredentialFailuresInARowStop(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
	server := httptest.NewServer(srv)
	defer server.Close()

	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if n <= 2 { // the up-front check and the trigger
			return shortLivedToken(n), nil
		}
		return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{}
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the wait to stop")
	}
	text := diagnosticsText(resp)
	if !strings.Contains(text, fmt.Sprintf("%d times in a row", credentialFailureLimit)) {
		t.Errorf("error should say how many token requests failed in a row, got: %s", text)
	}
	if !strings.Contains(text, "may still be running") {
		t.Errorf("error should say the run may still be running, got: %s", text)
	}
	if strings.Contains(text, "timed out") {
		t.Errorf("the wait should stop before the timeout, got: %s", text)
	}
	if got, want := credential.calls, 2+credentialFailureLimit; got != want {
		t.Errorf("token requests = %d, want %d", got, want)
	}
}

// TestPipelineTriggerAction_Wait_CredentialFailureCountResetsOnResponse
// verifies that the credential failure count only counts failures in a row: a
// poll that reached Azure DevOps resets it.
func TestPipelineTriggerAction_Wait_CredentialFailureCountResetsOnResponse(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		if n >= 2 {
			writeRun(w, "completed", "succeeded")
			return
		}
		writeStatus(w, http.StatusServiceUnavailable)
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	// The polls alternate: credentialFailureLimit-1 credential failures, one poll
	// that reaches Azure DevOps (503), credentialFailureLimit-1 more failures, then
	// success. The limit is never reached in a row.
	failFirst := credentialFailureLimit - 1
	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		switch {
		case n <= 2:
			return shortLivedToken(n), nil
		case n <= 2+failFirst:
			return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{}
		case n == 3+failFirst:
			return shortLivedToken(n), nil
		case n <= 3+2*failFirst:
			return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{}
		default:
			return shortLivedToken(n), nil
		}
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("failures that are not in a row should not stop the wait, got: %s", diagnosticsText(resp))
	}
}

// TestPipelineTriggerAction_Trigger_CredentialRejectedNotRetried verifies that
// when re-requesting the token after a 401 on the trigger is rejected by
// Microsoft Entra ID, the trigger is not retried.
func TestPipelineTriggerAction_Trigger_CredentialRejectedNotRetried(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if n <= 1 { // the up-front check, also used by the first trigger request
			return azcore.AccessToken{Token: "token-1", ExpiresOn: time.Now().Add(time.Hour)}, nil
		}
		return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{RawResponse: entraResponse(http.StatusUnauthorized)}
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, progress := invokeWithin(t, a, noWaitConfig(t, authMethodDAC), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if text := diagnosticsText(resp); !strings.Contains(text, "Microsoft Entra ID rejected") {
		t.Errorf("error should say Entra ID rejected the credential, got: %s", text)
	}
	if containsAny(progress, "failed before a run could be queued") {
		t.Errorf("a rejected credential should not be retried, got progress %v", progress)
	}
	if credential.calls != 2 {
		t.Errorf("token requests = %d, want 2 (the up-front check and one refresh)", credential.calls)
	}
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("trigger requests = %d, want 1", posts)
	}
}

// TestPipelineTriggerAction_Trigger_UnreadableAcceptedResponseWarns verifies
// that when Azure DevOps accepts the trigger (201) but the response body cannot
// be read, the error says a run was probably queued and the trigger is not
// retried.
func TestPipelineTriggerAction_Trigger_UnreadableAcceptedResponseWarns(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) {
		// Promise more body than is written, so the client sees an unexpected EOF.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 42, "state": "inPro`))
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if text := diagnosticsText(resp); !strings.Contains(text, "may have been queued") {
		t.Errorf("error should warn that a run may have been queued, got: %s", text)
	}
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("trigger requests = %d, want 1", posts)
	}
}

// TestPipelineTriggerAction_Wait_FailuresSayRunMayStillBeRunning verifies that
// every poll failure that ends the wait says the run was not cancelled.
func TestPipelineTriggerAction_Wait_FailuresSayRunMayStillBeRunning(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		auth  string
		onGet func(int, http.ResponseWriter)
	}{
		"404":                  {authMethodPAT, func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusNotFound) }},
		"PAT 401":              {authMethodPAT, func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }},
		"401 after re-request": {authMethodDAC, func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }},
		"unparseable 200":      {authMethodPAT, func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusOK); _, _ = w.Write([]byte("<html>")) }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: tc.onGet}
			server := httptest.NewServer(srv)
			defer server.Close()

			a := newRetryTestAction(server, nil, nil)
			resp, _ := invokeWithin(t, a, waitConfig(t, tc.auth), 5*time.Second)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected the wait to stop")
			}
			if text := diagnosticsText(resp); !strings.Contains(text, "may still be running") {
				t.Errorf("error should say the run may still be running, got: %s", text)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_401AfterOtherFailureRetriedAgain verifies that
// a 401 is only fatal when it directly follows another 401: a different
// failure in between allows another re-request of the token.
func TestPipelineTriggerAction_Wait_401AfterOtherFailureRetriedAgain(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(n int, w http.ResponseWriter) {
		switch n {
		case 1, 3:
			writeStatus(w, http.StatusUnauthorized)
		case 2:
			writeStatus(w, http.StatusServiceUnavailable)
		default:
			writeRun(w, "completed", "succeeded")
		}
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("401, 503, 401 should not be fatal, got: %s", diagnosticsText(resp))
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

// TestPipelineTriggerAction_CredentialErrorsAreTruncated verifies that a long
// credential error (a whole Entra ID response body, or a credential chain's
// report of every credential it tried) is shortened in progress messages and
// in the final error, like an Azure DevOps error response.
func TestPipelineTriggerAction_CredentialErrorsAreTruncated(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
	server := httptest.NewServer(srv)
	defer server.Close()

	long := azidentity.NewCredentialUnavailableError("DefaultAzureCredential: failed to acquire a token. " + strings.Repeat("Attempted credentials: ... ", 300))
	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if n <= 2 { // the up-front check and the trigger
			return shortLivedToken(n), nil
		}
		return azcore.AccessToken{}, long
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the wait to stop after the credential failures")
	}
	limit := maxErrorBodyBytes + 400
	for _, m := range progress {
		if strings.Contains(m, "access token") && (len(m) > limit || !strings.Contains(m, "truncated")) {
			t.Errorf("the credential error should be truncated in progress messages, got a %d-byte message", len(m))
		}
	}
	if text := diagnosticsText(resp); len(text) > limit+400 || !strings.Contains(text, "truncated") {
		t.Errorf("the credential error should be truncated in the final error, got %d bytes", len(text))
	}
}

// slowCredential answers the first `fast` GetToken calls at once and blocks
// every later call until its context ends, like a credential that never
// answers.
type slowCredential struct {
	mu    sync.Mutex
	calls int
	fast  int
}

func (c *slowCredential) GetToken(ctx context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n <= c.fast {
		return shortLivedToken(n), nil
	}
	<-ctx.Done()
	return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{}
}

// TestPipelineTriggerAction_TokenRequestTimeout verifies that a token request
// that does not answer is cut off by its own timeout: before the trigger the
// action fails instead of hanging, and while waiting the poll is retried like
// any other failure to get a token, so the wait stops after the limit.
func TestPipelineTriggerAction_TokenRequestTimeout(t *testing.T) {
	t.Parallel()

	t.Run("before the trigger", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, &slowCredential{fast: 0}, nil)
		a.tokenTimeout = 50 * time.Millisecond

		start := time.Now()
		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error when the credential does not answer")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("the token request should time out quickly, took %s", elapsed)
		}
		if posts, _ := srv.counts(); posts != 0 {
			t.Errorf("nothing should be triggered without a token, got %d POSTs", posts)
		}
	})

	t.Run("while waiting", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }}
		server := httptest.NewServer(srv)
		defer server.Close()

		credential := &slowCredential{fast: 2} // the up-front check and the trigger
		a := newRetryTestAction(server, credential, nil)
		a.tokenTimeout = 50 * time.Millisecond

		resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected the wait to stop after the token requests timed out")
		}
		text := diagnosticsText(resp)
		if !strings.Contains(text, fmt.Sprintf("%d times in a row", credentialFailureLimit)) {
			t.Errorf("the timed-out token requests should count as credential failures, got: %s", text)
		}
		if _, gets := srv.counts(); gets != 0 {
			t.Errorf("no poll can be sent without a token, got %d", gets)
		}
	})
}

// TestPipelineTriggerAction_Wait_401ResetsCredentialFailures verifies that a
// 401 from Azure DevOps resets the count of failures to get a token (a token
// was obtained), so earlier credential failures and one after the 401 do not
// add up to the limit.
func TestPipelineTriggerAction_Wait_401ResetsCredentialFailures(t *testing.T) {
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

	// Calls: 1 up-front check, 2 trigger, 3..(limit+1) fail (limit-1 polls in a
	// row), then a token for the poll that gets the 401, then the forced
	// refresh fails, then a token for the poll that succeeds.
	failFrom, failTo := 3, credentialFailureLimit+1
	tokenFor401 := failTo + 1
	refreshFails := tokenFor401 + 1
	credential := &scriptedCredential{next: func(n int) (azcore.AccessToken, error) {
		if (n >= failFrom && n <= failTo) || n == refreshFails {
			return azcore.AccessToken{}, &azidentity.AuthenticationFailedError{}
		}
		return shortLivedToken(n), nil
	}}
	a := newRetryTestAction(server, credential, nil)

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("the 401 should reset the credential failure count, got: %s", diagnosticsText(resp))
	}
	if _, gets := srv.counts(); gets != 2 {
		t.Errorf("status polls = %d, want 2 (the 401 and the success)", gets)
	}
}
