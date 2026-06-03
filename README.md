# terraform-provider-azureactions

A Terraform provider for Azure Actions using the new actions capability introduced in Terraform 1.14.

## Overview

This provider enables Terraform actions for Azure resources, allowing you to perform operational tasks like restarting virtual machines, scaling resources, or triggering maintenance operations as part of your Terraform workflow.

## Requirements

- [Terraform](https://www.terraform.io/downloads.html) >= 1.14
- [Go](https://golang.org/doc/install) >= 1.24.5 (for building from source)

## Building the Provider

Clone the repository:

```bash
git clone https://github.com/WebedMJ/terraform-provider-azureactions
cd terraform-provider-azureactions
```

Build and install the provider:

```bash
make build
```

## Configuration

The provider uses Azure Identity / DefaultAzureCredential behavior for Azure authentication.

Recommended local development flow:

```bash
az login
export AZURE_SUBSCRIPTION_ID="your-subscription-id"
```

Recommended CI/authenticated runtime flows:

1. Workload identity / OIDC via `AZURE_*` environment variables
2. Managed identity
3. Azure CLI / Azure Developer CLI / Azure PowerShell developer sign-in
4. Explicit client secret override via provider config or environment variables

Explicit service principal configuration remains supported as an override:

```hcl
provider "azureactions" {
  subscription_id = "your-subscription-id"
  client_id       = "your-client-id"
  client_secret   = "your-client-secret"
  tenant_id       = "your-tenant-id"
  organization_url = "https://dev.azure.com/myorg" # Optional, required only for DevOps actions
  environment     = "public" # Optional: public, usgovernment, china
}
```

Environment variables supported by the provider include:

- `AZURE_SUBSCRIPTION_ID` (or `ARM_SUBSCRIPTION_ID` alias)
- `AZURE_CLIENT_ID` (or `ARM_CLIENT_ID` alias)
- `AZURE_CLIENT_SECRET` (or `ARM_CLIENT_SECRET` alias)
- `AZURE_TENANT_ID` (or `ARM_TENANT_ID` alias)
- `ARM_ENVIRONMENT`
- `AZUREDEVOPS_ORG_URL` (optional, required for DevOps actions when not set in provider)

Precedence is consistent for all of the above: provider block value first, then `AZURE_*`, then `ARM_*` alias.

When no explicit client secret configuration is provided, the provider falls back to DefaultAzureCredential-compatible resolution, which allows local Azure CLI authentication and CI identity-based authentication without changing Terraform configuration.

## Usage

Actions are used with the new Terraform 1.14 action syntax:

### Azure Automation Runbook Trigger

```hcl
# Define an automation runbook trigger action
action "azureactions_automation_runbook_trigger" "restart_services" {
  config {
    automation_account_name = "my-automation-account"
    resource_group_name     = "my-resource-group"
    runbook_name           = "Restart-Services"
    parameters = {
      ServiceName = "MyService"
      Environment = "Production"
    }
    wait_for_completion = true
    timeout_minutes     = 15
  }
}

# Trigger the action on resource changes
resource "azurerm_linux_virtual_machine" "example" {
  # ... VM configuration ...

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_automation_runbook_trigger.restart_services]
    }
  }
}
```

### Example: Automated Maintenance Workflow

```hcl
terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

provider "azureactions" {
  subscription_id = var.subscription_id
  client_id       = var.client_id
  client_secret   = var.client_secret
  tenant_id       = var.tenant_id
}

# Pre-maintenance runbook
action "azureactions_automation_runbook_trigger" "pre_maintenance" {
  config {
    automation_account_name = azurerm_automation_account.example.name
    resource_group_name     = azurerm_resource_group.example.name
    runbook_name           = "Pre-Maintenance-Tasks"
    parameters = {
      ResourceGroup = azurerm_resource_group.example.name
      Timestamp     = timestamp()
    }
    wait_for_completion = true
    timeout_minutes     = 10
  }
}

# Post-maintenance runbook
action "azureactions_automation_runbook_trigger" "post_maintenance" {
  config {
    automation_account_name = azurerm_automation_account.example.name
    resource_group_name     = azurerm_resource_group.example.name
    runbook_name           = "Post-Maintenance-Tasks"
    wait_for_completion     = false  # Fire and forget
  }
}

resource "azurerm_linux_virtual_machine" "web_server" {
  # ... VM configuration ...

  lifecycle {
    action_trigger {
      events  = [before_update]
      actions = [action.azureactions_automation_runbook_trigger.pre_maintenance]
    }

    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_automation_runbook_trigger.post_maintenance]
    }
  }
}
```

### Azure DevOps Pipeline Trigger (PAT authentication)

```hcl
variable "devops_pat" {
  type      = string
  sensitive = true
}

action "azureactions_devops_pipeline_trigger" "deploy" {
  config {
    project               = "my-project"
    pipeline_id           = 42
    auth_method           = "pat"
    personal_access_token = var.devops_pat
    branch_ref            = "refs/heads/main"
    variables = {
      ENVIRONMENT = "production"
    }
    wait_for_completion = true
    timeout_minutes     = 30
  }
}

resource "azurerm_linux_virtual_machine" "app" {
  # ... VM configuration ...

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_devops_pipeline_trigger.deploy]
    }
  }
}
```

### Azure DevOps Pipeline Trigger (DefaultAzureCredential authentication)

```hcl
# auth_method = "default_azure_credential" reuses the provider-level Azure
# credential chain (Azure CLI locally, workload identity / managed identity /
# environment credentials in CI/runtime) to obtain an Azure AD token for
# Azure DevOps using scope 499b84ac-1321-427f-aa17-267ca6975798/.default.
# organization_url must be configured in the provider block.
action "azureactions_devops_pipeline_trigger" "deploy_dac" {
  config {
    project          = "my-project"
    pipeline_id      = 42
    auth_method      = "default_azure_credential"
    branch_ref       = "refs/heads/release"
    template_parameters = {
      deployTarget = "eastus"
    }
    wait_for_completion = false
  }
}
```

### Action Result Behavior

Terraform actions in the current Terraform/plugin framework model do not expose provider-defined expression outputs (for example `run_id`, `state`, or `result`) that can be referenced from `output`, `local`, or other expression contexts.

For this provider, operational result details are emitted via action progress events during `terraform apply`.

- Azure DevOps pipeline trigger emits run ID and initial state.
- When `wait_for_completion = true`, it also emits final state and result.

> **Security note (DevOps PAT):** Action schema attributes in Terraform 1.14+ do not support the `sensitive` flag. Always supply `personal_access_token` via a Terraform [sensitive variable](https://developer.hashicorp.com/terraform/language/values/variables#suppressing-values-in-cli-output) (`sensitive = true`) or a `TF_VAR_` environment variable to prevent the value appearing in plan/apply output.

## Actions

This provider supports various Azure actions:

### Azure Automation

- **`azureactions_automation_runbook_trigger`**: Triggers an Azure Automation runbook execution with optional parameter passing and completion waiting.

### Azure DevOps

- **`azureactions_devops_pipeline_trigger`**: Triggers an Azure DevOps pipeline run. Supports Personal Access Token (PAT) and DefaultAzureCredential-backed Microsoft Entra authentication (with `service_principal` retained as a backwards-compatible alias). Supports branch overrides, pipeline variables, template parameters, stage skipping, and optional waiting for completion.

### Planned Actions

The provider structure is ready for implementing additional Azure actions such as:

- App Service deployment slot management
- Database scaling operations
- Storage account maintenance tasks
- And more...

## Testing

### Running Unit Tests Manually

Unit tests use mock HTTP servers and require no real Azure or Azure DevOps credentials.

```bash
# Run all unit tests
make test

# Run tests with verbose output
go test ./... -v -timeout 5m

# Run tests for a specific package
go test ./internal/services/automation/... -v
go test ./internal/services/devops/... -v
```

### Running Acceptance Tests (real infrastructure required)

Acceptance tests execute real Azure Automation and Azure DevOps operations. They are build-tagged and run only when both `TF_ACC=1` and the `acceptance` build tag are provided.

#### Definitive Infrastructure Requirements

You must provision these resources before running acceptance tests.

##### Shared Azure Foundation

1. Azure subscription dedicated for test execution.
2. Azure identity with access to test resources. This can be a local Azure CLI login, workload identity, managed identity, or explicit service principal.
3. Resource group for acceptance tests.

Recommended minimum access for the Azure identity used during test execution:

1. `Contributor` on the acceptance-test resource group.
2. Ability to read and invoke Automation jobs in the Automation Account used for tests.

##### Azure Automation Infrastructure

1. Existing Automation Account in the test resource group.
2. Existing published runbook with name used by tests (`ACC_TEST_RUNBOOK_NAME`).
3. Runbook should be deterministic and complete quickly.
4. Runbook should accept string parameters (tests send `Source`, `Trigger`, and `TestType`).

##### Azure DevOps Infrastructure

1. Existing Azure DevOps organization.
2. Existing project.
3. Existing pipeline with a stable numeric pipeline ID.
4. The Microsoft Entra identity used by the provider must be recognized in Azure DevOps and granted permission to queue/read the target pipeline.
5. Pipeline should run non-interactively (no manual approvals/gates for test path).

#### Required Environment Variables (full list)

All acceptance test runs require these variables.

| Variable                      | Required For                                                    | Example Value                          |
| ----------------------------- | --------------------------------------------------------------- | -------------------------------------- |
| `AZURE_SUBSCRIPTION_ID`       | all acceptance tests (`ARM_SUBSCRIPTION_ID` supported as alias) | `11111111-2222-3333-4444-555555555555` |
| `ACC_TEST_RG`                 | automation tests                                                | `rg-azureactions-acc-eastus`           |
| `ACC_TEST_AUTOMATION_ACCOUNT` | automation tests                                                | `aa-azureactions-acc`                  |
| `ACC_TEST_RUNBOOK_NAME`       | automation tests                                                | `Run-AccTest-NoOp`                     |
| `AZUREDEVOPS_ORG_URL`         | devops tests                                                    | `https://dev.azure.com/contoso`        |
| `AZUREDEVOPS_PROJECT`         | devops tests                                                    | `platform-shared`                      |
| `AZUREDEVOPS_PIPELINE_ID`     | devops tests                                                    | `42`                                   |
| `AZURE_CLIENT_ID`             | optional, workload identity or user-assigned managed identity   | `aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee` |
| `AZURE_TENANT_ID`             | optional, workload identity / environment credential            | `99999999-8888-7777-6666-555555555555` |
| `AZURE_CLIENT_SECRET`         | optional, explicit service principal secret                     | `super-secret-sp-password`             |

#### Example Environment Setup

Linux/macOS (bash):

```bash
export AZURE_SUBSCRIPTION_ID="11111111-2222-3333-4444-555555555555"

export ACC_TEST_RG="rg-azureactions-acc-eastus"
export ACC_TEST_AUTOMATION_ACCOUNT="aa-azureactions-acc"
export ACC_TEST_RUNBOOK_NAME="Run-AccTest-NoOp"

export AZUREDEVOPS_ORG_URL="https://dev.azure.com/contoso"
export AZUREDEVOPS_PROJECT="platform-shared"
export AZUREDEVOPS_PIPELINE_ID="42"

# Optional if using workload identity or explicit service principal auth
export AZURE_CLIENT_ID="aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
export AZURE_TENANT_ID="99999999-8888-7777-6666-555555555555"
export AZURE_CLIENT_SECRET="super-secret-sp-password"
```

Windows PowerShell:

```powershell
$env:AZURE_SUBSCRIPTION_ID = "11111111-2222-3333-4444-555555555555"

$env:ACC_TEST_RG = "rg-azureactions-acc-eastus"
$env:ACC_TEST_AUTOMATION_ACCOUNT = "aa-azureactions-acc"
$env:ACC_TEST_RUNBOOK_NAME = "Run-AccTest-NoOp"

$env:AZUREDEVOPS_ORG_URL = "https://dev.azure.com/contoso"
$env:AZUREDEVOPS_PROJECT = "platform-shared"
$env:AZUREDEVOPS_PIPELINE_ID = "42"

# Optional if using workload identity or explicit service principal auth
$env:AZURE_CLIENT_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
$env:AZURE_TENANT_ID = "99999999-8888-7777-6666-555555555555"
$env:AZURE_CLIENT_SECRET = "super-secret-sp-password"
```

#### How to Run

Run only Automation acceptance tests:

```bash
TF_ACC=1 go test -tags=acceptance ./internal/services/automation/... -v -timeout 30m
```

Run only DevOps acceptance tests:

```bash
TF_ACC=1 go test -tags=acceptance ./internal/services/devops/... -v -timeout 30m
```

Run all acceptance tests via Makefile:

```bash
make testacc
```

### CI

Tests run automatically on every pull request via the GitHub Actions workflow defined in `.github/workflows/test.yml`. The workflow runs:

1. `gofmt` formatting check
2. `go vet` static analysis
3. All unit tests (`go test ./...`)

## Development

### Building

```bash
make build
```

### Formatting

```bash
make fmt
```

### Generating Documentation

Documentation is generated from schema and example HCL files using `tfplugindocs`:

```bash
make generate
```

Generated documentation will be placed in the `docs/` directory and can be published to the Terraform Registry.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MPL-2.0 License - see the LICENSE file for details.
