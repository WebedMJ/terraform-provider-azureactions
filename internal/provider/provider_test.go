//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccProtoV6ProviderFactories are used to instantiate a provider during
// acceptance testing. The factory function is called for each Terraform CLI
// command to create a provider server that the CLI can connect to and interact with.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"azureactions": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck validates that the required environment variables are set
// for running acceptance tests.
func testAccPreCheck(t *testing.T) {
	if v := os.Getenv("AZURE_SUBSCRIPTION_ID"); v == "" && os.Getenv("ARM_SUBSCRIPTION_ID") == "" {
		t.Fatal("AZURE_SUBSCRIPTION_ID or ARM_SUBSCRIPTION_ID must be set for acceptance tests")
	}
}

// testAccProviderConfig returns the HCL provider configuration for acceptance tests.
func testAccProviderConfig() string {
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		subscriptionID = os.Getenv("ARM_SUBSCRIPTION_ID")
	}

	return `
provider "azureactions" {
	subscription_id = "` + subscriptionID + `"
}
`
}

// testAccPreCheckAzureAutomation validates prerequisites for Azure Automation acceptance tests.
func testAccPreCheckAzureAutomation(t *testing.T) {
	testAccPreCheck(t)

	// Additional checks for Azure Automation tests can be added here
	// (e.g., checking for a specific automation account resource group)
}

// testAccPreCheckAzureDevOps validates prerequisites for Azure DevOps acceptance tests.
func testAccPreCheckAzureDevOps(t *testing.T) {
	testAccPreCheck(t)

	// Additional checks for DevOps tests can be added here
	// (e.g., checking for organization URL or project name)
}

// TestAccProvider checks that the provider can be instantiated.
// This is a simple sanity check test.
func TestAccProvider(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// This step checks that the provider can be configured without error
			{
				Config: testAccProviderConfig(),
			},
		},
	})
}
