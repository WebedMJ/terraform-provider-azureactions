// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

// Package devops provides mock-based unit tests for the DevOps service actions.
// Tests use net/http/httptest to stand up a fake Azure DevOps Pipelines REST
// API so no real Azure DevOps credentials are required.
package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"golang.org/x/oauth2"
)

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

// devOpsMockAuthorizer implements auth.Authorizer using a static bearer token.
type devOpsMockAuthorizer struct{}

func (m *devOpsMockAuthorizer) Token(_ context.Context, _ *http.Request) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "mock-devops-token", TokenType: "Bearer"}, nil
}

func (m *devOpsMockAuthorizer) AuxiliaryTokens(_ context.Context, _ *http.Request) ([]*oauth2.Token, error) {
	return nil, nil
}

type mockAzureTokenCredential struct {
	token  string
	scopes []string
}

func (m *mockAzureTokenCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	m.scopes = append([]string{}, opts.Scopes...)
	if m.token == "" {
		return azcore.AccessToken{}, fmt.Errorf("mock token is empty")
	}

	return azcore.AccessToken{
		Token:     m.token,
		ExpiresOn: time.Now().Add(1 * time.Hour),
	}, nil
}

// newDevOpsTestClient returns a *clients.Client suitable for unit tests.
func newDevOpsTestClient(organizationURL string) *clients.Client {
	credential := &mockAzureTokenCredential{token: "mock-devops-credential-token"}

	return &clients.Client{
		Account: clients.Account{
			SubscriptionID: "test-subscription-id",
			TenantID:       "test-tenant-id",
			ClientID:       "test-client-id",
			Environment:    "public",
		},
		Config: clients.Config{
			SubscriptionID:  "test-subscription-id",
			TenantID:        "test-tenant-id",
			ClientID:        "test-client-id",
			ClientSecret:    "test-secret",
			Environment:     "public",
			OrganizationURL: organizationURL,
		},
		Environment: &environments.Environment{
			Name:            "test",
			ResourceManager: environments.ResourceManagerAPI("http://localhost"),
		},
		Authorizer: &devOpsMockAuthorizer{},
		Credential: credential,
	}
}

// newTestAction returns a PipelineTriggerAction pre-configured with:
//   - the test client (for service_principal auth)
//   - an HTTP client whose transport rewrites the host to the test server
//   - a short poll interval so wait_for_completion tests finish quickly
//   - a mock DevOps authorizer factory to avoid real Azure AD calls in SP auth tests
func newTestAction(server *httptest.Server) *PipelineTriggerAction {
	a := &PipelineTriggerAction{
		httpClient: &http.Client{
			Transport: &hostRewriteTransport{host: serverHost(server.URL)},
		},
		pollInterval: 50 * time.Millisecond,
	}
	req := action.ConfigureRequest{ProviderData: newDevOpsTestClient("https://dev.azure.com/myorg")}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)
	return a
}

// serverHost strips the "http://" prefix from a URL to get just "host:port".
func serverHost(rawURL string) string {
	const prefix = "http://"
	if len(rawURL) > len(prefix) && rawURL[:len(prefix)] == prefix {
		return rawURL[len(prefix):]
	}
	return rawURL
}

// hostRewriteTransport rewrites every outgoing request's host so it reaches
// the test server, regardless of what URL the action code constructs.
type hostRewriteTransport struct {
	host string // e.g. "127.0.0.1:PORT"
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	clone.Host = t.host
	return http.DefaultTransport.RoundTrip(clone)
}

// buildDevOpsConfig constructs a tfsdk.Config for the pipeline trigger action.
func buildDevOpsConfig(
	t *testing.T,
	project string,
	pipelineID int64,
	authMethod, pat, branchRef string,
	waitForCompletion *bool,
	timeoutMins *int64,
) tfsdk.Config {
	t.Helper()

	ctx := context.Background()
	a := &PipelineTriggerAction{}
	schemaResp := &action.SchemaResponse{}
	a.Schema(ctx, action.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %v", schemaResp.Diagnostics)
	}

	schema := schemaResp.Schema
	schemaType := schema.Type().TerraformType(ctx)

	var patVal tftypes.Value
	if pat != "" {
		patVal = tftypes.NewValue(tftypes.String, pat)
	} else {
		patVal = tftypes.NewValue(tftypes.String, nil)
	}

	var branchVal tftypes.Value
	if branchRef != "" {
		branchVal = tftypes.NewValue(tftypes.String, branchRef)
	} else {
		branchVal = tftypes.NewValue(tftypes.String, nil)
	}

	var waitVal tftypes.Value
	if waitForCompletion != nil {
		waitVal = tftypes.NewValue(tftypes.Bool, *waitForCompletion)
	} else {
		waitVal = tftypes.NewValue(tftypes.Bool, nil)
	}

	var timeoutVal tftypes.Value
	if timeoutMins != nil {
		timeoutVal = tftypes.NewValue(tftypes.Number, *timeoutMins)
	} else {
		timeoutVal = tftypes.NewValue(tftypes.Number, nil)
	}

	rawValue := tftypes.NewValue(schemaType, map[string]tftypes.Value{
		"project":               tftypes.NewValue(tftypes.String, project),
		"pipeline_id":           tftypes.NewValue(tftypes.Number, pipelineID),
		"auth_method":           tftypes.NewValue(tftypes.String, authMethod),
		"personal_access_token": patVal,
		"branch_ref":            branchVal,
		"variables":             tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"template_parameters":   tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"stages_to_skip":        tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"wait_for_completion":   waitVal,
		"timeout_minutes":       timeoutVal,
	})

	return tfsdk.Config{Raw: rawValue, Schema: schema}
}

// invokeDevOpsAction invokes the PipelineTriggerAction and captures the results.
func invokeDevOpsAction(t *testing.T, a *PipelineTriggerAction, cfg tfsdk.Config) (*action.InvokeResponse, []string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var progress []string
	resp := &action.InvokeResponse{
		SendProgress: func(e action.InvokeProgressEvent) {
			progress = append(progress, e.Message)
		},
	}
	a.Invoke(ctx, action.InvokeRequest{Config: cfg}, resp)
	return resp, progress
}

// pipelineRunJSON returns a minimal pipeline run JSON body.
func pipelineRunJSON(id int, state, result string) []byte {
	run := pipelineRunResponse{ID: id, Name: "20240101.1", State: state, Result: result}
	b, _ := json.Marshal(run)
	return b
}

// newPipelineMux builds an http.ServeMux that handles the DevOps pipeline run
// trigger and status endpoints.  On first GET it returns "inProgress"; on
// subsequent GETs it returns the provided terminal state/result.
func newPipelineMux(runState, runResult string) *http.ServeMux {
	var mu sync.Mutex
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pipelineRunJSON(42, "inProgress", "unknown"))
		case http.MethodGet:
			mu.Lock()
			callCount++
			cnt := callCount
			mu.Unlock()
			if cnt == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(pipelineRunJSON(42, "inProgress", "unknown"))
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(pipelineRunJSON(42, runState, runResult))
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestPipelineTriggerAction_Schema verifies all expected attributes are present.
func TestPipelineTriggerAction_Schema(t *testing.T) {
	t.Parallel()

	a := &PipelineTriggerAction{}
	resp := &action.SchemaResponse{}
	a.Schema(context.Background(), action.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema diagnostics: %v", resp.Diagnostics)
	}

	for _, attr := range []string{"project", "pipeline_id", "auth_method"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	if _, ok := resp.Schema.Attributes["organization_url"]; ok {
		t.Error("organization_url must not be configurable on devops action")
	}

	if _, ok := resp.Schema.Attributes["personal_access_token"]; !ok {
		t.Error("personal_access_token attribute missing from schema")
	}
}

// TestPipelineTriggerAction_Metadata verifies the TypeName is correct.
func TestPipelineTriggerAction_Metadata(t *testing.T) {
	t.Parallel()

	a := &PipelineTriggerAction{}
	resp := &action.MetadataResponse{}
	a.Metadata(context.Background(), action.MetadataRequest{}, resp)

	if resp.TypeName != "azureactions_devops_pipeline_trigger" {
		t.Errorf("expected TypeName %q, got %q", "azureactions_devops_pipeline_trigger", resp.TypeName)
	}
}

// TestPipelineTriggerAction_Invoke_PAT_Success tests a successful pipeline
// trigger using PAT authentication.
func TestPipelineTriggerAction_Invoke_PAT_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat-token", "",
		nil, nil,
	)
	resp, progress := invokeDevOpsAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
	if len(progress) == 0 {
		t.Error("expected at least one progress message")
	}
}

// TestPipelineTriggerAction_Invoke_ServicePrincipal_Success tests triggering
// a pipeline using service principal authentication.
func TestPipelineTriggerAction_Invoke_ServicePrincipal_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodSP, "", "",
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
}

// TestPipelineTriggerAction_Invoke_DefaultAzureCredential_Success tests triggering
// a pipeline using the DefaultAzureCredential-backed auth alias.
func TestPipelineTriggerAction_Invoke_DefaultAzureCredential_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodDAC, "", "",
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
}

// TestPipelineTriggerAction_Invoke_WaitForCompletion_Succeeded tests that
// wait_for_completion works when the run succeeds.
func TestPipelineTriggerAction_Invoke_WaitForCompletion_Succeeded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat", "",
		&waitTrue, &timeoutMins,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
}

// TestPipelineTriggerAction_Invoke_WaitForCompletion_Failed tests that a
// failed pipeline run surfaces an error diagnostic.
func TestPipelineTriggerAction_Invoke_WaitForCompletion_Failed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "failed"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat", "",
		&waitTrue, &timeoutMins,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for failed pipeline run, got none")
	}
}

// TestPipelineTriggerAction_Invoke_APIError tests that an HTTP error from the
// DevOps API surfaces as a diagnostic error.
func TestPipelineTriggerAction_Invoke_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"TF400813: Resource not authorized."}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "invalid-pat", "",
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for 401 response, got none")
	}
}

// TestPipelineTriggerAction_Invoke_InvalidAuthMethod tests that an invalid
// auth_method value surfaces as a diagnostic error.
func TestPipelineTriggerAction_Invoke_InvalidAuthMethod(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		"oauth2_legacy", "", "", // unsupported auth method
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for invalid auth_method, got none")
	}
}

// TestPipelineTriggerAction_HTTPClient_Timeout tests that the HTTP client
// enforces timeouts on slow/unresponsive servers. This prevents indefinite
// hangs during terraform apply.
func TestPipelineTriggerAction_HTTPClient_Timeout(t *testing.T) {
	t.Parallel()

	// Server delays every response well beyond the client timeout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Use a short-timeout client that routes requests to the test server via
	// hostRewriteTransport so the delay is reliably triggered.
	shortTimeoutClient := &http.Client{
		Timeout:   100 * time.Millisecond,
		Transport: &hostRewriteTransport{host: serverHost(server.URL)},
	}
	a := &PipelineTriggerAction{
		httpClient:   shortTimeoutClient,
		pollInterval: 50 * time.Millisecond,
	}
	req := action.ConfigureRequest{ProviderData: newDevOpsTestClient("https://dev.azure.com/myorg")}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)

	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat-token", "",
		nil, nil,
	)

	invokeResp, _ := invokeDevOpsAction(t, a, cfg)

	if !invokeResp.Diagnostics.HasError() {
		t.Error("expected diagnostics error due to HTTP client timeout, got none")
	}
}

// TestPipelineTriggerAction_HTTPClient_UsesContextTimeoutModel verifies that
// the default HTTP client relies on request context for total timeout while
// still configuring transport-level timeouts.
func TestPipelineTriggerAction_HTTPClient_UsesContextTimeoutModel(t *testing.T) {
	t.Parallel()

	a := &PipelineTriggerAction{}
	client := a.getHTTPClient()

	if client == nil {
		t.Fatal("getHTTPClient returned nil")
	}

	// Total request timeout should be controlled by context, not a fixed client timeout.
	if client.Timeout != 0 {
		t.Errorf("HTTP client timeout is %v; expected 0 (context-driven timeout model)", client.Timeout)
	}

	// Verify that Transport is configured (including dial/TLS timeouts)
	if client.Transport == nil {
		t.Error("HTTP client Transport is nil; should have custom transport with timeouts")
	}
}

// TestPipelineTriggerAction_Invoke_PAT_Missing tests that omitting a PAT when
// auth_method = "pat" surfaces an error.
func TestPipelineTriggerAction_Invoke_PAT_Missing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "", "", // no PAT provided
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for missing PAT, got none")
	}
}

// TestPipelineTriggerAction_ResolveAuthHeader_UsesFixedScope verifies that
// Azure DevOps auth always requests token using the well-known .default scope.
func TestPipelineTriggerAction_ResolveAuthHeader_UsesFixedScope(t *testing.T) {
	t.Parallel()

	credential := &mockAzureTokenCredential{token: "fallback-devops-token"}
	a := &PipelineTriggerAction{
		ActionMetadata: sdk.ActionMetadata{
			Client: &clients.Client{
				Environment: &environments.Environment{Name: "test"},
				Credential:  credential,
				Config: clients.Config{
					OrganizationURL: "https://dev.azure.com/myorg",
				},
			},
		},
	}

	header, err := a.resolveAuthHeader(context.Background(), PipelineTriggerActionModel{
		AuthMethod: types.StringValue(authMethodDAC),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if header != "Bearer fallback-devops-token" {
		t.Fatalf("expected Bearer header with fallback token, got: %q", header)
	}

	if len(credential.scopes) != 1 || credential.scopes[0] != devOpsTokenScope {
		t.Fatalf("expected scope %q, got: %#v", devOpsTokenScope, credential.scopes)
	}
}

func TestPipelineTriggerAction_Invoke_MissingProviderOrganizationURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := &PipelineTriggerAction{
		httpClient: &http.Client{
			Transport: &hostRewriteTransport{host: serverHost(server.URL)},
		},
		pollInterval: 50 * time.Millisecond,
	}
	req := action.ConfigureRequest{ProviderData: newDevOpsTestClient("")}
	respCfg := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, respCfg)

	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodDAC, "", "",
		nil, nil,
	)

	resp, _ := invokeDevOpsAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for missing provider organization_url, got none")
	}
}

// TestPipelineTriggerAction_Invoke_InvalidPipelineID tests that a pipeline_id
// of zero or negative surfaces a validation error diagnostic.
func TestPipelineTriggerAction_Invoke_InvalidPipelineID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newTestAction(server)
	cfg := buildDevOpsConfig(t,
		"my-project", 0, // invalid: must be > 0
		authMethodPAT, "my-pat-token", "",
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for pipeline_id=0, got none")
	}
}

// TestPipelineTriggerAction_Invoke_InvalidTimeout tests that a timeout_minutes
// value less than 1 surfaces an error diagnostic.
func TestPipelineTriggerAction_Invoke_InvalidTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "succeeded"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	zeroTimeout := int64(0)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat-token", "",
		&waitTrue, &zeroTimeout,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for timeout_minutes=0, got none")
	}
}

// TestPipelineTriggerAction_Invoke_WaitForCompletion_Canceled tests that a
// canceled pipeline run surfaces an error diagnostic.
func TestPipelineTriggerAction_Invoke_WaitForCompletion_Canceled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "canceled"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat", "",
		&waitTrue, &timeoutMins,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for canceled pipeline run, got none")
	}
}

// TestPipelineTriggerAction_Invoke_WaitForCompletion_UnknownResult tests that
// a run completing with an unrecognised result value surfaces an error.
func TestPipelineTriggerAction_Invoke_WaitForCompletion_UnknownResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newPipelineMux("completed", "partiallySucceeded"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat", "",
		&waitTrue, &timeoutMins,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for unrecognised pipeline run result, got none")
	}
}

// TestPipelineTriggerAction_Invoke_PollTimeout tests that the action surfaces a
// diagnostic error when the polling context expires before the run completes.
func TestPipelineTriggerAction_Invoke_PollTimeout(t *testing.T) {
	t.Parallel()

	// Server always returns "inProgress" — run never completes.
	server := httptest.NewServer(newPipelineMux("inProgress", "unknown"))
	defer server.Close()

	a := newTestAction(server)
	waitTrue := true
	timeoutMins := int64(1) // minimum valid; parent context expires well before this
	cfg := buildDevOpsConfig(t,
		"my-project", 1,
		authMethodPAT, "my-pat-token", "",
		&waitTrue, &timeoutMins,
	)

	// A 300ms outer context is inherited by the action's internal WithTimeout
	// (min(300ms, 1min) → 300ms). With a 50ms poll interval this gives ~5 polls
	// before the deadline, exercising the ctx.Done() branch.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var progress []string
	resp := &action.InvokeResponse{
		SendProgress: func(e action.InvokeProgressEvent) {
			progress = append(progress, e.Message)
		},
	}
	a.Invoke(ctx, action.InvokeRequest{Config: cfg}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for poll timeout, got none")
	}
}

// TestPipelineTriggerAction_Invoke_WithVariables tests that variables,
// template_parameters, and stages_to_skip are serialised into the request body.
func TestPipelineTriggerAction_Invoke_WithVariables(t *testing.T) {
	t.Parallel()

	var capturedBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var err error
			capturedBody, err = io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pipelineRunJSON(42, "inProgress", "unknown"))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server)

	ctx := context.Background()
	schemaResp := &action.SchemaResponse{}
	a.Schema(ctx, action.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema
	rawValue := tftypes.NewValue(schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"project":               tftypes.NewValue(tftypes.String, "my-project"),
		"pipeline_id":           tftypes.NewValue(tftypes.Number, int64(1)),
		"auth_method":           tftypes.NewValue(tftypes.String, authMethodPAT),
		"personal_access_token": tftypes.NewValue(tftypes.String, "my-pat"),
		"branch_ref":            tftypes.NewValue(tftypes.String, "refs/heads/main"),
		"variables": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"Env": tftypes.NewValue(tftypes.String, "staging"),
		}),
		"template_parameters": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"deployTarget": tftypes.NewValue(tftypes.String, "eastus"),
		}),
		"stages_to_skip": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "IntegrationTests"),
		}),
		"wait_for_completion": tftypes.NewValue(tftypes.Bool, nil),
		"timeout_minutes":     tftypes.NewValue(tftypes.Number, nil),
	})
	cfg := tfsdk.Config{Raw: rawValue, Schema: schema}

	resp, _ := invokeDevOpsAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
	if capturedBody == nil {
		t.Fatal("expected POST request body to be captured")
	}
	body := string(capturedBody)
	if !strings.Contains(body, `"Env"`) || !strings.Contains(body, `"staging"`) {
		t.Errorf("expected variable Env=staging in request body, got: %s", body)
	}
	if !strings.Contains(body, `"deployTarget"`) || !strings.Contains(body, `"eastus"`) {
		t.Errorf("expected templateParameter deployTarget=eastus in request body, got: %s", body)
	}
	if !strings.Contains(body, `"stagesToSkip"`) || !strings.Contains(body, `"IntegrationTests"`) {
		t.Errorf("expected stagesToSkip IntegrationTests in request body, got: %s", body)
	}
}

// TestPipelineTriggerAction_Configure_InvalidProviderData verifies that passing
// incorrect ProviderData sets a diagnostic error.
func TestPipelineTriggerAction_Configure_InvalidProviderData(t *testing.T) {
	t.Parallel()

	a := &PipelineTriggerAction{}
	req := action.ConfigureRequest{ProviderData: "not-a-client"}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for invalid provider data, got none")
	}
}
