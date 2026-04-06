// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

import (
	"os"
	"testing"
)

// TestAccPipelineTriggerAction_Basic is a placeholder acceptance test.
// Acceptance tests require real Azure DevOps infrastructure and credentials.
// To run acceptance tests:
//
//	TF_ACC=1 go test -v -timeout 5m ./internal/services/devops/...
//
// And set the following environment variables:
//	ARM_SUBSCRIPTION_ID
//	ARM_CLIENT_ID
//	ARM_CLIENT_SECRET
//	ARM_TENANT_ID
//	AZUREDEVOPS_ORG_URL
//	AZUREDEVOPS_PROJECT
//	AZUREDEVOPS_PIPELINE_ID
//	AZUREDEVOPS_PAT (Personal Access Token with Build permission)
func TestAccPipelineTriggerAction_Basic(t *testing.T) {
	// Skip if not running acceptance tests
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Skipping acceptance test; set TF_ACC=1 to run")
	}

	// Acceptance tests are placeholder. Full implementation requires:
	// 1. Real Azure subscription with service principal credentials
	// 2. Real Azure DevOps organization, project, and pipeline
	// 3. Valid Personal Access Token with Build (Read & execute) permission
	//
	// This test will be expanded when DevOps infrastructure is available for testing
	t.Skip("Acceptance tests not yet fully implemented - requires Azure DevOps infrastructure")
}

