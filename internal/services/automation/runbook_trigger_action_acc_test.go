//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package automation_test

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccRunbookTriggerAction_Basic validates the DefaultAzureCredential-backed
// Azure authentication path against real Automation infrastructure.
// To run acceptance tests:
//
//	TF_ACC=1 go test -v -timeout 5m ./internal/services/automation/...
//
// And set the following environment variables as needed for your credential source:
//
//	AZURE_SUBSCRIPTION_ID (or ARM_SUBSCRIPTION_ID alias)
func TestAccRunbookTriggerAction_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckAutomation(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccRunbookTriggerConfig(t, "v1", acctest.Env(t, "ACC_TEST_RUNBOOK_NAME"), true),
			},
			{
				Config: testAccRunbookTriggerConfig(t, "v2", acctest.Env(t, "ACC_TEST_RUNBOOK_NAME"), true),
			},
		},
	})
}

func TestAccRunbookTriggerAction_InvalidRunbook(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckAutomation(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccRunbookTriggerConfig(t, "v1", acctest.Env(t, "ACC_TEST_RUNBOOK_NAME"), false),
			},
			{
				Config:      testAccRunbookTriggerConfig(t, "v2", "definitely-does-not-exist-acc-test", false),
				ExpectError: regexp.MustCompile("(?i)(creating runbook job|failed to create job|not found|404)"),
			},
		},
	})
}

func testAccRunbookTriggerConfig(t *testing.T, triggerValue, runbookName string, waitForCompletion bool) string {
	t.Helper()

	return fmt.Sprintf(`
terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

%s

action "azureactions_automation_runbook_trigger" "acc" {
  config {
    automation_account_name = %s
    resource_group_name     = %s
    runbook_name            = %s
    parameters = {
      Source   = "terraform-provider-azureactions-acc"
      Trigger  = %s
      TestType = "automation"
    }
    wait_for_completion = %t
    timeout_minutes     = 20
  }
}

resource "terraform_data" "trigger" {
  input = %s

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_automation_runbook_trigger.acc]
    }
  }
}
`,
		acctest.ProviderConfigFromEnv(t),
		acctest.Q(acctest.Env(t, "ACC_TEST_AUTOMATION_ACCOUNT")),
		acctest.Q(acctest.Env(t, "ACC_TEST_RG")),
		acctest.Q(runbookName),
		acctest.Q(triggerValue),
		waitForCompletion,
		acctest.Q(triggerValue),
	)
}
