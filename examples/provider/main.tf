terraform {
  required_providers {
    icx = {
      source = "pgehres/fastiron-icx"
    }
  }
}

provider "icx" {
  host     = "10.0.1.1"
  username = var.switch_username
  password = var.switch_password
  # enable_password = var.enable_password  # uncomment if needed
}

variable "switch_username" {
  type      = string
  sensitive = true
}

variable "switch_password" {
  type      = string
  sensitive = true
}
