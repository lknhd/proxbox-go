# Proxbox

SSH-managed Linux containers on Proxmox. No signup. Your SSH key is your identity.

```
  ____  ____   _____  ______   _____  __ __  
 |  _ \|  _ \ / _ \ \/ / __ ) / _ \ \/ / \ \ 
 | |_) | |_) | | | \  /|  _ \| | | \  /   \ \
 |  __/|  _ <| |_| /  \| |_) | |_| /  \   / /
 |_|   |_| \_\\___/_/\_\____/ \___/_/\_\ /_/ 
```

Proxbox is a [shellbox.dev](https://shellbox.dev)-like service backed by Proxmox VE. Users create and manage LXC containers entirely through SSH commands. Containers automatically pause on disconnect and resume on reconnect.

## Quick Start

### Prerequisites

- Proxmox VE with API token access
- Ubuntu 24.04 LXC template downloaded on Proxmox
- Docker and Docker Compose (for containerized deployment)

### Setup

```bash
git clone https://github.com/lknhd/proxbox-go.git
cd proxbox-go

# Generate SSH keys
ssh-keygen -t ed25519 -f ssh_host_key -N ''
ssh-keygen -t ed25519 -f ssh_gateway_key -N ''

# Create config
cp config.example.yaml config.yaml
# Edit config.yaml with your Proxmox details

# Run
docker compose up -d
```

### Download the template on Proxmox

```bash
pveam download local ubuntu-24.04-standard_24.04-2_amd64.tar.zst
```

### Create an API token on Proxmox

Datacenter → Permissions → API Tokens → Add. Uncheck "Privilege Separation" for full access.

## Usage

```bash
ssh -p 2222 yourserver help                  # Show commands
ssh -p 2222 yourserver create dev            # Create a small container
ssh -p 2222 yourserver create big large      # Create a large container
ssh -p 2222 yourserver list                  # List your containers
ssh -tt -p 2222 yourserver ssh dev           # Connect to a container
ssh -p 2222 yourserver stop dev              # Stop a container
ssh -p 2222 yourserver destroy dev           # Delete a container
```

### SSH Config

Add to `~/.ssh/config` for convenience:

```
Host proxbox
    HostName yourserver
    Port 2222
    RequestTTY force
```

Then:

```bash
ssh proxbox create dev
ssh proxbox ssh dev
ssh proxbox list
```

## Commands

| Command | Description |
|---------|-------------|
| `help` | Show available commands |
| `create <name> [size]` | Create a new container |
| `list` | List your containers |
| `ssh <name>` | Connect to a container (auto-starts/resumes) |
| `start <name>` | Start a stopped container |
| `stop <name>` | Stop a running container |
| `destroy <name>` | Permanently delete a container |

## Container Sizes

| Size | vCPU | RAM | Disk |
|------|------|-----|------|
| small | 1 | 1 GB | 8 GB |
| medium | 2 | 4 GB | 8 GB |
| large | 4 | 8 GB | 8 GB |

## How It Works

```
┌──────────┐    SSH    ┌──────────────┐   Proxmox API   ┌──────────┐
│  You     │ ───────── │  Proxbox     │ ─────────────── │  Proxmox │
│  (SSH)   │  :2222    │  Gateway     │                 │  VE      │
└──────────┘           └──────┬───────┘                 └────┬─────┘
                              │ SSH :22                      │
                              ▼                              │
                       ┌──────────────┐                      │
                       │  LXC         │ ◄────────────────────┘
                       │  Container   │
                       └──────────────┘
```

- **SSH gateway** accepts any SSH public key — your key fingerprint is your identity
- **Commands** are parsed from the SSH exec request (`ssh host <command>`)
- **Containers** are LXC on Proxmox, created via the Proxmox REST API
- **SSH proxy** opens a second SSH connection from the gateway into the container
- **Pause/Resume** uses Proxmox snapshots — filesystem state persists across cycles
- **Container naming** follows `proxbox-{username}-{name}` for easy identification in Proxmox

## Configuration

See [config.example.yaml](config.example.yaml) for all options.

| Field | Description |
|-------|-------------|
| `proxmox.host` | Proxmox hostname |
| `proxmox.port` | Proxmox API port (default: 8006) |
| `proxmox.token_name` | API token name |
| `proxmox.token_value` | API token secret |
| `proxmox.node` | Proxmox node name |
| `proxmox.storage` | Storage for containers (e.g. `local-zfs`) |
| `proxmox.template` | LXC template path |
| `proxmox.bridge` | Network bridge (e.g. `vmbr0`) |
| `gateway.port` | SSH gateway port (default: 2222) |
| `gateway.host_key_path` | Path to SSH host private key |
| `gateway.gateway_key_path` | Path to gateway SSH key (injected into containers) |
| `gateway.db_path` | Path to SQLite database |

## Build from Source

```bash
go build -o proxbox-go .
```

Cross-compile for Linux:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o proxbox-go .
```

No CGO required — uses [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go).

## License

MIT
