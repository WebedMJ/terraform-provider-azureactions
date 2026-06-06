// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package eventgrid

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
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"golang.org/x/oauth2"
)

// eventGridMockAuthorizer implements auth.Authorizer using a static bearer token.
type eventGridMockAuthorizer struct{}

func (m *eventGridMockAuthorizer) Token(_ context.Context, _ *http.Request) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "mock-token", TokenType: "Bearer"}, nil
}

func (m *eventGridMockAuthorizer) AuxiliaryTokens(_ context.Context, _ *http.Request) ([]*oauth2.Token, error) {
	return nil, nil
}

type mockEventGridTokenCredential struct {
	token  string
	scopes []string
	err    error
}

func (m *mockEventGridTokenCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	m.scopes = append([]string{}, opts.Scopes...)
	if m.err != nil {
		return azcore.AccessToken{}, m.err
	}
	if m.token == "" {
		return azcore.AccessToken{}, fmt.Errorf("mock token is empty")
	}

	return azcore.AccessToken{
		Token:     m.token,
		ExpiresOn: time.Now().Add(1 * time.Hour),
	}, nil
}

func newEventGridTestClient(credential azcore.TokenCredential) *clients.Client {
	if credential == nil {
		credential = &mockEventGridTokenCredential{token: "mock-eventgrid-token"}
	}

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
			Name:            "test",
			ResourceManager: environments.ResourceManagerAPI("http://localhost"),
		},
		Authorizer: &eventGridMockAuthorizer{},
		Credential: credential,
	}
}

// hostRewriteTransport rewrites outgoing host to the test server.
type hostRewriteTransport struct {
	host string
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	clone.Host = t.host
	return http.DefaultTransport.RoundTrip(clone)
}

func serverHost(rawURL string) string {
	const prefix = "http://"
	if strings.HasPrefix(rawURL, prefix) {
		return rawURL[len(prefix):]
	}
	return rawURL
}

func newPublishAction(server *httptest.Server, cred azcore.TokenCredential) *PublishEventAction {
	a := &PublishEventAction{
		httpClient: &http.Client{
			Transport: &hostRewriteTransport{host: serverHost(server.URL)},
		},
	}
	req := action.ConfigureRequest{ProviderData: newEventGridTestClient(cred)}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)
	return a
}

type testCloudEvent struct {
	SpecVersion          *string
	ID                   *string
	Source               string
	Type                 string
	Subject              *string
	Time                 *string
	DataContentType      *string
	Data                 map[string]string
	DataBase64           *string
	CloudEventExtensions map[string]string
}

func buildPublishConfig(
	t *testing.T,
	endpointURL string,
	authMethod *string,
	accessKey *string,
	sasToken *string,
	cloudEvents []testCloudEvent,
	contentType *string,
	timeoutSeconds *int64,
) tfsdk.Config {
	t.Helper()

	ctx := context.Background()
	a := &PublishEventAction{}
	schemaResp := &action.SchemaResponse{}
	a.Schema(ctx, action.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %v", schemaResp.Diagnostics)
	}

	schema := schemaResp.Schema
	schemaType := schema.Type().TerraformType(ctx)

	asStringOrNull := func(v *string) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *v)
	}
	asIntOrNull := func(v *int64) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.Number, nil)
		}
		return tftypes.NewValue(tftypes.Number, *v)
	}

	cloudEventObjType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"specversion":            tftypes.String,
		"id":                     tftypes.String,
		"source":                 tftypes.String,
		"type":                   tftypes.String,
		"subject":                tftypes.String,
		"time":                   tftypes.String,
		"datacontenttype":        tftypes.String,
		"data":                   tftypes.Map{ElementType: tftypes.String},
		"data_base64":            tftypes.String,
		"cloud_event_extensions": tftypes.Map{ElementType: tftypes.String},
	}}

	asMapString := func(m map[string]string) tftypes.Value {
		if m == nil {
			return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil)
		}

		vals := map[string]tftypes.Value{}
		for k, v := range m {
			vals[k] = tftypes.NewValue(tftypes.String, v)
		}

		return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, vals)
	}

	cloudEventVals := make([]tftypes.Value, 0, len(cloudEvents))
	for _, event := range cloudEvents {
		asOptionalString := func(v *string) tftypes.Value {
			if v == nil {
				return tftypes.NewValue(tftypes.String, nil)
			}
			return tftypes.NewValue(tftypes.String, *v)
		}

		cloudEventVals = append(cloudEventVals, tftypes.NewValue(cloudEventObjType, map[string]tftypes.Value{
			"specversion":            asOptionalString(event.SpecVersion),
			"id":                     asOptionalString(event.ID),
			"source":                 tftypes.NewValue(tftypes.String, event.Source),
			"type":                   tftypes.NewValue(tftypes.String, event.Type),
			"subject":                asOptionalString(event.Subject),
			"time":                   asOptionalString(event.Time),
			"datacontenttype":        asOptionalString(event.DataContentType),
			"data":                   asMapString(event.Data),
			"data_base64":            asOptionalString(event.DataBase64),
			"cloud_event_extensions": asMapString(event.CloudEventExtensions),
		}))
	}

	rawValue := tftypes.NewValue(schemaType, map[string]tftypes.Value{
		"endpoint_url":    tftypes.NewValue(tftypes.String, endpointURL),
		"auth_method":     asStringOrNull(authMethod),
		"access_key":      asStringOrNull(accessKey),
		"sas_token":       asStringOrNull(sasToken),
		"cloud_event":     tftypes.NewValue(tftypes.List{ElementType: cloudEventObjType}, cloudEventVals),
		"content_type":    asStringOrNull(contentType),
		"timeout_seconds": asIntOrNull(timeoutSeconds),
	})

	return tfsdk.Config{Raw: rawValue, Schema: schema}
}

func invokePublishAction(t *testing.T, a *PublishEventAction, cfg tfsdk.Config) (*action.InvokeResponse, []string) {
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

func sampleCloudEventBlocks() []testCloudEvent {
	id := "evt-1"
	return []testCloudEvent{
		{
			ID:     &id,
			Source: "/tests/eventgrid",
			Type:   "com.webedmj.event.created",
			Data: map[string]string{
				"message": "hello",
			},
		},
	}
}

func sampleCloudEventBlocksWithoutID() []testCloudEvent {
	return []testCloudEvent{
		{
			Source: "/tests/eventgrid",
			Type:   "com.webedmj.event.created",
			Data: map[string]string{
				"message": "hello",
			},
		},
	}
}

func TestPublishEventAction_Schema(t *testing.T) {
	t.Parallel()

	a := &PublishEventAction{}
	resp := &action.SchemaResponse{}
	a.Schema(context.Background(), action.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema diagnostics: %v", resp.Diagnostics)
	}

	for _, attr := range []string{"endpoint_url", "auth_method", "access_key", "sas_token"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	if _, ok := resp.Schema.Blocks["cloud_event"]; !ok {
		t.Fatal("expected cloud_event block in schema")
	}
}

func TestPublishEventAction_Metadata(t *testing.T) {
	t.Parallel()

	a := &PublishEventAction{}
	resp := &action.MetadataResponse{}
	a.Metadata(context.Background(), action.MetadataRequest{}, resp)

	if resp.TypeName != "azureactions_eventgrid_publish_event" {
		t.Errorf("expected TypeName %q, got %q", "azureactions_eventgrid_publish_event", resp.TypeName)
	}
}

func TestPublishEventAction_Invoke_DefaultAuthMethod_DACSuccess(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var authHeader string
	var contentType string
	var body []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()

	a := newPublishAction(server, &mockEventGridTokenCredential{token: "eventgrid-dac-token"})
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no diagnostics, got: %v", resp.Diagnostics)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(authHeader, "Bearer ") {
		t.Fatalf("expected Bearer auth header, got %q", authHeader)
	}
	if contentType != defaultContentType {
		t.Fatalf("expected content-type %q, got %q", defaultContentType, contentType)
	}
	if !strings.Contains(string(body), "specversion") {
		t.Fatalf("expected CloudEvents JSON body, got %s", string(body))
	}

	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected valid JSON payload, got error: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("expected one cloud event in payload, got %d", len(payload))
	}
	if payload[0]["id"] == nil || strings.TrimSpace(payload[0]["id"].(string)) == "" {
		t.Fatal("expected cloud event id to be present in payload")
	}
}

func TestPublishEventAction_Invoke_DefaultIDWhenOmitted(t *testing.T) {
	t.Parallel()

	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()

	a := newPublishAction(server, &mockEventGridTokenCredential{token: "eventgrid-dac-token"})
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocksWithoutID(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no diagnostics, got: %v", resp.Diagnostics)
	}

	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected valid JSON payload, got error: %v", err)
	}
	id, ok := payload[0]["id"].(string)
	if !ok || !strings.HasPrefix(id, "terraform-") {
		t.Fatalf("expected default id prefix terraform-, got %v", payload[0]["id"])
	}
}

func TestPublishEventAction_Invoke_AccessKeySuccess(t *testing.T) {
	t.Parallel()

	var headerVal string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerVal = r.Header.Get("aeg-sas-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	a := newPublishAction(server, nil)
	authMethod := authMethodKey
	accessKey := "my-access-key"
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		&authMethod, &accessKey, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
	if headerVal != accessKey {
		t.Fatalf("expected aeg-sas-key header %q, got %q", accessKey, headerVal)
	}
}

func TestPublishEventAction_Invoke_SASTokenSuccess(t *testing.T) {
	t.Parallel()

	var headerVal string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerVal = r.Header.Get("aeg-sas-token")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	a := newPublishAction(server, nil)
	authMethod := authMethodSAS
	sasToken := "r=https%3A%2F%2Fexample...&e=...&s=..."
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		&authMethod, nil, &sasToken,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no diagnostics, got: %v", resp.Diagnostics)
	}
	if headerVal != sasToken {
		t.Fatalf("expected aeg-sas-token header %q, got %q", sasToken, headerVal)
	}
}

func TestPublishEventAction_Invoke_InvalidAuthMethod(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	invalid := "bad_auth"
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		&invalid, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for invalid auth_method, got none")
	}
}

func TestPublishEventAction_Invoke_MissingAccessKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	authMethod := authMethodKey
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		&authMethod, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for missing access_key, got none")
	}
}

func TestPublishEventAction_Invoke_MissingSASToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	authMethod := authMethodSAS
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		&authMethod, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for missing sas_token, got none")
	}
}

func TestPublishEventAction_Invoke_InvalidEndpointURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	cfg := buildPublishConfig(t,
		"http://insecure.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for non-https endpoint_url, got none")
	}
}

func TestPublishEventAction_Invoke_DataAndDataBase64Conflict(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	dataBase64 := "aGVsbG8="
	id := "evt-1"
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		[]testCloudEvent{
			{
				ID:     &id,
				Source: "/tests/eventgrid",
				Type:   "com.webedmj.event.created",
				Data: map[string]string{
					"message": "hello",
				},
				DataBase64: &dataBase64,
			},
		},
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for conflicting cloud_event.data and data_base64, got none")
	}
}

func TestPublishEventAction_Invoke_MissingCloudEventBlocks(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		[]testCloudEvent{},
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for missing cloud_event blocks, got none")
	}
}

func TestPublishEventAction_Invoke_InvalidTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := newPublishAction(server, nil)
	zero := int64(0)
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, &zero,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for timeout_seconds=0, got none")
	}
}

func TestPublishEventAction_Invoke_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	a := newPublishAction(server, nil)
	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for API failure, got none")
	}
}

func TestPublishEventAction_Invoke_DACMissingCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	a := &PublishEventAction{
		httpClient: &http.Client{Transport: &hostRewriteTransport{host: serverHost(server.URL)}},
	}
	req := action.ConfigureRequest{ProviderData: newEventGridTestClient(nil)}
	cfgResp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, cfgResp)
	a.Client.Credential = nil

	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for missing credential, got none")
	}
}

func TestPublishEventAction_Invoke_HTTPClientTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	a := &PublishEventAction{
		httpClient: &http.Client{
			Timeout:   100 * time.Millisecond,
			Transport: &hostRewriteTransport{host: serverHost(server.URL)},
		},
	}
	req := action.ConfigureRequest{ProviderData: newEventGridTestClient(nil)}
	cfgResp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, cfgResp)

	cfg := buildPublishConfig(t,
		"https://example.eventgrid.azure.net/api/events",
		nil, nil, nil,
		sampleCloudEventBlocks(),
		nil, nil,
	)
	resp, _ := invokePublishAction(t, a, cfg)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error due to timeout, got none")
	}
}

func TestPublishEventAction_Configure_InvalidProviderData(t *testing.T) {
	t.Parallel()

	a := &PublishEventAction{}
	req := action.ConfigureRequest{ProviderData: "not-a-client"}
	resp := &action.ConfigureResponse{}
	a.Configure(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics error for invalid provider data, got none")
	}
}

func TestPublishEventAction_ResolveAuthMethod_Default(t *testing.T) {
	t.Parallel()

	if got := resolveAuthMethod(types.StringNull()); got != authMethodDAC {
		t.Fatalf("expected default auth method %q, got %q", authMethodDAC, got)
	}
}
