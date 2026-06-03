//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	providerpkg "github.com/WebedMJ/terraform-provider-azureactions/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// ProtoV6ProviderFactories creates the provider server used by acceptance tests.
var ProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"azureactions": providerserver.NewProtocol6WithError(providerpkg.New("test")()),
}

// RequireEnv fails the test if any required environment variable is missing.
func RequireEnv(t *testing.T, keys ...string) {
	t.Helper()

	for _, key := range keys {
		if os.Getenv(key) == "" {
			t.Fatalf("%s must be set for acceptance tests", key)
		}
	}
}

// PreCheckCommon validates required Azure credentials.
func PreCheckCommon(t *testing.T) {
	t.Helper()
	if subscriptionID() == "" {
		t.Fatal("AZURE_SUBSCRIPTION_ID or ARM_SUBSCRIPTION_ID must be set for acceptance tests")
	}
}

// PreCheckAutomation validates environment variables for automation acceptance tests.
func PreCheckAutomation(t *testing.T) {
	t.Helper()
	PreCheckCommon(t)
	RequireEnv(t,
		"ACC_TEST_RG",
		"ACC_TEST_AUTOMATION_ACCOUNT",
		"ACC_TEST_RUNBOOK_NAME",
	)
}

// PreCheckDevOps validates environment variables for devops acceptance tests.
// Note: subscription_id is not required for DevOps actions; both PAT and
// DefaultAzureCredential auth operate independently of Azure subscription context.
func PreCheckDevOps(t *testing.T) {
	t.Helper()
	RequireEnv(t,
		"AZUREDEVOPS_ORG_URL",
		"AZUREDEVOPS_PROJECT",
		"AZUREDEVOPS_PIPELINE_ID",
	)
}

// Env returns an environment variable and fails the test if it is missing.
func Env(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s must be set for acceptance tests", key)
	}
	return value
}

// Q returns a quoted HCL string literal.
func Q(value string) string {
	return strconv.Quote(value)
}

// ProviderConfigFromEnv returns a provider block wired from canonical AZURE_* environment
// variables (with ARM_* aliases supported by provider resolution).
// subscription_id is omitted when not set, which is valid for DevOps PAT-only usage.
func ProviderConfigFromEnv(t *testing.T) string {
	t.Helper()

	subID := subscriptionID()
	env := strings.TrimSpace(firstEnv("ARM_ENVIRONMENT", "AZURE_ENVIRONMENT"))
	devOpsOrgURL := strings.TrimSpace(firstEnv("AZUREDEVOPS_ORG_URL"))

	var lines []string
	if subID != "" {
		lines = append(lines, fmt.Sprintf("  subscription_id = %s", Q(subID)))
	}
	if env != "" {
		lines = append(lines, fmt.Sprintf("  environment     = %s", Q(env)))
	}
	if devOpsOrgURL != "" {
		lines = append(lines, fmt.Sprintf("  organization_url = %s", Q(devOpsOrgURL)))
	}

	if len(lines) == 0 {
		return "provider \"azureactions\" {}\n"
	}

	return fmt.Sprintf("provider \"azureactions\" {\n%s\n}\n", strings.Join(lines, "\n"))
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}

	return ""
}

func subscriptionID() string {
	return firstEnv("AZURE_SUBSCRIPTION_ID", "ARM_SUBSCRIPTION_ID")
}
