terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  organization_url = var.organization_url
}

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

variable "template_parameters" {
  type        = map(string)
  description = "Map of template parameters to pass when triggering the pipeline"
  default     = null
}

resource "terraform_data" "example" {
  input = timestamp()

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.azureactions_devops_pipeline_trigger.example]
    }
  }
}

action "azureactions_devops_pipeline_trigger" "example" {
  config {
    project             = var.project
    pipeline_id         = var.pipeline_id
    auth_method         = "default_azure_credential"
    wait_for_completion = true
    template_parameters = var.template_parameters
  }
}
