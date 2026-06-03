terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  subscription_id = var.subscription_id
  client_id       = var.client_id
  client_secret   = var.client_secret
  tenant_id       = var.tenant_id
}

variable "subscription_id" {
  type = string
}

variable "client_id" {
  type = string
}

variable "client_secret" {
  type      = string
  sensitive = true
}

variable "tenant_id" {
  type = string
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

variable "personal_access_token" {
  type        = string
  sensitive   = true
  description = "Azure DevOps Personal Access Token"
}

action "azureactions_devops_pipeline_trigger" "example" {
  config {
    organization_url      = var.organization_url
    project               = var.project
    pipeline_id           = var.pipeline_id
    auth_method           = "pat"
    personal_access_token = var.personal_access_token
    wait_for_completion   = false
  }
}

output "action_invoked" {
  value       = "DevOps pipeline trigger action configured"
  description = "Indicates that the DevOps pipeline trigger action has been set up"
}
