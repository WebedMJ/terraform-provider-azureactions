terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }

    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~>4.0"
    }
  }
}

# provider "azureactions" {
#   subscription_id = var.subscription_id
#   client_id       = var.client_id
#   client_secret   = var.client_secret
#   tenant_id       = var.tenant_id
# }

# variable "subscription_id" {
#   type = string
# }

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

variable "organization_url" {
  type        = string
  description = "Azure DevOps organization URL"
}

variable "project" {
  type        = string
  description = "Azure DevOps project name"
}

variable "pipeline_id" {
  type        = number
  description = "Azure DevOps pipeline ID"
}

# variable "personal_access_token" {
#   type        = string
#   sensitive   = true
#   description = "Azure DevOps Personal Access Token"
# }

resource "terraform_data" "example2" {
  input = timestamp()

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.azureactions_devops_pipeline_trigger.example]
    }
  }
}

action "azureactions_devops_pipeline_trigger" "example" {
  config {
    organization_url    = var.organization_url
    project             = var.project
    pipeline_id         = var.pipeline_id
    auth_method         = "default_azure_credential"
    wait_for_completion = true
    # template_parameters = {
    #   deployTarget = "eastus"
    # }
  }
}

output "action_invoked" {
  value       = "DevOps pipeline trigger action configured"
  description = "Indicates that the DevOps pipeline trigger action has been set up"
}

output "action_output_note" {
  value       = "Pipeline run details (run ID/state/result) are emitted as action progress events during apply."
  description = "Current Terraform action model does not expose provider-defined action outputs as normal expression values."
}
