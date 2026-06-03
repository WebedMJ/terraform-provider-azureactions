terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  subscription_id  = var.subscription_id
  client_id        = var.client_id
  client_secret    = var.client_secret
  tenant_id        = var.tenant_id
  organization_url = var.organization_url
}

variable "subscription_id" {
  type        = string
  description = "The Azure subscription ID"
}

variable "client_id" {
  type        = string
  description = "The Azure client ID"
}

variable "client_secret" {
  type        = string
  sensitive   = true
  description = "The Azure client secret"
}

variable "tenant_id" {
  type        = string
  description = "The Azure tenant ID"
}

variable "organization_url" {
  type        = string
  default     = ""
  description = "Optional Azure DevOps organization URL. Required for DevOps actions."
}
