terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  subscription_id = "00000000-0000-0000-0000-000000000000" # Can come from AZURE_SUBSCRIPTION_ID / ARM_SUBSCRIPTION_ID.
}

variable "eventgrid_publish_endpoint" {
  type        = string
  description = "Event Grid publish endpoint, e.g. https://<topic>.<region>-1.eventgrid.azure.net/api/events"
}

variable "order_id" {
  type        = string
  description = "Sample order id to include in event payload"
}

resource "terraform_data" "example" {
  input = var.order_id

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.azureactions_eventgrid_publish_event.example]
    }
  }
}

action "azureactions_eventgrid_publish_event" "example" {
  config {
    endpoint_url = var.eventgrid_publish_endpoint

    cloud_event {
      source          = "terraform-provider-azureactions/examples"
      type            = "com.webedmj.order.updated"
      subject         = "orders/${var.order_id}"
      time            = timestamp()
      datacontenttype = "application/json"
      data = {
        orderId = var.order_id
        status  = "processed"
      }
      cloud_event_extensions = {
        environment = "example"
      }
    }
  }
}
