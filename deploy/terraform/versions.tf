# The server and its network on Oracle Cloud.
#
# Terraform runs from a laptop only, never from CI, so cloud credentials
# never reach GitHub. The provider reads them from ~/.oci/config; nothing
# in this directory is secret. The state stays local and gitignored.

terraform {
  required_version = ">= 1.12"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "9.10.0"
    }
  }
}

provider "oci" {
  region = var.region
}
