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
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/testhelpers"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// ----------------------------------------------------------------------------
// Helpers for the retry and token refresh tests
// ----------------------------------------------------------------------------

const testRunWebURL = "https://dev.azure.com/myorg/my-project/_build/results?buildId=42"

// countingCredential hands out a new token ("token-1", "token-2", ...) on each
// GetToken call, valid for lifetime.
type countingCredential struct {
	mu       sync.Mutex
	calls    int
	lifetime time.Duration
}

func (c *countingCredential) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return azcore.AccessToken{Token: fmt.Sprintf("token-%d", c.calls), ExpiresOn: time.Now().Add(c.lifetime)}, nil
}

func (c *countingCredential) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// scriptedServer is a fake Azure DevOps API whose answers to the trigger POST
// and the status GETs are scripted per call number (1-based).
type scriptedServer struct {
	mu       sync.Mutex
	posts    int
	gets     int
	auth     []string
	getTimes []time.Time
	onPost   func(n int, w http.ResponseWriter)
	onGet    func(n int, w http.ResponseWriter)
}

func (s *scriptedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	var n int
	var handler func(int, http.ResponseWriter)
	switch r.Method {
	case http.MethodPost:
		s.posts++
		n, handler = s.posts, s.onPost
	case http.MethodGet:
		s.gets++
		s.getTimes = append(s.getTimes, time.Now())
		n, handler = s.gets, s.onGet
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if handler == nil {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	handler(n, w)
}

func (s *scriptedServer) counts() (posts, gets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts, s.gets
}

func (s *scriptedServer) authHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.auth...)
}

// runJSON is a pipeline run body that includes the run's web link.
func runJSON(state, result string) string {
	return fmt.Sprintf(`{"id":42,"name":"20261106.1","state":%q,"result":%q,"_links":{"web":{"href":%q}}}`,
		state, result, testRunWebURL)
}

func writeRun(w http.ResponseWriter, state, result string) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(runJSON(state, result)))
}

func writeStatus(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":"status %d"}`, status)
}

// queued answers the trigger POST with a queued run.
func queued(_ int, w http.ResponseWriter) { writeRun(w, "inProgress", "unknown") }

// failThenSucceed answers the first status GET with status, then reports the
// run in progress once and completed after that.
func failThenSucceed(status int) func(int, http.ResponseWriter) {
	return func(n int, w http.ResponseWriter) {
		switch n {
		case 1:
			writeStatus(w, status)
		case 2:
			writeRun(w, "inProgress", "unknown")
		default:
			writeRun(w, "completed", "succeeded")
		}
	}
}

// failingTransport fails the first `failures` requests with the given method
// using err, and passes everything else to the inner transport.
type failingTransport struct {
	inner    http.RoundTripper
	method   string
	failures int
	err      error

	mu   sync.Mutex
	seen int
}

func (f *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == f.method {
		f.mu.Lock()
		f.seen++
		fail := f.seen <= f.failures
		f.mu.Unlock()
		if fail {
			return nil, f.err
		}
	}
	return f.inner.RoundTrip(req)
}

// newRetryTestAction returns an action pointed at server with short poll
// intervals and retry delays so the tests stay fast.
func newRetryTestAction(server *httptest.Server, credential azcore.TokenCredential, transport http.RoundTripper) *PipelineTriggerAction {
	if transport == nil {
		transport = &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)}
	}
	a := &PipelineTriggerAction{
		httpClient:    &http.Client{Transport: transport},
		pollInterval:  20 * time.Millisecond,
		maxRetryDelay: 40 * time.Millisecond,
		maxRetryAfter: 100 * time.Millisecond,
	}
	client := newDevOpsTestClient("https://dev.azure.com/myorg")
	if credential != nil {
		client.Credential = credential
	}
	a.Configure(context.Background(), action.ConfigureRequest{ProviderData: client}, &action.ConfigureResponse{})
	return a
}

func waitConfig(t *testing.T, authMethod string) tfsdk.Config {
	t.Helper()
	waitTrue := true
	timeoutMins := int64(1)
	pat := ""
	if authMethod == authMethodPAT {
		pat = "my-pat-token"
	}
	return buildDevOpsConfig(t, "my-project", 1, authMethod, pat, "", &waitTrue, &timeoutMins)
}

func noWaitConfig(t *testing.T, authMethod string) tfsdk.Config {
	t.Helper()
	pat := ""
	if authMethod == authMethodPAT {
		pat = "my-pat-token"
	}
	return buildDevOpsConfig(t, "my-project", 1, authMethod, pat, "", nil, nil)
}

func invokeWithin(t *testing.T, a *PipelineTriggerAction, cfg tfsdk.Config, limit time.Duration) (*action.InvokeResponse, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	var mu sync.Mutex
	var progress []string
	resp := &action.InvokeResponse{
		SendProgress: func(e action.InvokeProgressEvent) {
			mu.Lock()
			progress = append(progress, e.Message)
			mu.Unlock()
		},
	}
	a.Invoke(ctx, action.InvokeRequest{Config: cfg}, resp)
	return resp, progress
}

func containsAny(messages []string, fragment string) bool {
	for _, m := range messages {
		if strings.Contains(m, fragment) {
			return true
		}
	}
	return false
}

func diagnosticsText(resp *action.InvokeResponse) string {
	var b strings.Builder
	for _, d := range resp.Diagnostics {
		b.WriteString(d.Summary())
		b.WriteString(": ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// ----------------------------------------------------------------------------
// Token handling
// ----------------------------------------------------------------------------

// TestCredentialAuthHeader_Caching verifies the token is reused while it has
// more than tokenRefreshMargin left, refreshed near expiry, refreshed when
// forced, and never reused when its expiry is unknown.
func TestCredentialAuthHeader_Caching(t *testing.T) {
	t.Parallel()

	now := time.Now()
	credential := &countingCredential{lifetime: time.Hour}
	source := &credentialAuthHeader{credential: credential, now: func() time.Time { return now }}
	ctx := context.Background()

	h1, err := source.Header(ctx, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, _ := source.Header(ctx, false)
	if h1 != "Bearer token-1" || h2 != h1 || credential.count() != 1 {
		t.Fatalf("token should be cached: h1=%q h2=%q calls=%d", h1, h2, credential.count())
	}

	// Within the refresh margin of expiry: a new token is requested.
	now = now.Add(time.Hour - tokenRefreshMargin + time.Second)
	if h3, _ := source.Header(ctx, false); h3 != "Bearer token-2" {
		t.Fatalf("token near expiry should be refreshed, got %q", h3)
	}

	// Forced refresh after a 401.
	if h4, _ := source.Header(ctx, true); h4 != "Bearer token-3" {
		t.Fatalf("forced refresh should request a new token, got %q", h4)
	}

	// A token with no expiry is never reused.
	unknown := &credentialAuthHeader{credential: &mockAzureTokenCredential{token: "static"}}
	unknown.token = azcore.AccessToken{Token: "cached-without-expiry"}
	if h, _ := unknown.Header(ctx, false); h != "Bearer static" {
		t.Fatalf("token without expiry should not be reused, got %q", h)
	}
}

// TestPipelineTriggerAction_Wait_RequestsCurrentTokenPerRequest verifies that a
// short-lived token is replaced for the trigger and every poll, instead of one
// token being reused for the whole wait.
func TestPipelineTriggerAction_Wait_RequestsCurrentTokenPerRequest(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{
		onPost: queued,
		onGet: func(n int, w http.ResponseWriter) {
			if n < 3 {
				writeRun(w, "inProgress", "unknown")
				return
			}
			writeRun(w, "completed", "succeeded")
		},
	}
	server := httptest.NewServer(srv)
	defer server.Close()

	// Shorter than tokenRefreshMargin, so it is never reused.
	credential := &countingCredential{lifetime: time.Minute}
	a := newRetryTestAction(server, credential, nil)

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %s", diagnosticsText(resp))
	}

	headers := srv.authHeaders()
	posts, gets := srv.counts()
	if posts != 1 || gets != 3 {
		t.Fatalf("expected 1 POST and 3 GETs, got %d and %d", posts, gets)
	}
	// One token for the up-front check, then one per request.
	if credential.count() != 1+posts+gets {
		t.Fatalf("expected %d token requests, got %d", 1+posts+gets, credential.count())
	}
	seen := map[string]bool{}
	for _, h := range headers {
		if seen[h] {
			t.Fatalf("token %q was reused although it was about to expire", h)
		}
		seen[h] = true
	}
}

// TestPipelineTriggerAction_Wait_401RefreshesTokenOnce verifies that a 401
// during polling gets a new token and the wait carries on.
func TestPipelineTriggerAction_Wait_401RefreshesTokenOnce(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: failThenSucceed(http.StatusUnauthorized)}
	server := httptest.NewServer(srv)
	defer server.Close()

	credential := &countingCredential{lifetime: time.Hour}
	a := newRetryTestAction(server, credential, nil)

	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %s", diagnosticsText(resp))
	}
	// One token up front (cached for the trigger and polls), one forced after the 401.
	if credential.count() != 2 {
		t.Fatalf("expected 2 token requests, got %d", credential.count())
	}
	headers := srv.authHeaders()
	if last := headers[len(headers)-1]; last != "Bearer token-2" {
		t.Fatalf("polls after the 401 should use the new token, got %q", last)
	}
	if !containsAny(progress, "HTTP 401") {
		t.Errorf("expected a progress message about the 401, got %v", progress)
	}
}

// TestPipelineTriggerAction_Wait_401TwiceIsFatal verifies that a second
// consecutive 401 ends the wait.
func TestPipelineTriggerAction_Wait_401TwiceIsFatal(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, &countingCredential{lifetime: time.Hour}, nil)
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodDAC), 5*time.Second)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error after two 401s")
	}
	if text := diagnosticsText(resp); !strings.Contains(text, "still unauthorised") {
		t.Errorf("unexpected error text: %s", text)
	}
	if _, gets := srv.counts(); gets != 2 {
		t.Errorf("expected exactly 2 polls, got %d", gets)
	}
}

// ----------------------------------------------------------------------------
// Transient and fatal poll errors
// ----------------------------------------------------------------------------

// TestPipelineTriggerAction_Wait_RetriesTransientStatuses verifies that each
// transient HTTP status during polling is retried and the wait completes.
func TestPipelineTriggerAction_Wait_RetriesTransientStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: failThenSucceed(status)}
			server := httptest.NewServer(srv)
			defer server.Close()

			a := newRetryTestAction(server, nil, nil)
			resp, progress := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
			if resp.Diagnostics.HasError() {
				t.Fatalf("status %d should be retried, got: %s", status, diagnosticsText(resp))
			}
			if !containsAny(progress, "failed (attempt 1)") {
				t.Errorf("expected a progress message for the retried poll, got %v", progress)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_HonoursRetryAfter verifies that a 429 with a
// Retry-After header waits that long (capped) before the next poll.
func TestPipelineTriggerAction_Wait_HonoursRetryAfter(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{
		onPost: queued,
		onGet: func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.Header().Set("Retry-After", "30") // capped to maxRetryAfter (100ms) in the test action
				writeStatus(w, http.StatusTooManyRequests)
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
	// The backoff alone is at most maxRetryDelay (40ms); Retry-After raises it to the 100ms cap.
	if gap < 90*time.Millisecond || gap > 2*time.Second {
		t.Errorf("expected the retry about 100ms after the 429 (Retry-After capped), got %v", gap)
	}
}

// TestPipelineTriggerAction_Wait_RetriesTransportError verifies that a network
// error during polling is retried.
func TestPipelineTriggerAction_Wait_RetriesTransportError(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeRun(w, "completed", "succeeded") }}
	server := httptest.NewServer(srv)
	defer server.Close()

	transport := &failingTransport{
		inner:    &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)},
		method:   http.MethodGet,
		failures: 2,
		err:      &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
	}
	a := newRetryTestAction(server, nil, transport)

	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("network errors should be retried, got: %s", diagnosticsText(resp))
	}
	if !containsAny(progress, "failed (attempt 2)") {
		t.Errorf("expected two retried polls, got %v", progress)
	}
}

// TestPipelineTriggerAction_Wait_RetriesPerRequestTimeout verifies that a poll
// that hits the per-request timeout (while the overall wait has time left) is
// retried.
func TestPipelineTriggerAction_Wait_RetriesPerRequestTimeout(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{
		onPost: queued,
		onGet: func(n int, w http.ResponseWriter) {
			if n == 1 {
				time.Sleep(300 * time.Millisecond) // longer than the 50ms request timeout
			}
			writeRun(w, "completed", "succeeded")
		},
	}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	a.requestTimeout = 50 * time.Millisecond

	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a per-request timeout should be retried, got: %s", diagnosticsText(resp))
	}
	if _, gets := srv.counts(); gets < 2 {
		t.Errorf("expected the timed-out poll to be retried, got %d polls", gets)
	}
}

// TestPipelineTriggerAction_Wait_PersistentTransientErrorsTimeOut verifies that
// transient errors are retried until the wait ends, and that the error then
// names the run, says it may still be running and gives its link.
func TestPipelineTriggerAction_Wait_PersistentTransientErrorsTimeOut(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusServiceUnavailable) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	start := time.Now()
	resp, progress := invokeWithin(t, a, waitConfig(t, authMethodPAT), 500*time.Millisecond)
	elapsed := time.Since(start)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a timeout error")
	}
	text := diagnosticsText(resp)
	for _, want := range []string{"pipeline run 42", "may still be running", testRunWebURL} {
		if !strings.Contains(text, want) {
			t.Errorf("error should contain %q, got: %s", want, text)
		}
	}
	if _, gets := srv.counts(); gets < 3 {
		t.Errorf("expected several retried polls, got %d", gets)
	}
	if !containsAny(progress, "failed (attempt 2)") {
		t.Errorf("expected progress messages for the retries, got %v", progress)
	}
	// The backoff is bounded by the deadline: the wait ends promptly once it expires.
	if elapsed > 2*time.Second {
		t.Errorf("wait overran its deadline: %v", elapsed)
	}
}

// TestPipelineTriggerAction_Wait_FatalStatusesStopAtOnce verifies that 400, 403
// and 404 during polling end the wait on the first poll.
func TestPipelineTriggerAction_Wait_FatalStatusesStopAtOnce(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()

			srv := &scriptedServer{onPost: queued, onGet: func(_ int, w http.ResponseWriter) { writeStatus(w, status) }}
			server := httptest.NewServer(srv)
			defer server.Close()

			a := newRetryTestAction(server, nil, nil)
			resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("status %d should end the wait", status)
			}
			if _, gets := srv.counts(); gets != 1 {
				t.Errorf("status %d should not be retried, got %d polls", status, gets)
			}
		})
	}
}

// TestPipelineTriggerAction_Wait_UnparseableStatusIsFatal verifies that a 200
// with a body that is not a pipeline run ends the wait.
func TestPipelineTriggerAction_Wait_UnparseableStatusIsFatal(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{
		onPost: queued,
		onGet: func(_ int, w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>not json</html>"))
		},
	}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, waitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unparseable status response should end the wait")
	}
	if _, gets := srv.counts(); gets != 1 {
		t.Errorf("expected exactly 1 poll, got %d", gets)
	}
}

// ----------------------------------------------------------------------------
// Trigger request retries
// ----------------------------------------------------------------------------

// TestPipelineTriggerAction_Trigger_ServerErrorNotRetried verifies that a 500
// from the trigger request is not retried, because a run may have been queued.
func TestPipelineTriggerAction_Trigger_ServerErrorNotRetried(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusInternalServerError) }}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("the trigger must be sent exactly once, got %d", posts)
	}
}

// TestPipelineTriggerAction_Trigger_TimeoutAfterSendNotRetried verifies that a
// trigger request that timed out after being sent is not retried.
func TestPipelineTriggerAction_Trigger_TimeoutAfterSendNotRetried(t *testing.T) {
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
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	// Give the server a moment to count any duplicate request.
	time.Sleep(400 * time.Millisecond)
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("the trigger must be sent exactly once, got %d", posts)
	}
}

// TestPipelineTriggerAction_Trigger_ConnectionRefusedRetried verifies that a
// trigger request whose connection was never made is retried.
func TestPipelineTriggerAction_Trigger_ConnectionRefusedRetried(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued}
	server := httptest.NewServer(srv)
	defer server.Close()

	transport := &failingTransport{
		inner:    &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)},
		method:   http.MethodPost,
		failures: 1,
		err:      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
	a := newRetryTestAction(server, nil, transport)

	resp, progress := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a refused connection should be retried, got: %s", diagnosticsText(resp))
	}
	if posts, _ := srv.counts(); posts != 1 {
		t.Errorf("exactly one trigger should reach the server, got %d", posts)
	}
	if !containsAny(progress, "before a run could be queued") {
		t.Errorf("expected a progress message for the retry, got %v", progress)
	}
}

// TestPipelineTriggerAction_Trigger_ConnectionRefusedGivesUp verifies that the
// trigger retries are bounded.
func TestPipelineTriggerAction_Trigger_ConnectionRefusedGivesUp(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: queued}
	server := httptest.NewServer(srv)
	defer server.Close()

	transport := &failingTransport{
		inner:    &testhelpers.HostRewriteTransport{Host: testhelpers.ServerHost(server.URL)},
		method:   http.MethodPost,
		failures: 100,
		err:      &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
	a := newRetryTestAction(server, nil, transport)

	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error once the attempts are used up")
	}
	transport.mu.Lock()
	attempts := transport.seen
	transport.mu.Unlock()
	if attempts != triggerMaxAttempts {
		t.Errorf("expected %d attempts, got %d", triggerMaxAttempts, attempts)
	}
}

// TestPipelineTriggerAction_Trigger_ThrottledRetried verifies that a throttled
// (429) trigger request is retried.
func TestPipelineTriggerAction_Trigger_ThrottledRetried(t *testing.T) {
	t.Parallel()

	srv := &scriptedServer{onPost: func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			writeStatus(w, http.StatusTooManyRequests)
			return
		}
		writeRun(w, "inProgress", "unknown")
	}}
	server := httptest.NewServer(srv)
	defer server.Close()

	a := newRetryTestAction(server, nil, nil)
	resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodPAT), 5*time.Second)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a throttled trigger should be retried, got: %s", diagnosticsText(resp))
	}
	if posts, _ := srv.counts(); posts != 2 {
		t.Errorf("expected 2 trigger requests, got %d", posts)
	}
}

// TestPipelineTriggerAction_Trigger_401RefreshesTokenOnce verifies that a 401
// on the trigger gets a new token and is retried once, and a second 401 fails.
func TestPipelineTriggerAction_Trigger_401RefreshesTokenOnce(t *testing.T) {
	t.Parallel()

	t.Run("then accepted", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: func(n int, w http.ResponseWriter) {
			if n == 1 {
				writeStatus(w, http.StatusUnauthorized)
				return
			}
			writeRun(w, "inProgress", "unknown")
		}}
		server := httptest.NewServer(srv)
		defer server.Close()

		credential := &countingCredential{lifetime: time.Hour}
		a := newRetryTestAction(server, credential, nil)
		resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodDAC), 5*time.Second)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected error: %s", diagnosticsText(resp))
		}
		headers := srv.authHeaders()
		if len(headers) != 2 || headers[0] != "Bearer token-1" || headers[1] != "Bearer token-2" {
			t.Errorf("expected the retry to use a new token, got %v", headers)
		}
	})

	t.Run("rejected again", func(t *testing.T) {
		t.Parallel()

		srv := &scriptedServer{onPost: func(_ int, w http.ResponseWriter) { writeStatus(w, http.StatusUnauthorized) }}
		server := httptest.NewServer(srv)
		defer server.Close()

		a := newRetryTestAction(server, &countingCredential{lifetime: time.Hour}, nil)
		resp, _ := invokeWithin(t, a, noWaitConfig(t, authMethodDAC), 5*time.Second)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected an error after two 401s")
		}
		if posts, _ := srv.counts(); posts != 2 {
			t.Errorf("expected 2 trigger requests, got %d", posts)
		}
	})
}
