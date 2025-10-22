// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package sdk

import (
	"context"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/hashicorp/terraform-plugin-framework/action"
)

type Action interface {
	action.ActionWithConfigure
}

type ActionMetadata struct {
	Client *clients.Client

	SubscriptionId string
}

func (a *ActionMetadata) Defaults(_ context.Context, request action.ConfigureRequest, response *action.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}

	c, ok := request.ProviderData.(*clients.Client)
	if !ok {
		response.Diagnostics.AddError("Client Provider Data Error", "invalid provider data supplied")
		return
	}

	a.Client = c
	a.SubscriptionId = c.Account.SubscriptionId
}
