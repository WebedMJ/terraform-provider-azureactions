# terraform-provider-azureactions

A Terraform provider for Azure Actions using the new actions capability introduced in Terraform 1.14.

## Overview

This provider enables Terraform actions for Azure resources, allowing you to perform operational tasks like restarting virtual machines, scaling resources, or triggering maintenance operations as part of your Terraform workflow.

## Requirements

- [Terraform](https://www.terraform.io/downloads.html) >= 1.14
- [Go](https://golang.org/doc/install) >= 1.24.5 (for building from source)

## Building the Provider

1. Clone the repository:
```bash
git clone https://github.com/WebedMJ/terraform-provider-azureactions
cd terraform-provider-azureactions
```

2. Build and install the provider:
```bash
make build
```

## Configuration

The provider supports authentication via service principal credentials:

```hcl
provider "azureactions" {
  subscription_id = "your-subscription-id"
  client_id       = "your-client-id"
  client_secret   = "your-client-secret"
  tenant_id       = "your-tenant-id"
  environment     = "public" # Optional: public, usgovernment, china
}
```

Alternatively, you can use environment variables:
- `ARM_SUBSCRIPTION_ID`
- `ARM_CLIENT_ID`
- `ARM_CLIENT_SECRET`
- `ARM_TENANT_ID`
- `ARM_ENVIRONMENT`

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
    organization_url      = "https://dev.azure.com/myorg"
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

### Azure DevOps Pipeline Trigger (service principal authentication)

```hcl
# auth_method = "service_principal" reuses the provider-level SP credentials
# to obtain an Azure AD token scoped to Azure DevOps automatically.
action "azureactions_devops_pipeline_trigger" "deploy_sp" {
  config {
    organization_url = "https://dev.azure.com/myorg"
    project          = "my-project"
    pipeline_id      = 42
    auth_method      = "service_principal"
    branch_ref       = "refs/heads/release"
    template_parameters = {
      deployTarget = "eastus"
    }
    wait_for_completion = false
  }
}
```

> **Security note (DevOps PAT):** Action schema attributes in Terraform 1.14+ do not support the `sensitive` flag. Always supply `personal_access_token` via a Terraform [sensitive variable](https://developer.hashicorp.com/terraform/language/values/variables#suppressing-values-in-cli-output) (`sensitive = true`) or a `TF_VAR_` environment variable to prevent the value appearing in plan/apply output.

## Actions

This provider supports various Azure actions:

### Azure Automation
- **`azureactions_automation_runbook_trigger`**: Triggers an Azure Automation runbook execution with optional parameter passing and completion waiting.

### Azure DevOps
- **`azureactions_devops_pipeline_trigger`**: Triggers an Azure DevOps pipeline run. Supports Personal Access Token (PAT) and service principal (Azure AD) authentication. Supports branch overrides, pipeline variables, template parameters, stage skipping, and optional waiting for completion.

### Planned Actions
The provider structure is ready for implementing additional Azure actions such as:
- Virtual Machine power operations (start, stop, restart)
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

### Running Acceptance Tests (requires real Azure credentials)

> **Note:** Acceptance tests are not yet implemented. The `make testacc` target and `TF_ACC`-tagged test files are placeholders for future end-to-end tests that make real API calls to Azure.

When acceptance tests are added, they will require the following environment variables:

```bash
export ARM_SUBSCRIPTION_ID="your-subscription-id"
export ARM_CLIENT_ID="your-client-id"
export ARM_CLIENT_SECRET="your-client-secret"
export ARM_TENANT_ID="your-tenant-id"

# Run acceptance tests (once implemented)
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

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MPL-2.0 License - see the LICENSE file for details.