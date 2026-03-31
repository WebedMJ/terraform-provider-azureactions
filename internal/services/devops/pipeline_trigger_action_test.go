// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

// Package devops provides mock-based unit tests for the DevOps service actions.
// Tests use net/http/httptest to stand up a fake Azure DevOps Pipelines REST
// API so no real Azure DevOps credentials are required.
package devops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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

// newDevOpsTestClient returns a *clients.Client suitable for unit tests.
func newDevOpsTestClient() *clients.Client {
	return &clients.Client{
		Account: clients.Account{
			SubscriptionId: "test-subscription-id",
			TenantId:       "test-tenant-id",
			ClientId:       "test-client-id",
			Environment:    "public",
		},
		Config: clients.Config{
			SubscriptionID: "test-subscription-id",
			TenantID:       "test-tenant-id",
			ClientID:       "test-client-id",
			ClientSecret:   "test-secret",
			Environment:    "public",
		},
		Environment: &environments.Environment{
			Name:            "test",
			ResourceManager: environments.ResourceManagerAPI("http://localhost"),
		},
		Authorizer: &devOpsMockAuthorizer{},
	}
}

// newTestAction returns a PipelineTriggerAction pre-configured with:
//   - the test client (for service_principal auth)
//   - an HTTP client whose transport rewrites the host to the test server
//   - a short poll interval so wait_for_completion tests finish quickly
func newTestAction(server *httptest.Server) *PipelineTriggerAction {
	a := &PipelineTriggerAction{
		httpClient: &http.Client{
			Transport: &hostRewriteTransport{host: serverHost(server.URL)},
		},
		pollInterval: 50 * time.Millisecond,
	}
	req := action.ConfigureRequest{ProviderData: newDevOpsTestClient()}
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
	orgURL, project string,
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
		"organization_url":      tftypes.NewValue(tftypes.String, orgURL),
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
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pipelineRunJSON(42, "inProgress", "unknown"))
		case http.MethodGet:
			callCount++
			if callCount == 1 {
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

	for _, attr := range []string{"organization_url", "project", "pipeline_id", "auth_method"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
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
		"https://dev.azure.com/myorg", "my-project", 1,
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
		"https://dev.azure.com/myorg", "my-project", 1,
		authMethodSP, "", "",
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
		"https://dev.azure.com/myorg", "my-project", 1,
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
		"https://dev.azure.com/myorg", "my-project", 1,
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
		"https://dev.azure.com/myorg", "my-project", 1,
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
		"https://dev.azure.com/myorg", "my-project", 1,
		"oauth2_legacy", "", "", // unsupported auth method
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for invalid auth_method, got none")
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
		"https://dev.azure.com/myorg", "my-project", 1,
		authMethodPAT, "", "", // no PAT provided
		nil, nil,
	)
	resp, _ := invokeDevOpsAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for missing PAT, got none")
	}
}
