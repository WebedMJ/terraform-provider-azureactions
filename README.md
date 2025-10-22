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

```hcl
# Define an action
action "azureactions_virtual_machine_power" "restart_vm" {
  config {
    virtual_machine_id = azurerm_linux_virtual_machine.example.id
    power_action       = "restart"
  }
}

# Trigger the action on resource changes
resource "azurerm_linux_virtual_machine" "example" {
  # ... VM configuration ...

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_virtual_machine_power.restart_vm]
    }
  }
}
```

## Actions

This provider is designed to support various Azure actions. The provider structure is ready for implementing actions such as:

- Virtual Machine power operations (start, stop, restart)
- App Service deployment slots management
- Database scaling operations
- Storage account maintenance tasks
- And more...

## Development

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Formatting

```bash
make fmt
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MPL-2.0 License - see the LICENSE file for details.