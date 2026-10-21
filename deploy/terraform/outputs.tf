output "public_ip" {
  description = "The server's public IPv4 address."
  value       = oci_core_instance.server.public_ip
}

output "ssh" {
  description = "Log in to the server."
  value       = "ssh ubuntu@${oci_core_instance.server.public_ip}"
}
