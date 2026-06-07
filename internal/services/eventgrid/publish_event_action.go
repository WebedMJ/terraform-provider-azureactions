// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package eventgrid

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	// Event Grid data plane authentication uses this audience scope.
	// Last reviewed: 2026-06-06
	// Ref: https://learn.microsoft.com/en-us/azure/event-grid/authenticate-with-microsoft-entra-id
	eventGridTokenScope = "https://eventgrid.azure.net/.default"

	authMethodDAC = "default_azure_credential"
	authMethodKey = "access_key"
	authMethodSAS = "sas_token"

	defaultTimeoutSeconds = int64(30)
	defaultContentType    = "application/cloudevents-batch+json"

	httpDialTimeout         = 10 * time.Second // timeout for establishing connection
	httpTLSHandshakeTimeout = 10 * time.Second
)

type PublishEventAction struct {
	sdk.ActionMetadata
	// httpClient is used in tests to override the default client.
	httpClient *http.Client
}

var _ sdk.Action = &PublishEventAction{}

// cloudEventExtNameRegexp matches valid CloudEvents extension attribute names:
// must start with a lowercase letter, contain only lowercase letters (a-z) and digits (0-9),
// and be 1-20 characters long, as required by the CloudEvents v1.0 spec
// (https://github.com/cloudevents/spec/blob/v1.0.2/spec.md#attributes).
var cloudEventExtNameRegexp = regexp.MustCompile(`^[a-z][a-z0-9]{0,19}$`)

func NewPublishEventAction() action.Action {
	return &PublishEventAction{}
}

type PublishEventActionModel struct {
	EndpointURL    types.String `tfsdk:"endpoint_url"`
	AuthMethod     types.String `tfsdk:"auth_method"`
	AccessKey      types.String `tfsdk:"access_key"`
	SASToken       types.String `tfsdk:"sas_token"`
	CloudEvents    types.List   `tfsdk:"cloud_event"`
	ContentType    types.String `tfsdk:"content_type"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

type CloudEventBlockModel struct {
	SpecVersion          types.String `tfsdk:"specversion"`
	ID                   types.String `tfsdk:"id"`
	Source               types.String `tfsdk:"source"`
	Type                 types.String `tfsdk:"type"`
	Subject              types.String `tfsdk:"subject"`
	Time                 types.String `tfsdk:"time"`
	DataContentType      types.String `tfsdk:"datacontenttype"`
	Data                 types.Map    `tfsdk:"data"`
	DataBase64           types.String `tfsdk:"data_base64"`
	CloudEventExtensions types.Map    `tfsdk:"cloud_event_extensions"`
}

func (p *PublishEventAction) Metadata(_ context.Context, _ action.MetadataRequest, response *action.MetadataResponse) {
	response.TypeName = "azureactions_eventgrid_publish_cloudevent"
}

func (p *PublishEventAction) Schema(_ context.Context, _ action.SchemaRequest, response *action.SchemaResponse) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Publishes CloudEvents to an Azure Event Grid publish endpoint using Microsoft Entra ID, access key, or SAS token authentication. This action only publishes CloudEvents payloads. Configure the target Event Grid topic or domain to accept CloudEvents input schema, for example `input_schema = \"CloudEventSchemaV1_0\"` on `azurerm_eventgrid_topic` or `azurerm_eventgrid_domain`.",
		Attributes: map[string]schema.Attribute{
			"endpoint_url": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Event Grid publish endpoint URL. Examples: `https://<topic>.<region>-1.eventgrid.azure.net/api/events` or `https://<namespace>.<region>.eventgrid.azure.net/topics/<topic>:publish`.",
			},
			"auth_method": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Authentication method. Accepted values: `default_azure_credential`, `access_key`, `sas_token`. Defaults to `default_azure_credential` when omitted.",
			},
			"access_key": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Event Grid access key, required when `auth_method` is `access_key`. Provide via a Terraform sensitive variable.",
			},
			"sas_token": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Event Grid SAS token, required when `auth_method` is `sas_token`. Provide via a Terraform sensitive variable.",
			},
			"content_type": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "HTTP content type. Defaults to `application/cloudevents-batch+json`.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Request timeout in seconds. Defaults to 30. Must be >= 1.",
			},
		},
		Blocks: map[string]schema.Block{
			"cloud_event": schema.ListNestedBlock{
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				MarkdownDescription: "CloudEvent blocks to publish. At least one block is required. Use repeated blocks or dynamic blocks. The provider encodes these into a CloudEvents JSON batch payload. Event Grid resources configured for the legacy EventGridEvent schema will reject these payloads.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"specversion": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "CloudEvents spec version. Defaults to `1.0`.",
						},
						"id": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Event identifier. Defaults to a globally unique `terraform-<uuid>` value when omitted.",
						},
						"source": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Event source URI-reference.",
						},
						"type": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Event type.",
						},
						"subject": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Event subject.",
						},
						"time": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Event time in RFC3339 format.",
						},
						"datacontenttype": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Content type of event data. Defaults to `application/json` when `data` is provided.",
						},
						"data": schema.MapAttribute{
							Optional:            true,
							ElementType:         types.StringType,
							MarkdownDescription: "Event data map. The provider JSON-encodes this map into the CloudEvent `data` value.",
						},
						"data_base64": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Base64-encoded event payload for CloudEvent `data_base64`.",
						},
						"cloud_event_extensions": schema.MapAttribute{
							Optional:            true,
							ElementType:         types.StringType,
							MarkdownDescription: "CloudEvent extension attributes represented as string values.",
						},
					},
				},
			},
		},
	}
}

func (p *PublishEventAction) Configure(ctx context.Context, request action.ConfigureRequest, response *action.ConfigureResponse) {
	p.Defaults(ctx, request, response)
}

func (p *PublishEventAction) Invoke(ctx context.Context, request action.InvokeRequest, response *action.InvokeResponse) {
	var model PublishEventActionModel

	response.Diagnostics.Append(request.Config.Get(ctx, &model)...)
	if response.Diagnostics.HasError() {
		return
	}

	endpointURL, err := validateEndpointURL(model.EndpointURL)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "invalid endpoint_url", err)
		return
	}

	authMethod := resolveAuthMethod(model.AuthMethod)
	if authMethod != authMethodDAC && authMethod != authMethodKey && authMethod != authMethodSAS {
		sdk.SetResponseErrorDiagnostic(response, "invalid auth_method",
			fmt.Sprintf("auth_method must be %q, %q, or %q, got %q", authMethodDAC, authMethodKey, authMethodSAS, authMethod))
		return
	}

	timeoutSeconds := defaultTimeoutSeconds
	if !model.TimeoutSeconds.IsNull() && !model.TimeoutSeconds.IsUnknown() {
		timeoutSeconds = model.TimeoutSeconds.ValueInt64()
	}
	if timeoutSeconds < 1 {
		sdk.SetResponseErrorDiagnostic(response, "invalid timeout_seconds",
			fmt.Errorf("timeout_seconds must be at least 1, got %d", timeoutSeconds))
		return
	}

	payload, eventCount, err := buildCloudEventsPayload(ctx, model.CloudEvents)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "invalid cloud_event configuration", err)
		return
	}

	contentType := defaultContentType
	if !model.ContentType.IsNull() && !model.ContentType.IsUnknown() && strings.TrimSpace(model.ContentType.ValueString()) != "" {
		contentType = strings.TrimSpace(model.ContentType.ValueString())
	}

	headers, err := p.resolveAuthHeaders(ctx, model, authMethod)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "resolving authentication", err)
		return
	}

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Publishing %d CloudEvents to Event Grid endpoint %s using auth_method=%s", eventCount, endpointURL, authMethod),
	})

	publishCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	statusCode, respBody, err := p.publishEvents(publishCtx, endpointURL, contentType, payload, headers)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "publishing events", err)
		return
	}

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Published %d CloudEvents successfully (HTTP %d)", eventCount, statusCode),
	})
	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Action result details (progress-only): endpoint=%s status_code=%d event_count=%d response_bytes=%d", endpointURL, statusCode, eventCount, len(respBody)),
	})
}

func validateEndpointURL(endpoint types.String) (string, error) {
	if endpoint.IsNull() || endpoint.IsUnknown() {
		return "", fmt.Errorf("endpoint_url must be provided")
	}

	endpointURL := strings.TrimSpace(endpoint.ValueString())
	if endpointURL == "" {
		return "", fmt.Errorf("endpoint_url must be provided")
	}

	u, err := url.Parse(endpointURL)
	if err != nil {
		return "", fmt.Errorf("parsing endpoint URL: %w", err)
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("endpoint_url must be an absolute URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("endpoint_url must use https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("endpoint_url host is required")
	}

	return endpointURL, nil
}

func resolveAuthMethod(authMethod types.String) string {
	if authMethod.IsNull() || authMethod.IsUnknown() || strings.TrimSpace(authMethod.ValueString()) == "" {
		return authMethodDAC
	}

	return strings.TrimSpace(authMethod.ValueString())
}

func buildCloudEventsPayload(ctx context.Context, cloudEvents types.List) ([]byte, int, error) {
	if cloudEvents.IsNull() || cloudEvents.IsUnknown() {
		return nil, 0, fmt.Errorf("at least one cloud_event block must be provided")
	}

	var events []CloudEventBlockModel
	if diags := cloudEvents.ElementsAs(ctx, &events, false); diags.HasError() {
		return nil, 0, fmt.Errorf("parsing cloud_event blocks: %v", diags)
	}

	if len(events) == 0 {
		return nil, 0, fmt.Errorf("at least one cloud_event block must be provided")
	}

	result := make([]map[string]any, 0, len(events))

	for i, event := range events {
		eventMap := map[string]any{}

		specVersion := "1.0"
		if !event.SpecVersion.IsNull() && !event.SpecVersion.IsUnknown() && strings.TrimSpace(event.SpecVersion.ValueString()) != "" {
			specVersion = strings.TrimSpace(event.SpecVersion.ValueString())
		}
		eventMap["specversion"] = specVersion

		id := optionalStringValue(event.ID)
		if id == "" {
			id = defaultEventID()
		}
		eventMap["id"] = id

		source, err := requiredStringValue(event.Source, fmt.Sprintf("cloud_event[%d].source", i))
		if err != nil {
			return nil, 0, err
		}
		eventMap["source"] = source

		eventType, err := requiredStringValue(event.Type, fmt.Sprintf("cloud_event[%d].type", i))
		if err != nil {
			return nil, 0, err
		}
		eventMap["type"] = eventType

		if v := optionalStringValue(event.Subject); v != "" {
			eventMap["subject"] = v
		}

		if v := optionalStringValue(event.Time); v != "" {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return nil, 0, fmt.Errorf("cloud_event[%d].time must be RFC3339 format: %w", i, err)
			}
			eventMap["time"] = v
		}

		dataContentType := optionalStringValue(event.DataContentType)
		dataBase64 := optionalStringValue(event.DataBase64)

		hasDataMap := !event.Data.IsNull() && !event.Data.IsUnknown()

		if hasDataMap && dataBase64 != "" {
			return nil, 0, fmt.Errorf("cloud_event[%d] cannot set both data and data_base64", i)
		}

		if hasDataMap {
			dataMap := map[string]types.String{}
			if diags := event.Data.ElementsAs(ctx, &dataMap, false); diags.HasError() {
				return nil, 0, fmt.Errorf("parsing cloud_event[%d].data: %v", i, diags)
			}

			serializedData := map[string]string{}
			for k, v := range dataMap {
				if v.IsNull() || v.IsUnknown() {
					continue
				}
				serializedData[k] = v.ValueString()
			}

			var data any = serializedData
			eventMap["data"] = data
			if dataContentType == "" {
				dataContentType = "application/json"
			}
		}

		if dataBase64 != "" {
			if _, err := base64.StdEncoding.DecodeString(dataBase64); err != nil {
				return nil, 0, fmt.Errorf("cloud_event[%d].data_base64 is not valid base64: %w", i, err)
			}
			eventMap["data_base64"] = dataBase64
		}

		if dataContentType != "" {
			eventMap["datacontenttype"] = dataContentType
		}

		if !event.CloudEventExtensions.IsNull() && !event.CloudEventExtensions.IsUnknown() {
			extMap := map[string]types.String{}
			if diags := event.CloudEventExtensions.ElementsAs(ctx, &extMap, false); diags.HasError() {
				return nil, 0, fmt.Errorf("parsing cloud_event[%d].cloud_event_extensions: %v", i, diags)
			}

			for key, value := range extMap {
				if value.IsNull() || value.IsUnknown() {
					continue
				}

				if !cloudEventExtNameRegexp.MatchString(key) {
					return nil, 0, fmt.Errorf("cloud_event[%d].cloud_event_extensions: extension attribute name %q must consist only of lowercase letters (a-z) and digits (0-9)", i, key)
				}

				if key == "specversion" || key == "id" || key == "source" || key == "type" || key == "subject" || key == "time" || key == "datacontenttype" || key == "data" || key == "data_base64" {
					return nil, 0, fmt.Errorf("cloud_event[%d].cloud_event_extensions contains reserved CloudEvents attribute name %q", i, key)
				}

				eventMap[key] = value.ValueString()
			}
		}

		result = append(result, eventMap)
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return nil, 0, fmt.Errorf("encoding cloud_event blocks into JSON payload: %w", err)
	}

	return payload, len(result), nil
}

func requiredStringValue(value types.String, field string) (string, error) {
	if value.IsNull() || value.IsUnknown() || strings.TrimSpace(value.ValueString()) == "" {
		return "", fmt.Errorf("%s must be provided", field)
	}

	return strings.TrimSpace(value.ValueString()), nil
}

func defaultEventID() string {
	return fmt.Sprintf("terraform-%s", uuid.NewString())
}

func optionalStringValue(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}

	return strings.TrimSpace(value.ValueString())
}

func (p *PublishEventAction) resolveAuthHeaders(ctx context.Context, model PublishEventActionModel, authMethod string) (map[string]string, error) {
	headers := map[string]string{}

	switch authMethod {
	case authMethodDAC:
		if p.Client == nil {
			return nil, fmt.Errorf("provider client is not configured; ensure the provider block is set up with valid Azure credentials")
		}
		if p.Client.Credential == nil {
			return nil, fmt.Errorf("provider token credential is not configured; ensure the provider has valid Azure credentials")
		}

		token, err := p.Client.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{eventGridTokenScope}})
		if err != nil {
			return nil, fmt.Errorf("obtaining Event Grid token for scope %q: %w", eventGridTokenScope, err)
		}
		if strings.TrimSpace(token.Token) == "" {
			return nil, fmt.Errorf("obtaining Event Grid token for scope %q returned an empty access token", eventGridTokenScope)
		}

		headers["Authorization"] = fmt.Sprintf("Bearer %s", token.Token)

	case authMethodKey:
		if model.AccessKey.IsNull() || model.AccessKey.IsUnknown() || strings.TrimSpace(model.AccessKey.ValueString()) == "" {
			return nil, fmt.Errorf("access_key must be provided when auth_method is %q", authMethodKey)
		}
		headers["aeg-sas-key"] = strings.TrimSpace(model.AccessKey.ValueString())

	case authMethodSAS:
		if model.SASToken.IsNull() || model.SASToken.IsUnknown() || strings.TrimSpace(model.SASToken.ValueString()) == "" {
			return nil, fmt.Errorf("sas_token must be provided when auth_method is %q", authMethodSAS)
		}
		headers["aeg-sas-token"] = strings.TrimSpace(model.SASToken.ValueString())
	}

	return headers, nil
}

func (p *PublishEventAction) getHTTPClient() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}

	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: httpDialTimeout}).DialContext,
			TLSHandshakeTimeout: httpTLSHandshakeTimeout,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			DisableCompression:  false,
			MaxConnsPerHost:     0,
		},
	}
}

func (p *PublishEventAction) publishEvents(ctx context.Context, endpointURL, contentType string, payload []byte, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "terraform-provider-azureactions/eventgrid-publish")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := p.getHTTPClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("sending publish request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("reading publish response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, respBody, fmt.Errorf("Event Grid publish API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return resp.StatusCode, respBody, nil
}
