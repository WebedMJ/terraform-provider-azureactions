// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package sdk

import (
	"github.com/hashicorp/terraform-plugin-framework/action"
)

type ServiceRegistration interface {
	// Actions returns the actions supported by this service
	Actions() []func() action.Action
}
