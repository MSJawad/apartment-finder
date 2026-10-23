# A virtual network with one public subnet. The security list is the
# server's outer firewall.

locals {
  compartment_id = coalesce(var.compartment_ocid, var.tenancy_ocid)
  name           = "apartment-finder"
}

resource "oci_core_vcn" "main" {
  compartment_id = local.compartment_id
  display_name   = local.name
  cidr_blocks    = ["10.0.0.0/16"]
  dns_label      = "apartment"
}

resource "oci_core_internet_gateway" "main" {
  compartment_id = local.compartment_id
  vcn_id         = oci_core_vcn.main.id
  display_name   = local.name
  enabled        = true
}

resource "oci_core_route_table" "public" {
  compartment_id = local.compartment_id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name}-public"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.main.id
  }
}

# Stateful rules: replies to allowed traffic are let back in automatically.
resource "oci_core_security_list" "public" {
  compartment_id = local.compartment_id
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${local.name}-public"

  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "all"
  }

  ingress_security_rules {
    description = "SSH"
    source      = var.ssh_source_cidr
    protocol    = "6" # TCP
    tcp_options {
      min = 22
      max = 22
    }
  }

  ingress_security_rules {
    description = "HTTP, for Let's Encrypt and the redirect to HTTPS"
    source      = "0.0.0.0/0"
    protocol    = "6"
    tcp_options {
      min = 80
      max = 80
    }
  }

  ingress_security_rules {
    description = "HTTPS"
    source      = "0.0.0.0/0"
    protocol    = "6"
    tcp_options {
      min = 443
      max = 443
    }
  }

  # "Fragmentation needed" messages, without which connections over paths
  # with a smaller MTU hang.
  ingress_security_rules {
    description = "Path MTU discovery"
    source      = "0.0.0.0/0"
    protocol    = "1" # ICMP
    icmp_options {
      type = 3
      code = 4
    }
  }
}

resource "oci_core_subnet" "public" {
  compartment_id    = local.compartment_id
  vcn_id            = oci_core_vcn.main.id
  display_name      = "${local.name}-public"
  cidr_block        = "10.0.1.0/24"
  dns_label         = "public"
  route_table_id    = oci_core_route_table.public.id
  security_list_ids = [oci_core_security_list.public.id]
}
