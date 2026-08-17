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

  # Obtain with: ssh-keyscan 10.0.1.1
  host_key = "10.0.1.1 ssh-rsa AAAAB3NzaC1yc2EAAAA..."

  # For lab use only — disables host key verification (MITM risk):
  # insecure_skip_host_key_verify = true
}

variable "switch_username" {
  type      = string
  sensitive = true
}

variable "switch_password" {
  type      = string
  sensitive = true
}
