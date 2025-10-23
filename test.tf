terraform {
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  # Configuration will be done via environment variables for testing
}
