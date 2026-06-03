//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops_test

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccPipelineTriggerAction_Basic validates the DefaultAzureCredential-backed
// Azure DevOps auth path against real infrastructure.
// To run acceptance tests:
//
//	TF_ACC=1 go test -v -timeout 5m ./internal/services/devops/...
//
// And set the following environment variables as needed for your credential source:
//
//	AZURE_SUBSCRIPTION_ID (or ARM_SUBSCRIPTION_ID alias)
//	AZUREDEVOPS_ORG_URL
//	AZUREDEVOPS_PROJECT
//	AZUREDEVOPS_PIPELINE_ID
func TestAccPipelineTriggerAction_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckDevOps(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPipelineTriggerConfigDAC(t, "v1", true, acctest.Env(t, "AZUREDEVOPS_PIPELINE_ID")),
			},
			{
				Config: testAccPipelineTriggerConfigDAC(t, "v2", true, acctest.Env(t, "AZUREDEVOPS_PIPELINE_ID")),
			},
		},
	})
}

func TestAccPipelineTriggerAction_InvalidPipelineID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckDevOps(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPipelineTriggerConfigDAC(t, "v1", false, acctest.Env(t, "AZUREDEVOPS_PIPELINE_ID")),
			},
			{
				Config:      testAccPipelineTriggerConfigDAC(t, "v2", false, "-1"),
				ExpectError: regexp.MustCompile("(?i)pipeline_id must be greater than 0"),
			},
		},
	})
}

func testAccPipelineTriggerConfigDAC(t *testing.T, triggerValue string, waitForCompletion bool, pipelineID string) string {
	t.Helper()

	parsedPipelineID, err := strconv.ParseInt(pipelineID, 10, 64)
	if err != nil {
		t.Fatalf("AZUREDEVOPS_PIPELINE_ID must be a valid integer, got %q: %v", pipelineID, err)
	}

	return fmt.Sprintf(`
terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

%s

action "azureactions_devops_pipeline_trigger" "acc" {
  config {
		project             = %s
		pipeline_id         = %d
		auth_method         = "default_azure_credential"
		branch_ref          = "refs/heads/main"
    variables = {
      AccTestSource = "terraform-provider-azureactions"
      TriggerValue  = %s
    }
    wait_for_completion = %t
    timeout_minutes     = 30
  }
}

resource "terraform_data" "trigger" {
  input = %s

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_devops_pipeline_trigger.acc]
    }
  }
}
`,
		acctest.ProviderConfigFromEnv(t),
		acctest.Q(acctest.Env(t, "AZUREDEVOPS_PROJECT")),
		parsedPipelineID,
		acctest.Q(triggerValue),
		waitForCompletion,
		acctest.Q(triggerValue),
	)
}
