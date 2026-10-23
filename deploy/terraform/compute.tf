# One Arm server with 2 OCPUs and 12 GB. k3s runs on it as a single-node
# Kubernetes cluster.

data "oci_identity_availability_domains" "all" {
  compartment_id = var.tenancy_ocid
}

data "oci_core_images" "ubuntu" {
  compartment_id = local.compartment_id
  display_name   = var.image_name
  shape          = "VM.Standard.A1.Flex"
}

resource "oci_core_instance" "server" {
  compartment_id      = local.compartment_id
  availability_domain = data.oci_identity_availability_domains.all.availability_domains[var.availability_domain - 1].name
  display_name        = local.name

  shape = "VM.Standard.A1.Flex"
  shape_config {
    ocpus         = 2
    memory_in_gbs = 12
  }

  source_details {
    source_type = "image"
    source_id   = one(data.oci_core_images.ubuntu.images).id
    # Room for the images, the database and the routing data.
    boot_volume_size_in_gbs = 50
  }
  # Without this, destroying the server would leave its disk behind.
  preserve_boot_volume = false

  create_vnic_details {
    subnet_id = oci_core_subnet.public.id
    # An ephemeral public IP. It changes if the server is replaced, which DNS
    # has to follow.
    assign_public_ip = true
    hostname_label   = "server"
  }

  metadata = {
    ssh_authorized_keys = trimspace(file(pathexpand(var.ssh_public_key_file)))
    # Runs once, at first boot: changing it means replacing the server.
    user_data = base64encode(templatefile("${path.module}/cloud-init.yaml.tftpl", {
      setup_script = templatefile("${path.module}/setup-node.sh.tftpl", {
        k3s_version        = var.k3s_version
        k3s_version_url    = replace(var.k3s_version, "+", "%2B")
        k3s_sha256         = var.k3s_sha256
        k3s_install_sha256 = var.k3s_install_sha256
      })
    }))
  }

  instance_options {
    # Only the metadata service version that needs a session token, which a
    # request forged through an app on the server can't easily get.
    are_legacy_imds_endpoints_disabled = true
  }
}
