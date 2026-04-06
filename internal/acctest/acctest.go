//go:build acceptance

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"os"
	"strconv"
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
	RequireEnv(t,
		"ARM_SUBSCRIPTION_ID",
		"ARM_CLIENT_ID",
		"ARM_CLIENT_SECRET",
		"ARM_TENANT_ID",
	)
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
func PreCheckDevOps(t *testing.T) {
	t.Helper()
	PreCheckCommon(t)
	RequireEnv(t,
		"AZUREDEVOPS_ORG_URL",
		"AZUREDEVOPS_PROJECT",
		"AZUREDEVOPS_PIPELINE_ID",
		"AZUREDEVOPS_PAT",
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

// ProviderConfigFromEnv returns a provider block wired from ARM_* environment variables.
func ProviderConfigFromEnv(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf(`
provider "azureactions" {
  subscription_id = %s
  client_id       = %s
  client_secret   = %s
  tenant_id       = %s
}
`,
		Q(Env(t, "ARM_SUBSCRIPTION_ID")),
		Q(Env(t, "ARM_CLIENT_ID")),
		Q(Env(t, "ARM_CLIENT_SECRET")),
		Q(Env(t, "ARM_TENANT_ID")),
	)
}
