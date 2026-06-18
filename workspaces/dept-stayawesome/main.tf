terraform {
  required_providers {
    coder  = { source = "coder/coder" }
    docker = { source = "kreuzwerker/docker" }
  }
}

# --- Tenant-scope parameters ---------------------------------------------
# Default-deny: ONLY the listed source paths get bind-mounted. No /opt host
# binds, no /root binds, no docker.sock, no global Vault.

variable "tenant" {
  description = "Tenant slug — matches Authentik group and Secrets-Broker policy."
  type        = string
  default     = "dept-stayawesome"
}

variable "tenant_repos_host_path" {
  description = "Host path on werkbank holding ONLY this tenant's repos."
  type        = string
  default     = "/srv/tenants/dept-stayawesome/repos"
}

variable "workspace_repos_mount" {
  description = "Mount target inside the workspace. Also the value of PLAN_REPO_ROOTS."
  type        = string
  default     = "/home/coder/repos"
}

variable "workspace_image" {
  description = "Pre-built workspace image (built via image/Dockerfile)."
  type        = string
  default     = "stack/coder-dept-stayawesome:latest"
}

# --- Coder agent & apps ---------------------------------------------------

data "coder_workspace" "me" {}
data "coder_workspace_owner" "me" {}

resource "coder_agent" "main" {
  arch                   = "amd64"
  os                     = "linux"
  startup_script_timeout = 180
  startup_script         = <<-EOT
    set -eu
    # Verify the mount whitelist took effect — fail fast if a regression
    # ever lets a forbidden host path slip in.
    if [ -e /opt/quantbot ] || [ -e /root/.secrets ]; then
      echo "FATAL: forbidden host path visible in workspace" >&2
      exit 13
    fi
    # Claude Code is baked into the image; gt-plan is on PATH. Just stay up.
    exec sleep infinity
  EOT

  env = {
    TENANT          = var.tenant
    PLAN_REPO_ROOTS = var.workspace_repos_mount
  }
}

resource "coder_app" "claude_code" {
  agent_id     = coder_agent.main.id
  slug         = "claude-code"
  display_name = "Claude Code"
  command      = "claude-code"
  icon         = "/icon/code.svg"
}

# --- Container with strict mount whitelist --------------------------------

resource "docker_container" "workspace" {
  count    = data.coder_workspace.me.start_count
  image    = var.workspace_image
  name     = "ws-${var.tenant}-${data.coder_workspace_owner.me.name}-${data.coder_workspace.me.name}"
  hostname = data.coder_workspace.me.name
  user     = "1000:1000"

  # Whitelist: exactly one bind, the tenant's repo tree. Read-write inside.
  # NO docker.sock, NO /opt host bind, NO /root bind, NO global Vault.
  mounts {
    type      = "bind"
    source    = var.tenant_repos_host_path
    target    = var.workspace_repos_mount
    read_only = false
  }

  # Hardening
  privileged    = false
  security_opts = ["no-new-privileges:true"]
  capabilities {
    drop = ["ALL"]
  }

  env = [
    "CODER_AGENT_TOKEN=${coder_agent.main.token}",
    "CODER_AGENT_URL=${data.coder_workspace.me.access_url}",
    "TENANT=${var.tenant}",
    "PLAN_REPO_ROOTS=${var.workspace_repos_mount}",
  ]

  entrypoint = ["sh", "-c", coder_agent.main.init_script]
}
