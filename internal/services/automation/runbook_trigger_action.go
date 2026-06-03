// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/hashicorp/go-azure-sdk/resource-manager/automation/2019-06-01/job"
	"github.com/hashicorp/go-azure-sdk/resource-manager/automation/2019-06-01/runbook"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type RunbookTriggerAction struct {
	sdk.ActionMetadata
	// pollInterval is used in tests to override the default 10-second polling interval.
	// A zero value uses the default of 10 seconds.
	pollInterval time.Duration
}

var _ sdk.Action = &RunbookTriggerAction{}

func NewRunbookTriggerAction() action.Action {
	return &RunbookTriggerAction{}
}

type RunbookTriggerActionModel struct {
	AutomationAccountName types.String `tfsdk:"automation_account_name"`
	ResourceGroupName     types.String `tfsdk:"resource_group_name"`
	RunbookName           types.String `tfsdk:"runbook_name"`
	Parameters            types.Map    `tfsdk:"parameters"`
	WaitForCompletion     types.Bool   `tfsdk:"wait_for_completion"`
	TimeoutMinutes        types.Int64  `tfsdk:"timeout_minutes"`
}

func (r *RunbookTriggerAction) Schema(_ context.Context, _ action.SchemaRequest, response *action.SchemaResponse) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Triggers an Azure Automation runbook execution.",
		Attributes: map[string]schema.Attribute{
			"automation_account_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the Azure Automation Account containing the runbook.",
			},
			"resource_group_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the resource group containing the Automation Account.",
			},
			"runbook_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name of the runbook to execute.",
			},
			"parameters": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Parameters to pass to the runbook. Keys are parameter names, values are parameter values.",
			},
			"wait_for_completion": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether to wait for the runbook job to complete before returning. Defaults to false.",
			},
			"timeout_minutes": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum time to wait for job completion in minutes. Only used when wait_for_completion is true. Defaults to 30.",
			},
		},
	}
}

func (r *RunbookTriggerAction) Metadata(_ context.Context, _ action.MetadataRequest, response *action.MetadataResponse) {
	response.TypeName = "azureactions_automation_runbook_trigger"
}

func statusString(status *job.JobStatus) string {
	if status == nil {
		return "unknown"
	}

	return string(*status)
}

func (r *RunbookTriggerAction) Invoke(ctx context.Context, request action.InvokeRequest, response *action.InvokeResponse) {
	var model RunbookTriggerActionModel

	response.Diagnostics.Append(request.Config.Get(ctx, &model)...)
	if response.Diagnostics.HasError() {
		return
	}

	// Subscription ID is required for all ARM-based automation operations.
	if r.SubscriptionID == "" {
		sdk.SetResponseErrorDiagnostic(response, "Missing Subscription ID",
			"subscription_id must be configured when using automation actions. "+
				"Set it via the provider block, AZURE_SUBSCRIPTION_ID, or ARM_SUBSCRIPTION_ID.")
		return
	}

	// Create automation client
	automationClient, err := job.NewJobClientWithBaseURI(r.Client.Environment.ResourceManager)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "creating automation client", err)
		return
	}
	automationClient.Client.Authorizer = r.Client.Authorizer

	runbooksClient, err := runbook.NewRunbookClientWithBaseURI(r.Client.Environment.ResourceManager)
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "creating runbook client", err)
		return
	}
	runbooksClient.Client.Authorizer = r.Client.Authorizer

	// Prepare runbook ID - for reference if needed later
	runbookName := model.RunbookName.ValueString()

	// Prepare job parameters
	jobParameters := make(map[string]string)
	if !model.Parameters.IsNull() && !model.Parameters.IsUnknown() {
		parametersMap := make(map[string]types.String)
		response.Diagnostics.Append(model.Parameters.ElementsAs(ctx, &parametersMap, false)...)
		if response.Diagnostics.HasError() {
			return
		}

		for key, value := range parametersMap {
			if !value.IsNull() && !value.IsUnknown() {
				jobParameters[key] = value.ValueString()
			}
		}
	}

	// Create job
	jobName := fmt.Sprintf("terraform-action-%d", time.Now().Unix())
	jobId := job.NewJobID(r.SubscriptionID, model.ResourceGroupName.ValueString(), model.AutomationAccountName.ValueString(), jobName)

	createJobPayload := job.JobCreateParameters{
		Properties: job.JobCreateProperties{
			Runbook: &job.RunbookAssociationProperty{
				Name: &runbookName,
			},
			Parameters: &jobParameters,
		},
	}

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Starting runbook %s in automation account %s", model.RunbookName.ValueString(), model.AutomationAccountName.ValueString()),
	})

	// Create and start the job
	createResp, err := automationClient.Create(ctx, jobId, createJobPayload, job.DefaultCreateOperationOptions())
	if err != nil {
		sdk.SetResponseErrorDiagnostic(response, "creating runbook job", fmt.Sprintf("failed to create job for runbook %s: %v", model.RunbookName.ValueString(), err))
		return
	}

	if createResp.Model == nil || createResp.Model.Properties == nil {
		sdk.SetResponseErrorDiagnostic(response, "creating runbook job", "job creation response was empty")
		return
	}

	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Runbook job %s created successfully", jobName),
	})
	response.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Action result details (progress-only): job_name=%s initial_status=%s", jobName, statusString(createResp.Model.Properties.Status)),
	})

	// If wait_for_completion is true, wait for the job to complete
	if !model.WaitForCompletion.IsNull() && model.WaitForCompletion.ValueBool() {
		timeoutMinutes := int64(30) // default timeout
		if !model.TimeoutMinutes.IsNull() && !model.TimeoutMinutes.IsUnknown() {
			timeoutMinutes = model.TimeoutMinutes.ValueInt64()
		}

		if timeoutMinutes < 1 {
			sdk.SetResponseErrorDiagnostic(response, "invalid timeout_minutes",
				fmt.Errorf("timeout_minutes must be at least 1, got %d", timeoutMinutes))
			return
		}

		timeout := time.Duration(timeoutMinutes) * time.Minute
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Waiting for runbook job to complete (timeout: %d minutes)", timeoutMinutes),
		})

		// Poll job status
		interval := r.pollInterval
		if interval <= 0 {
			interval = 10 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				sdk.SetResponseErrorDiagnostic(response, "waiting for job completion", fmt.Sprintf("timeout waiting for job %s to complete", jobName))
				return
			case <-ticker.C:
				jobResp, err := automationClient.Get(ctx, jobId, job.DefaultGetOperationOptions())
				if err != nil {
					sdk.SetResponseErrorDiagnostic(response, "checking job status", fmt.Sprintf("failed to get job status: %v", err))
					return
				}

				if jobResp.Model != nil && jobResp.Model.Properties != nil && jobResp.Model.Properties.Status != nil {
					status := *jobResp.Model.Properties.Status

					switch status {
					case job.JobStatusCompleted:
						response.SendProgress(action.InvokeProgressEvent{
							Message: fmt.Sprintf("Runbook job %s completed successfully", jobName),
						})
						response.SendProgress(action.InvokeProgressEvent{
							Message: fmt.Sprintf("Action result details (progress-only): job_name=%s final_status=%s", jobName, string(status)),
						})
						return
					case job.JobStatusFailed:
						sdk.SetResponseErrorDiagnostic(response, "runbook job failed", fmt.Sprintf("job %s failed", jobName))
						return
					case job.JobStatusStopped:
						sdk.SetResponseErrorDiagnostic(response, "runbook job stopped", fmt.Sprintf("job %s was stopped", jobName))
						return
					case job.JobStatusSuspended:
						sdk.SetResponseErrorDiagnostic(response, "runbook job suspended", fmt.Sprintf("job %s was suspended", jobName))
						return
					default:
						// Job is still running, continue polling
						response.SendProgress(action.InvokeProgressEvent{
							Message: fmt.Sprintf("Runbook job %s status: %s", jobName, string(status)),
						})
					}
				}
			}
		}
	} else {
		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Runbook %s triggered successfully. Job name: %s", model.RunbookName.ValueString(), jobName),
		})
		response.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Action result details (progress-only): job_name=%s final_status=not_waited", jobName),
		})
	}
}

func (r *RunbookTriggerAction) Configure(ctx context.Context, request action.ConfigureRequest, response *action.ConfigureResponse) {
	r.Defaults(ctx, request, response)
}
