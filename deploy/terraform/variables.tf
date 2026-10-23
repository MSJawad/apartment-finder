variable "tenancy_ocid" {
  description = "The tenancy (account) OCID, from ~/.oci/config."
  type        = string
}

variable "compartment_ocid" {
  description = "The compartment to create everything in. Defaults to the tenancy's root compartment."
  type        = string
  default     = null
}

variable "region" {
  description = "Must be the tenancy's home region."
  type        = string
  default     = "eu-frankfurt-1"
}

variable "availability_domain" {
  description = <<-EOT
    Which availability domain (1-3) to put the server in. Arm capacity is
    tracked per domain: on an "Out of host capacity" error, try another one.
  EOT
  type        = number
  default     = 1

  validation {
    condition     = contains([1, 2, 3], var.availability_domain)
    error_message = "Frankfurt has availability domains 1, 2 and 3."
  }
}

variable "ssh_public_key_file" {
  description = "The public key allowed to log in as ubuntu. Password login is off."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "ssh_source_cidr" {
  description = "Who may reach SSH. Open by default: logins need the key."
  type        = string
  default     = "0.0.0.0/0"
}

variable "image_name" {
  description = <<-EOT
    The Ubuntu image, by its exact name, so a rebuild uses the same image.
    Bump it deliberately; the server keeps itself patched in between.
  EOT
  type        = string
  default     = "Canonical-Ubuntu-24.04-aarch64-2026.09.18-0"
}

# k3s is pinned to a release, and both the binary and the install script are
# checked against these hashes before anything runs. Bumping the version
# means updating all three, from the release's sha256sum-arm64.txt and the
# install.sh at its tag.
variable "k3s_version" {
  type    = string
  default = "v1.36.5+k3s1"
}

variable "k3s_sha256" {
  description = "sha256 of the k3s-arm64 binary of k3s_version."
  type        = string
  default     = "135e34cb9e8a1cfae3cb55577789e93501efe7edc080a8b2630458a85b07721e"
}

variable "k3s_install_sha256" {
  description = "sha256 of install.sh at the k3s_version tag."
  type        = string
  default     = "46177d4c99440b4c0311b67233823a8e8a2fc09693f6c89af1a7161e152fbfad"
}
