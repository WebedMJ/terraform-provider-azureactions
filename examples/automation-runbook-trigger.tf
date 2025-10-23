# Example Terraform configuration using the Azure Actions provider
# to trigger Azure Automation runbooks

terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.0"
    }
  }
}

provider "azurerm" {
  features {}
}

provider "azureactions" {
  # Authentication can be configured via provider config or environment variables:
  # subscription_id = "your-subscription-id"
  # client_id       = "your-client-id"
  # client_secret   = "your-client-secret"
  # tenant_id       = "your-tenant-id"
}

# Example: Trigger a runbook when a VM is updated
resource "azurerm_resource_group" "example" {
  name     = "rg-terraform-actions-example"
  location = "East US"
}

resource "azurerm_automation_account" "example" {
  name                = "automation-terraform-actions"
  location            = azurerm_resource_group.example.location
  resource_group_name = azurerm_resource_group.example.name
  sku_name           = "Basic"
}

# Define an automation runbook trigger action
action "azureactions_automation_runbook_trigger" "vm_maintenance" {
  config {
    automation_account_name = azurerm_automation_account.example.name
    resource_group_name     = azurerm_resource_group.example.name
    runbook_name           = "VM-Maintenance-Tasks"
    parameters = {
      ResourceGroupName = azurerm_resource_group.example.name
      MaintenanceType   = "Update"
      Timestamp         = timestamp()
    }
    wait_for_completion = true
    timeout_minutes     = 20
  }
}

# Example VM that triggers maintenance runbook on updates
resource "azurerm_virtual_network" "example" {
  name                = "vnet-example"
  address_space       = ["10.0.0.0/16"]
  location            = azurerm_resource_group.example.location
  resource_group_name = azurerm_resource_group.example.name
}

resource "azurerm_subnet" "example" {
  name                 = "subnet-example"
  resource_group_name  = azurerm_resource_group.example.name
  virtual_network_name = azurerm_virtual_network.example.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_network_interface" "example" {
  name                = "nic-example"
  location            = azurerm_resource_group.example.location
  resource_group_name = azurerm_resource_group.example.name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.example.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_linux_virtual_machine" "example" {
  name                = "vm-example"
  resource_group_name = azurerm_resource_group.example.name
  location            = azurerm_resource_group.example.location
  size                = "Standard_B1s"
  admin_username      = "azureuser"

  disable_password_authentication = true

  network_interface_ids = [
    azurerm_network_interface.example.id,
  ]

  admin_ssh_key {
    username   = "azureuser"
    public_key = file("~/.ssh/id_rsa.pub") # Replace with your SSH public key path
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts"
    version   = "latest"
  }

  # Trigger automation runbook when VM is updated
  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_automation_runbook_trigger.vm_maintenance]
    }
  }

  tags = {
    Environment = "Demo"
    Purpose     = "Terraform Actions Example"
  }
}