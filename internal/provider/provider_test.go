//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccProvider checks that the provider can be instantiated.
// This is a simple sanity check test.
func TestAccProvider(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckCommon(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// This step checks that the provider can be configured without error
			{
				Config: acctest.ProviderConfigFromEnv(t),
			},
		},
	})
}
