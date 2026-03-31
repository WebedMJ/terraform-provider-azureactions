// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

// Package automation provides mock-based unit tests for the automation service
// actions.  Tests use net/http/httptest to stand up a fake Azure Automation REST
// API so no real Azure credentials are required.
package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// mockAuthorizer implements auth.Authorizer using a static bearer token.
type mockAuthorizer struct{}

func (m *mockAuthorizer) Token(_ context.Context, _ *http.Request) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "mock-token", TokenType: "Bearer"}, nil
}

func (m *mockAuthorizer) AuxiliaryTokens(_ context.Context, _ *http.Request) ([]*oauth2.Token, error) {
	return nil, nil
}

// newTestClient creates a *clients.Client whose ResourceManager endpoint points
// at the supplied httptest server URL.
func newTestClient(serverURL string) *clients.Client {
	return &clients.Client{
		Account: clients.Account{
			SubscriptionID: "test-subscription-id",
			TenantID:       "test-tenant-id",
			ClientID:       "test-client-id",
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
			Name: "test",
			// No trailing slash: the go-azure-sdk client concatenates the base URI
			// directly with the resource path (which starts with "/"), so adding a
			// trailing slash here would produce a double-slash in the URL.
			ResourceManager: environments.ResourceManagerAPI(serverURL),
		},
		Authorizer: &mockAuthorizer{},
	}
}

// newTestAction builds a RunbookTriggerAction with a short poll interval and
// the test client pre-configured.
func newTestAction(serverURL string) *RunbookTriggerAction {
	a := &RunbookTriggerAction{
		pollInterval: 50 * time.Millisecond,
	}
	req := action.ConfigureRequest{ProviderData: newTestClient(serverURL)}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)
	return a
}

// buildConfig constructs a tfsdk.Config for the runbook trigger action using the
// provided test values.
func buildConfig(
	t *testing.T,
	automationAccount, resourceGroup, runbookName string,
	waitForCompletion *bool,
	timeoutMinutes *int64,
) tfsdk.Config {
	t.Helper()

	ctx := context.Background()

	a := &RunbookTriggerAction{}
	schemaResp := &action.SchemaResponse{}
	a.Schema(ctx, action.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", schemaResp.Diagnostics)
	}

	schema := schemaResp.Schema
	schemaType := schema.Type().TerraformType(ctx)

	var waitVal tftypes.Value
	if waitForCompletion != nil {
		waitVal = tftypes.NewValue(tftypes.Bool, *waitForCompletion)
	} else {
		waitVal = tftypes.NewValue(tftypes.Bool, nil)
	}

	var timeoutVal tftypes.Value
	if timeoutMinutes != nil {
		timeoutVal = tftypes.NewValue(tftypes.Number, *timeoutMinutes)
	} else {
		timeoutVal = tftypes.NewValue(tftypes.Number, nil)
	}

	rawValue := tftypes.NewValue(schemaType, map[string]tftypes.Value{
		"automation_account_name": tftypes.NewValue(tftypes.String, automationAccount),
		"resource_group_name":     tftypes.NewValue(tftypes.String, resourceGroup),
		"runbook_name":            tftypes.NewValue(tftypes.String, runbookName),
		"parameters":              tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"wait_for_completion":     waitVal,
		"timeout_minutes":         timeoutVal,
	})

	return tfsdk.Config{
		Raw:    rawValue,
		Schema: schema,
	}
}

// invokeAction invokes the RunbookTriggerAction and returns the response.
// A timeout of 10 seconds is applied to the context so the SDK does not
// reject a context without a deadline.
func invokeAction(t *testing.T, a *RunbookTriggerAction, cfg tfsdk.Config) (*action.InvokeResponse, []string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var progressMessages []string
	resp := &action.InvokeResponse{
		SendProgress: func(e action.InvokeProgressEvent) {
			progressMessages = append(progressMessages, e.Message)
		},
	}

	a.Invoke(ctx, action.InvokeRequest{Config: cfg}, resp)
	return resp, progressMessages
}

// ----------------------------------------------------------------------------
// Mock server helpers
// ----------------------------------------------------------------------------

// jobResponse returns a minimal Azure Automation Job JSON body.
func jobResponse(status string) []byte {
	body := map[string]interface{}{
		"id":   "/subscriptions/test-subscription-id/resourceGroups/test-rg/providers/Microsoft.Automation/automationAccounts/test-account/jobs/terraform-action-1",
		"name": "terraform-action-1",
		"properties": map[string]interface{}{
			"jobId":  "abc123",
			"status": status,
			"runbook": map[string]string{
				"name": "TestRunbook",
			},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// newJobMuxPUT builds an http.ServeMux that responds to the PUT (create) job
// request with the given status code and body.
func newJobMuxPUT(statusCode int, body []byte) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/jobs/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

// newJobMuxWithStatus returns a mux that:
//   - Responds to PUT (create job) with a "New" job
//   - Responds to GET (poll) with the supplied final status
func newJobMuxWithStatus(finalStatus string) *http.ServeMux {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(jobResponse("New"))
		case http.MethodGet:
			callCount++
			// Return "Running" on first poll, final status on subsequent
			if callCount == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(jobResponse("Running"))
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(jobResponse(finalStatus))
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

// TestRunbookTriggerAction_Schema verifies the action schema is populated
// correctly without making any network calls.
func TestRunbookTriggerAction_Schema(t *testing.T) {
	t.Parallel()

	a := &RunbookTriggerAction{}
	resp := &action.SchemaResponse{}
	a.Schema(context.Background(), action.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema diagnostics: %v", resp.Diagnostics)
	}

	required := []string{"automation_account_name", "resource_group_name", "runbook_name"}
	for _, attr := range required {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected required attribute %q to be present in schema", attr)
		}
	}
}

// TestRunbookTriggerAction_Metadata verifies the TypeName is set correctly.
func TestRunbookTriggerAction_Metadata(t *testing.T) {
	t.Parallel()

	a := &RunbookTriggerAction{}
	resp := &action.MetadataResponse{}
	a.Metadata(context.Background(), action.MetadataRequest{}, resp)

	if resp.TypeName != "azureactions_automation_runbook_trigger" {
		t.Errorf("expected TypeName %q, got %q", "azureactions_automation_runbook_trigger", resp.TypeName)
	}
}

// TestRunbookTriggerAction_Invoke_FireAndForget tests triggering a runbook
// without waiting for completion.
func TestRunbookTriggerAction_Invoke_FireAndForget(t *testing.T) {
	t.Parallel()

	mux := newJobMuxPUT(http.StatusCreated, jobResponse("New"))
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server.URL)
	cfg := buildConfig(t, "test-account", "test-rg", "TestRunbook", nil, nil)
	resp, progress := invokeAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
	if len(progress) == 0 {
		t.Error("expected at least one progress message")
	}
}

// TestRunbookTriggerAction_Invoke_WaitForCompletion tests triggering a runbook
// and waiting for it to complete successfully.
func TestRunbookTriggerAction_Invoke_WaitForCompletion(t *testing.T) {
	t.Parallel()

	mux := newJobMuxWithStatus("Completed")
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server.URL)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildConfig(t, "test-account", "test-rg", "TestRunbook", &waitTrue, &timeoutMins)
	resp, _ := invokeAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
}

// TestRunbookTriggerAction_Invoke_JobFailed tests that a failed runbook job
// surfaces an error diagnostic.
func TestRunbookTriggerAction_Invoke_JobFailed(t *testing.T) {
	t.Parallel()

	mux := newJobMuxWithStatus("Failed")
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server.URL)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildConfig(t, "test-account", "test-rg", "TestRunbook", &waitTrue, &timeoutMins)
	resp, _ := invokeAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for failed job, got none")
	}
}

// TestRunbookTriggerAction_Invoke_JobStopped tests that a stopped runbook job
// surfaces an error diagnostic.
func TestRunbookTriggerAction_Invoke_JobStopped(t *testing.T) {
	t.Parallel()

	mux := newJobMuxWithStatus("Stopped")
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server.URL)
	waitTrue := true
	timeoutMins := int64(1)
	cfg := buildConfig(t, "test-account", "test-rg", "TestRunbook", &waitTrue, &timeoutMins)
	resp, _ := invokeAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for stopped job, got none")
	}
}

// TestRunbookTriggerAction_Invoke_APIError tests that an Azure API error during
// job creation is reported as a diagnostic error.
func TestRunbookTriggerAction_Invoke_APIError(t *testing.T) {
	t.Parallel()

	errorBody := []byte(`{"error":{"code":"ResourceNotFound","message":"Automation account not found"}}`)
	mux := newJobMuxPUT(http.StatusNotFound, errorBody)
	server := httptest.NewServer(mux)
	defer server.Close()

	a := newTestAction(server.URL)
	cfg := buildConfig(t, "nonexistent-account", "test-rg", "TestRunbook", nil, nil)
	resp, _ := invokeAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for API 404, got none")
	}
}

// TestRunbookTriggerAction_Configure_InvalidProviderData verifies that passing
// incorrect ProviderData sets a diagnostic error.
func TestRunbookTriggerAction_Configure_InvalidProviderData(t *testing.T) {
	t.Parallel()

	a := &RunbookTriggerAction{}
	req := action.ConfigureRequest{ProviderData: "this-is-not-a-client"}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected diagnostics error for invalid provider data, got none")
	}
}

// TestRunbookTriggerAction_Configure_NilProviderData verifies that nil
// ProviderData is handled gracefully (no error, client stays nil).
func TestRunbookTriggerAction_Configure_NilProviderData(t *testing.T) {
	t.Parallel()

	a := &RunbookTriggerAction{}
	req := action.ConfigureRequest{ProviderData: nil}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("unexpected diagnostics: %v", resp.Diagnostics)
	}
}

// Ensure the mock authorizer satisfies the interface at compile time.
var _ interface {
	Token(_ context.Context, _ *http.Request) (*oauth2.Token, error)
	AuxiliaryTokens(_ context.Context, _ *http.Request) ([]*oauth2.Token, error)
} = (*mockAuthorizer)(nil)
