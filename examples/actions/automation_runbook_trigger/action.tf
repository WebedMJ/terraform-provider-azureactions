terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  subscription_id = var.subscription_id
}

variable "subscription_id" {
  type = string
}

# variable "client_id" {
#   type = string
# }

# variable "client_secret" {
#   type      = string
#   sensitive = true
# }

# variable "tenant_id" {
#   type = string
# }

variable "automation_account_name" {
  type        = string
  description = "Name of the Azure Automation Account"
}

variable "resource_group_name" {
  type        = string
  description = "Name of the resource group containing the Automation Account"
}

variable "runbook_name" {
  type        = string
  description = "Name of the runbook to trigger"
}

resource "terraform_data" "example" {
  input = timestamp()

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.azureactions_automation_runbook_trigger.example]
    }
  }
}


action "azureactions_automation_runbook_trigger" "example" {
  config {
    automation_account_name = var.automation_account_name
    resource_group_name     = var.resource_group_name
    runbook_name            = var.runbook_name
    wait_for_completion     = false
    parameters = {
      testparam = "My test value"
    }
  }
}

output "action_invoked" {
  value       = "Automation runbook trigger action configured"
  description = "Indicates that the automation runbook trigger action has been set up"
}
