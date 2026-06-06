//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package eventgrid_test

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccPublishEventAction_Basic validates Event Grid publish with
// default_azure_credential authentication against real infrastructure.
// To run acceptance tests:
//
//	TF_ACC=1 go test -v -timeout 5m ./internal/services/eventgrid/...
//
// Required environment variables:
//
//	AZURE_SUBSCRIPTION_ID (or ARM_SUBSCRIPTION_ID alias)
//	ACC_TEST_EVENTGRID_ENDPOINT
func TestAccPublishEventAction_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckEventGrid(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPublishEventConfig(t, "v1", false),
			},
			{
				Config: testAccPublishEventConfig(t, "v2", false),
			},
		},
	})
}

func TestAccPublishEventAction_InvalidPayload(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckEventGrid(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPublishEventConfig(t, "v1", false),
			},
			{
				Config:      testAccPublishEventConfig(t, "v2", true),
				ExpectError: regexp.MustCompile("(?i)cloud_event\\[0\\]\\.time must be an RFC3339 timestamp"),
			},
		},
	})
}

func testAccPublishEventConfig(t *testing.T, marker string, invalidPayload bool) string {
	t.Helper()

	timeValue := "2026-06-06T00:00:00Z"

	if invalidPayload {
		timeValue = "invalid-time"
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

action "azureactions_eventgrid_publish_cloudevent" "acc" {
  config {
		endpoint_url    = %s
		timeout_seconds = 30

		cloud_event {
			source          = "/terraform-provider-azureactions/acc"
			type            = "com.webedmj.eventgrid.acceptance"
			subject         = %s
			time            = %s
			datacontenttype = "application/json"
			data = {
				marker = %s
			}
		}
  }
}

resource "terraform_data" "trigger" {
  input = %s

  lifecycle {
    action_trigger {
      events  = [after_update]
			actions = [action.azureactions_eventgrid_publish_cloudevent.acc]
    }
  }
}
`,
		acctest.ProviderConfigFromEnv(t),
		acctest.Q(acctest.Env(t, "ACC_TEST_EVENTGRID_ENDPOINT")),
		acctest.Q("tests/"+marker),
		acchtest.Q(timeValue),
		acchtest.Q(marker),
		acctest.Q(marker),
	)
}
