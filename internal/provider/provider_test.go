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
	if v := os.Getenv("ARM_SUBSCRIPTION_ID"); v == "" {
		t.Fatal("ARM_SUBSCRIPTION_ID must be set for acceptance tests")
	}
	if v := os.Getenv("ARM_CLIENT_ID"); v == "" {
		t.Fatal("ARM_CLIENT_ID must be set for acceptance tests")
	}
	if v := os.Getenv("ARM_CLIENT_SECRET"); v == "" {
		t.Fatal("ARM_CLIENT_SECRET must be set for acceptance tests")
	}
	if v := os.Getenv("ARM_TENANT_ID"); v == "" {
		t.Fatal("ARM_TENANT_ID must be set for acceptance tests")
	}
}

// testAccProviderConfig returns the HCL provider configuration for acceptance tests.
func testAccProviderConfig() string {
	return `
provider "azureactions" {
  subscription_id = var.subscription_id
  client_id       = var.client_id
  client_secret   = var.client_secret
  tenant_id       = var.tenant_id
}

variable "subscription_id" {
  type    = string
  default = ""
}

variable "client_id" {
  type    = string
  default = ""
}

variable "client_secret" {
  type    = string
  default = ""
}

variable "tenant_id" {
  type    = string
  default = ""
}
`
}

// testAccProviderFactories returns a map of provider factories for acceptance tests.
func testAccProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return testAccProtoV6ProviderFactories
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
