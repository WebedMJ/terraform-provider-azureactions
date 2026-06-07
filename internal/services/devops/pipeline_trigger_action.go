// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

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
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	// Azure DevOps REST API version 7.1 is the current stable release.
	// Last reviewed: 2024-12-20
	// Ref: https://learn.microsoft.com/en-us/rest/api/azure/devops/
	devOpsAPIVersion        = "7.1"
	devOpsTokenScope        = "499b84ac-1321-427f-aa17-267ca6975798/.default"
	authMethodPAT           = "pat"
	authMethodSP            = "service_principal"
	authMethodDAC           = "default_azure_credential"
	defaultPollSeconds      = 10
	httpDialTimeout         = 10 * time.Second // timeout for establishing connection
	httpTLSHandshakeTimeout = 10 * time.Second
	httpRequestTimeout      = 30 * time.Second // per-request total timeout via context
)

// PipelineTriggerAction implements sdk.Action for triggering an Azure DevOps
// pipeline run.  It supports both Personal Access Token (PAT) authentication
// and service principal (Azure AD) authentication.
type PipelineTriggerAction struct {
	sdk.ActionMetadata
	// httpClient is used in tests to override http.DefaultClient.
	// A nil value uses http.DefaultClient.
	httpClient *http.Client
	// pollInterval is used in tests to override the default polling interval.
	// A zero value uses defaultPollSeconds.
	pollInterval time.Duration
}

var _ sdk.Action = &PipelineTriggerAction{}

// NewPipelineTriggerAction is the factory function registered with the provider.
func NewPipelineTriggerAction() action.Action {
	return &PipelineTriggerAction{}
}

// PipelineTriggerActionModel is the Terraform model for the action config block.
type PipelineTriggerActionModel struct {
	Project             types.String `tfsdk:"project"`
	PipelineID          types.Int64  `tfsdk:"pipeline_id"`
	AuthMethod          types.String `tfsdk:"auth_method"`
	PersonalAccessToken types.String `tfsdk:"personal_access_token"`
	BranchRef           types.String `tfsdk:"branch_ref"`
	Variables           types.Map    `tfsdk:"variables"`
	TemplateParameters  types.Map    `tfsdk:"template_parameters"`
	StagesToSkip        types.List   `tfsdk:"stages_to_skip"`
	WaitForCompletion   types.Bool   `tfsdk:"wait_for_completion"`
	TimeoutMinutes      types.Int64  `tfsdk:"timeout_minutes"`
}

// pipelineRunRequest is serialised as the body of the DevOps Pipelines Run API call.
type pipelineRunRequest struct {
	Resources          *pipelineRunResources `json:"resources,omitempty"`
	Variables          map[string]varValue   `json:"variables,omitempty"`
	TemplateParameters map[string]string     `json:"templateParameters,omitempty"`
	StagesToSkip       []string              `json:"stagesToSkip,omitempty"`
}

type pipelineRunResources struct {
	Repositories map[string]repositoryRef `json:"repositories,omitempty"`
}

type repositoryRef struct {
	RefName string `json:"refName"`
}

type varValue struct {
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

// pipelineRunResponse is the subset of the DevOps Pipelines Run API response
// that is needed for status polling.
type pipelineRunResponse struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`  // unknown, inProgress, canceling, completed
	Result string `json:"result"` // unknown, succeeded, failed, canceled
}

// Metadata sets the action type name.
func (p *PipelineTriggerAction) Metadata(_ context.Context, _ action.MetadataRequest, response *action.MetadataResponse) {
	response.TypeName = "azureactions_devops_pipeline_trigger"
}

// Schema defines the configuration attributes for the action.
func (p *PipelineTriggerAction) Schema(_ context.Context, _ action.SchemaRequest, response *action.SchemaResponse) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Triggers an Azure DevOps pipeline run. Supports Personal Access Token (PAT) " +
			"and service principal (Azure AD) authentication methods. Configure `organization_url` on the provider.",
		Attributes: map[string]schema.Attribute{
			"project": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name or ID of the Azure DevOps project.",
			},
			"pipeline_id": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "The integer ID of the pipeline to trigger.",
			},
			"auth_method": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Authentication method to use. Accepted values: " +
					"`pat` (Personal Access Token) or `default_azure_credential` (reuses the provider-level Azure credential chain). `service_principal` is retained as a backwards-compatible alias for `default_azure_credential`.",
			},
			"personal_access_token": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Personal Access Token (PAT) used when `auth_method` is `\"pat\"`. Must have Build (Read & execute) permission. " +
					"Provide this value via a Terraform sensitive variable or the `TF_VAR_PERSONAL_ACCESS_TOKEN` environment variable to avoid it appearing in plan output.",
			},
			"branch_ref": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Git ref to run the pipeline against, e.g. `refs/heads/main`. When omitted the pipeline's default branch is used.",
			},
			"variables": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Pipeline variable overrides. Keys are variable names, values are variable values.",
			},
			"template_parameters": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Template parameter values. Keys are parameter names, values are parameter values.",
			},
			"stages_to_skip": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "List of stage names to skip in the pipeline run.",
			},
			"wait_for_completion": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether to wait for the pipeline run to reach a terminal state before returning. Defaults to false.",
			},
			"timeout_minutes": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum time in minutes to wait for pipeline completion. Only used when wait_for_completion is true. Defaults to 30.",
			},
		},
	}
}

// Configure populates the embedded sdk.ActionMetadata with the provider client.
func (p *PipelineTriggerAction) Configure(ctx context.Context, request action.ConfigureRequest, response *action.ConfigureResponse) {
	p.Defaults(ctx, request, response)
}

// Invoke triggers the Azure DevOps pipeline run.
func (p *PipelineTriggerAction) Invoke(ctx context.Context, request action.InvokeRequest, response *action.InvokeResponse) {
	var model PipelineTriggerActionModel

	response.Diagnostics.Append(request.Config.Get(ctx, &model)...)
	if response.Diagnostics.HasError() {
		return
	}

	// Validate auth_method
	authMethod := model.AuthMethod.ValueString()
	if authMethod != authMethodPAT && authMethod != authMethodSP && authMethod != authMethodDAC {
		sdk.SetResponseErrorDiagnostic(response, "invalid auth_method",
			fmt.Sprintf("auth_method must be %q, %q, or %q, got %q", authMethodPAT, authMethodDAC, authMethodSP, authMethod))
		return
	}

	// Resolve the authorisation header
	authHeader, err := p.resolveAuthHeader(ctx, model)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "resolving authentication", err)
		return
	}

	orgURL, err := p.organizationURL()
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "missing provider organization_url", err)
		return
	}

	// Build the request body
	body, err := p.buildRequestBody(ctx, model)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "building pipeline run request", err)
		return
	}

	project := url.PathEscape(model.Project.ValueString())
	pipelineID := model.PipelineID.ValueInt64()

	if pipelineID <= 0 {
		sdk.SetResponseErrorDiagnostic(response, "invalid pipeline_id",
			fmt.Errorf("pipeline_id must be greater than 0, got %d", pipelineID))
		return
	}

	triggerURL := fmt.Sprintf("%s/%s/_apis/pipelines/%d/runs?api-version=%s",
		orgURL, project, pipelineID, devOpsAPIVersion)

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Triggering pipeline %d in project %s (org: %s)", pipelineID, project, orgURL),
	})

	run, err := p.triggerPipeline(ctx, triggerURL, authHeader, body)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "triggering pipeline", err)
		return
	}

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Pipeline run %d (%s) created with state: %s", run.ID, run.Name, run.State),
	})
	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Action result details (progress-only): run_id=%d initial_state=%s", run.ID, run.State),
	})

	// Optionally wait for the run to reach a terminal state
	if !model.WaitForCompletion.IsNull() && model.WaitForCompletion.ValueBool() {
		timeoutMinutes := int64(30)
		if !model.TimeoutMinutes.IsNull() && !model.TimeoutMinutes.IsUnknown() {
			timeoutMinutes = model.TimeoutMinutes.ValueInt64()
		}

		if timeoutMinutes < 1 {
			sdk.SetResponseErrorDiagnostic(response, "invalid timeout_minutes",
				fmt.Errorf("timeout_minutes must be at least 1, got %d", timeoutMinutes))
			return
		}

		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMinutes)*time.Minute)
		defer cancel()

		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Waiting for pipeline run %d to complete (timeout: %d minutes)", run.ID, timeoutMinutes),
		})

		statusURL := fmt.Sprintf("%s/%s/_apis/pipelines/%d/runs/%d?api-version=%s",
			orgURL, project, pipelineID, run.ID, devOpsAPIVersion)

		finalRun, err := p.waitForPipelineRun(waitCtx, response, statusURL, authHeader, run.ID)
		if err != nil {
			sdk.SetResponseErrorDiagnostic(response, "waiting for pipeline run", err)
			return
		}

		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Action result details (progress-only): run_id=%d final_state=%s final_result=%s", finalRun.ID, finalRun.State, finalRun.Result),
		})
	} else {
		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Pipeline run %d triggered successfully (not waiting for completion)", run.ID),
		})
	}
}

func (p *PipelineTriggerAction) organizationURL() (string, error) {
	if p.Client == nil {
		return "", fmt.Errorf("provider client is not configured")
	}

	orgURL := strings.TrimRight(strings.TrimSpace(p.Client.Config.OrganizationURL), "/")
	if orgURL == "" {
		return "", fmt.Errorf("organization_url must be configured in the provider block for Azure DevOps actions")
	}

	return orgURL, nil
}

// resolveAuthHeader returns the Authorization header value to use for the
// Azure DevOps API request.
func (p *PipelineTriggerAction) resolveAuthHeader(ctx context.Context, model PipelineTriggerActionModel) (string, error) {
	switch model.AuthMethod.ValueString() {
	case authMethodPAT:
		if model.PersonalAccessToken.IsNull() || model.PersonalAccessToken.IsUnknown() || model.PersonalAccessToken.ValueString() == "" {
			return "", fmt.Errorf("personal_access_token must be provided when auth_method is %q", authMethodPAT)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(":" + model.PersonalAccessToken.ValueString()))
		return "Basic " + encoded, nil

	case authMethodSP, authMethodDAC:
		if p.Client == nil {
			return "", fmt.Errorf("provider client is not configured; ensure the provider block is set up with valid Azure credentials")
		}
		if p.Client.Credential == nil {
			return "", fmt.Errorf("provider token credential is not configured; ensure the provider has valid Azure credentials")
		}

		// Always request Azure DevOps token using the well-known app scope.
		token, err := p.Client.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{devOpsTokenScope}})
		if err != nil {
			return "", fmt.Errorf("obtaining Azure DevOps token for scope %q: %w", devOpsTokenScope, err)
		}

		if strings.TrimSpace(token.Token) == "" {
			return "", fmt.Errorf("obtaining Azure DevOps token for scope %q returned an empty access token", devOpsTokenScope)
		}

		return fmt.Sprintf("Bearer %s", token.Token), nil

	default:
		return "", fmt.Errorf("unsupported auth_method: %q", model.AuthMethod.ValueString())
	}
}

// buildRequestBody constructs the JSON payload for the pipeline run API call.
func (p *PipelineTriggerAction) buildRequestBody(ctx context.Context, model PipelineTriggerActionModel) ([]byte, error) {
	runReq := pipelineRunRequest{}

	// Branch ref
	if !model.BranchRef.IsNull() && !model.BranchRef.IsUnknown() && model.BranchRef.ValueString() != "" {
		runReq.Resources = &pipelineRunResources{
			Repositories: map[string]repositoryRef{
				"self": {RefName: model.BranchRef.ValueString()},
			},
		}
	}

	// Variables
	if !model.Variables.IsNull() && !model.Variables.IsUnknown() {
		varsMap := make(map[string]types.String)
		if diags := model.Variables.ElementsAs(ctx, &varsMap, false); diags.HasError() {
			return nil, fmt.Errorf("parsing variables: %v", diags)
		}
		runReq.Variables = make(map[string]varValue, len(varsMap))
		for k, v := range varsMap {
			if !v.IsNull() && !v.IsUnknown() {
				runReq.Variables[k] = varValue{Value: v.ValueString()}
			}
		}
	}

	// Template parameters
	if !model.TemplateParameters.IsNull() && !model.TemplateParameters.IsUnknown() {
		paramsMap := make(map[string]types.String)
		if diags := model.TemplateParameters.ElementsAs(ctx, &paramsMap, false); diags.HasError() {
			return nil, fmt.Errorf("parsing template_parameters: %v", diags)
		}
		runReq.TemplateParameters = make(map[string]string, len(paramsMap))
		for k, v := range paramsMap {
			if !v.IsNull() && !v.IsUnknown() {
				runReq.TemplateParameters[k] = v.ValueString()
			}
		}
	}

	// Stages to skip
	if !model.StagesToSkip.IsNull() && !model.StagesToSkip.IsUnknown() {
		var stages []types.String
		if diags := model.StagesToSkip.ElementsAs(ctx, &stages, false); diags.HasError() {
			return nil, fmt.Errorf("parsing stages_to_skip: %v", diags)
		}
		for _, s := range stages {
			if !s.IsNull() && !s.IsUnknown() {
				runReq.StagesToSkip = append(runReq.StagesToSkip, s.ValueString())
			}
		}
	}

	return json.Marshal(runReq)
}

// getHTTPClient returns the HTTP client to use for DevOps API calls.
// Tests can inject a custom client via the httpClient field.
// If no custom client is set, returns a default client with transport-level
// timeouts. Request lifetime is controlled by the request context.
func (p *PipelineTriggerAction) getHTTPClient() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	// Return a client with transport-level timeouts to avoid hangs on network failures.
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: httpDialTimeout}).DialContext,
			TLSHandshakeTimeout: httpTLSHandshakeTimeout,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			DisableCompression:  false,
			MaxConnsPerHost:     0, // unlimited
		},
	}
}

// triggerPipeline sends the POST request to the Azure DevOps Pipelines API
// and returns the pipeline run response.
func (p *PipelineTriggerAction) triggerPipeline(ctx context.Context, url, authHeader string, body []byte) (*pipelineRunResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := p.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("Azure DevOps API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var run pipelineRunResponse
	if err := json.Unmarshal(respBody, &run); err != nil {
		return nil, fmt.Errorf("parsing pipeline run response: %w", err)
	}

	return &run, nil
}

// waitForPipelineRun polls the pipeline run status URL until the run reaches a
// terminal state (completed) or the context is cancelled.
func (p *PipelineTriggerAction) waitForPipelineRun(ctx context.Context, response *action.InvokeResponse, statusURL, authHeader string, runID int) (*pipelineRunResponse, error) {
	interval := p.pollInterval
	if interval <= 0 {
		interval = defaultPollSeconds * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for pipeline run %d to complete: %w", runID, ctx.Err())
		case <-ticker.C:
			run, err := p.getPipelineRun(ctx, statusURL, authHeader)
			if err != nil {
				return nil, fmt.Errorf("polling pipeline run status: %w", err)
			}

			response.SendProgress(action.InvokeProgressEvent{
				Message: fmt.Sprintf("Pipeline run %d state: %s", runID, run.State),
			})

			if run.State == "completed" {
				switch run.Result {
				case "succeeded":
					response.SendProgress(action.InvokeProgressEvent{
						Message: fmt.Sprintf("Pipeline run %d completed successfully", runID),
					})
					return run, nil
				case "failed":
					return nil, fmt.Errorf("pipeline run %d failed", runID)
				case "canceled":
					return nil, fmt.Errorf("pipeline run %d was canceled", runID)
				default:
					return nil, fmt.Errorf("pipeline run %d completed with unknown result: %s", runID, run.Result)
				}
			}
		}
	}
}

// getPipelineRun fetches the current state of a pipeline run.
func (p *PipelineTriggerAction) getPipelineRun(ctx context.Context, statusURL, authHeader string) (*pipelineRunResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating status request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := p.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending status request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading status response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Azure DevOps status API returned %d: %s", resp.StatusCode, string(body))
	}

	var run pipelineRunResponse
	if err := json.Unmarshal(body, &run); err != nil {
		return nil, fmt.Errorf("parsing pipeline run status: %w", err)
	}

	return &run, nil
}
