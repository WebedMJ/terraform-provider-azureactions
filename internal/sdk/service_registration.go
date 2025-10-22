// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package sdk

import (
	"github.com/hashicorp/terraform-plugin-framework/action"
)

type ServiceRegistration interface {
	// Name returns the name of the service
	Name() string

	// Actions returns the actions supported by this service
	Actions() []func() action.Action
}
