output "public_ip" {
  description = "Relay public IP; goes into the Ansible inventory and --relay <ip>:9009."
  value       = azurerm_public_ip.relay.ip_address
}

output "ssh_user" {
  value = azurerm_linux_virtual_machine.relay.admin_username
}
