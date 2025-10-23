// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package automation

import (
	"github.com/WebedMJ/terraform-provider-azureactions/internal/sdk"
	"github.com/hashicorp/terraform-plugin-framework/action"
)

type Registration struct{}

var _ sdk.ServiceRegistration = Registration{}

// Name returns the name of this service
func (r Registration) Name() string {
	return "Automation"
}

// Actions returns the actions supported by this service
func (r Registration) Actions() []func() action.Action {
	return []func() action.Action{
		NewRunbookTriggerAction,
	}
}
