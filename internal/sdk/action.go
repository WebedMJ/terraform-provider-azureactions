// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package sdk

import (
	"context"
	"fmt"

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

// SetResponseErrorDiagnostic is a helper function to set error diagnostics on action responses
func SetResponseErrorDiagnostic(response *action.InvokeResponse, summary string, detail interface{}) {
	var errorMsg string
	switch v := detail.(type) {
	case string:
		errorMsg = v
	case error:
		errorMsg = v.Error()
	default:
		errorMsg = fmt.Sprintf("%v", v)
	}
	response.Diagnostics.AddError(summary, errorMsg)
}
