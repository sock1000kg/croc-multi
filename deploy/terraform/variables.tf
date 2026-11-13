variable "location" {
  description = "Azure region. Pick one close to the team to cut latency."
  type        = string
  default     = "Southeast Asia"
}

variable "project_name" {
  description = "Prefix for resource names."
  type        = string
  default     = "croc-multi"
}

variable "admin_ssh_key_path" {
  description = "Path to the local SSH public key."
  type        = string
}

variable "ssh_allowed_cidr" {
  description = "CIDR allowed to SSH (your home IP, e.g. 203.0.113.5/32)."
  type        = string
}

variable "relay_ports" {
  description = "TCP ports the croc relay listens on (first is the main one)."
  type        = list(string)
  default     = ["9009", "9010", "9011", "9012", "9013"]
}
