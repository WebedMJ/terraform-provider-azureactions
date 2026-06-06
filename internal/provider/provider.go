// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"
	"strings"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/automation"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/devops"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/eventgrid"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type azureActionsProvider struct {
	version string
}

const (
	envAzureSubscriptionID = "AZURE_SUBSCRIPTION_ID"
	envARMSubscriptionID   = "ARM_SUBSCRIPTION_ID"
	envAzureClientID       = "AZURE_CLIENT_ID"
	envARMClientID         = "ARM_CLIENT_ID"
	envAzureClientSecret   = "AZURE_CLIENT_SECRET"
	envARMClientSecret     = "ARM_CLIENT_SECRET"
	envAzureTenantID       = "AZURE_TENANT_ID"
	envARMTenantID         = "ARM_TENANT_ID"
	envAzureDevOpsOrgURL   = "AZUREDEVOPS_ORG_URL"
)

var (
	_ provider.Provider            = &azureActionsProvider{}
	_ provider.ProviderWithActions = &azureActionsProvider{}
)

type azureActionsProviderModel struct {
	SubscriptionID  types.String `tfsdk:"subscription_id"`
	ClientID        types.String `tfsdk:"client_id"`
	ClientSecret    types.String `tfsdk:"client_secret"`
	TenantID        types.String `tfsdk:"tenant_id"`
	Environment     types.String `tfsdk:"environment"`
	OrganizationURL types.String `tfsdk:"organization_url"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &azureActionsProvider{
			version: version,
		}
	}
}

func (p *azureActionsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, response *provider.MetadataResponse) {
	response.TypeName = "azureactions"
	response.Version = p.version
}

func (p *azureActionsProvider) Schema(_ context.Context, _ provider.SchemaRequest, response *provider.SchemaResponse) {
	response.Schema = schema.Schema{
		MarkdownDescription: "The Azure Actions provider enables Terraform actions for Azure resources using the new actions capability in Terraform 1.14.",
		Attributes: map[string]schema.Attribute{
			"subscription_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The Azure Subscription ID. Required for Azure resource management actions (e.g. Automation); may be omitted for Azure DevOps actions using PAT or DefaultAzureCredential authentication. Can also be supplied via `AZURE_SUBSCRIPTION_ID` (with `ARM_SUBSCRIPTION_ID` supported as an equivalent alias).",
			},
			"client_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional Client ID override. Use together with `tenant_id` and `client_secret` for explicit client-secret authentication, or prefer `AZURE_CLIENT_ID` environment configuration when using DefaultAzureCredential (`ARM_CLIENT_ID` is supported as an equivalent alias).",
			},
			"client_secret": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Optional Client Secret override. When set, `client_id` and `tenant_id` must also be provided. Otherwise omit all three and let DefaultAzureCredential authenticate via Azure CLI, workload identity, managed identity, or environment configuration (`ARM_CLIENT_SECRET` is supported as an equivalent alias to `AZURE_CLIENT_SECRET`).",
			},
			"tenant_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional Tenant ID override. This can also guide DefaultAzureCredential and may be supplied via `AZURE_TENANT_ID` (`ARM_TENANT_ID` is supported as an equivalent alias).",
			},
			"environment": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The Cloud Environment which should be used. Possible values are public, usgovernment, and china. Defaults to public.",
			},
			"organization_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional Azure DevOps organization URL used by DevOps actions, for example `https://dev.azure.com/myorg`. This is not required for Azure Automation actions. Can also be supplied via `AZUREDEVOPS_ORG_URL`.",
			},
		},
	}
}

func (p *azureActionsProvider) Configure(ctx context.Context, request provider.ConfigureRequest, response *provider.ConfigureResponse) {
	var data azureActionsProviderModel

	response.Diagnostics.Append(request.Config.Get(ctx, &data)...)
	if response.Diagnostics.HasError() {
		return
	}

	config := clients.Config{}

	// Resolve provider values first, then AZURE_* env vars, then ARM_* aliases.
	config.SubscriptionID = providerValueOrEnv(data.SubscriptionID, envAzureSubscriptionID, envARMSubscriptionID)
	config.ClientID = providerValueOrEnv(data.ClientID, envAzureClientID, envARMClientID)
	config.ClientSecret = providerValueOrEnv(data.ClientSecret, envAzureClientSecret, envARMClientSecret)
	config.TenantID = providerValueOrEnv(data.TenantID, envAzureTenantID, envARMTenantID)
	config.OrganizationURL = providerValueOrEnv(data.OrganizationURL, envAzureDevOpsOrgURL)

	// Set environment from config or environment, default to public
	environment := "public"
	if !data.Environment.IsNull() && !data.Environment.IsUnknown() {
		environment = data.Environment.ValueString()
	} else if env := os.Getenv("ARM_ENVIRONMENT"); env != "" {
		environment = env
	}
	config.Environment = environment

	if strings.TrimSpace(config.ClientSecret) != "" && (strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.TenantID) == "") {
		response.Diagnostics.AddError(
			"Incomplete explicit credential configuration",
			"When client_secret is provided, client_id and tenant_id must also be provided. Otherwise omit all three and let DefaultAzureCredential authenticate via Azure CLI, managed identity, workload identity, or environment configuration.",
		)
		return
	}

	client, err := clients.NewClient(ctx, config)
	if err != nil {
		response.Diagnostics.AddError(
			"Failed to create Azure client",
			"Error creating Azure client: "+err.Error(),
		)
		return
	}

	response.ActionData = client
}

func (p *azureActionsProvider) Actions(_ context.Context) []func() action.Action {
	var output []func() action.Action

	for _, service := range SupportedServices() {
		output = append(output, service.Actions()...)
	}

	return output
}

func (p *azureActionsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	// This provider focuses on actions only, no data sources
	return []func() datasource.DataSource{}
}

func (p *azureActionsProvider) Resources(_ context.Context) []func() resource.Resource {
	// This provider focuses on actions only, no resources
	return []func() resource.Resource{}
}

func SupportedServices() []sdk.ServiceRegistration {
	return []sdk.ServiceRegistration{
		automation.Registration{},
		devops.Registration{},
		eventgrid.Registration{},
	}
}

func providerValueOrEnv(value types.String, envKeys ...string) string {
	if !value.IsNull() && !value.IsUnknown() {
		return strings.TrimSpace(value.ValueString())
	}

	return firstNonEmptyEnv(envKeys...)
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}

	return ""
}
