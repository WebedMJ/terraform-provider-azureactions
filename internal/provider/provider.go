// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/automation"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/compute"
	"github.com/WebedMJ/terraform-provider-azureactions/internal/services/devops"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
)

type azureActionsProvider struct{}

var (
	_ provider.Provider            = &azureActionsProvider{}
	_ provider.ProviderWithActions = &azureActionsProvider{}
)

type azureActionsProviderModel struct {
	SubscriptionID types.String `tfsdk:"subscription_id"`
	ClientID       types.String `tfsdk:"client_id"`
	ClientSecret   types.String `tfsdk:"client_secret"`
	TenantID       types.String `tfsdk:"tenant_id"`
	Environment    types.String `tfsdk:"environment"`
}

func NewFrameworkV5Provider(_ context.Context) (tfprotov5.ProviderServer, error) {
	return providerserver.NewProtocol5(New())(), nil
}

func New() provider.Provider {
	return &azureActionsProvider{}
}

func (p *azureActionsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, response *provider.MetadataResponse) {
	response.TypeName = "azureactions"
}

func (p *azureActionsProvider) Schema(_ context.Context, _ provider.SchemaRequest, response *provider.SchemaResponse) {
	response.Schema = schema.Schema{
		Description: "The Azure Actions provider enables Terraform actions for Azure resources using the new actions capability in Terraform 1.14.",
		Attributes: map[string]schema.Attribute{
			"subscription_id": schema.StringAttribute{
				Optional:    true,
				Description: "The Azure Subscription ID which should be used.",
			},
			"client_id": schema.StringAttribute{
				Optional:    true,
				Description: "The Client ID which should be used for authentication.",
			},
			"client_secret": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The Client Secret which should be used for authentication.",
			},
			"tenant_id": schema.StringAttribute{
				Optional:    true,
				Description: "The Tenant ID which should be used for authentication.",
			},
			"environment": schema.StringAttribute{
				Optional:    true,
				Description: "The Cloud Environment which should be used. Possible values are public, usgovernment, and china. Defaults to public.",
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

	// Set subscription ID from config or environment
	if !data.SubscriptionID.IsNull() && !data.SubscriptionID.IsUnknown() {
		config.SubscriptionID = data.SubscriptionID.ValueString()
	} else if subscriptionId := os.Getenv("ARM_SUBSCRIPTION_ID"); subscriptionId != "" {
		config.SubscriptionID = subscriptionId
	}

	// Set client ID from config or environment
	if !data.ClientID.IsNull() && !data.ClientID.IsUnknown() {
		config.ClientID = data.ClientID.ValueString()
	} else if clientId := os.Getenv("ARM_CLIENT_ID"); clientId != "" {
		config.ClientID = clientId
	}

	// Set client secret from config or environment
	if !data.ClientSecret.IsNull() && !data.ClientSecret.IsUnknown() {
		config.ClientSecret = data.ClientSecret.ValueString()
	} else if clientSecret := os.Getenv("ARM_CLIENT_SECRET"); clientSecret != "" {
		config.ClientSecret = clientSecret
	}

	// Set tenant ID from config or environment
	if !data.TenantID.IsNull() && !data.TenantID.IsUnknown() {
		config.TenantID = data.TenantID.ValueString()
	} else if tenantId := os.Getenv("ARM_TENANT_ID"); tenantId != "" {
		config.TenantID = tenantId
	}

	// Set environment from config or environment, default to public
	environment := "public"
	if !data.Environment.IsNull() && !data.Environment.IsUnknown() {
		environment = data.Environment.ValueString()
	} else if env := os.Getenv("ARM_ENVIRONMENT"); env != "" {
		environment = env
	}
	config.Environment = environment

	// Validate required fields
	if config.SubscriptionID == "" {
		response.Diagnostics.AddError(
			"Missing Azure Subscription ID",
			"subscription_id must be provided either via provider configuration or ARM_SUBSCRIPTION_ID environment variable",
		)
		return
	}

	if config.ClientID == "" {
		response.Diagnostics.AddError(
			"Missing Azure Client ID",
			"client_id must be provided either via provider configuration or ARM_CLIENT_ID environment variable",
		)
		return
	}

	if config.ClientSecret == "" {
		response.Diagnostics.AddError(
			"Missing Azure Client Secret",
			"client_secret must be provided either via provider configuration or ARM_CLIENT_SECRET environment variable",
		)
		return
	}

	if config.TenantID == "" {
		response.Diagnostics.AddError(
			"Missing Azure Tenant ID",
			"tenant_id must be provided either via provider configuration or ARM_TENANT_ID environment variable",
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
		compute.Registration{},
		devops.Registration{},
	}
}
