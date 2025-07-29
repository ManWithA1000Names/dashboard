# Homelab Dashboard

A modern, cyberpunk-styled service status dashboard for home labs built with Go and designed for NixOS integration. Monitor all your self-hosted services from a single, beautiful interface.

## Features

- **Real-time Service Monitoring**: Checks service health via local port connectivity
- **Beautiful UI**: Cyberpunk-inspired design with glowing effects and animations
- **Dual Configuration**: Check local ports for availability while displaying public URLs in the UI
- **Auto-refresh**: Services are checked every 5 minutes automatically
- **Responsive Design**: Works perfectly on desktop, tablet, and mobile
- **NixOS Integration**: First-class NixOS module support
- **Configurable**: Easy configuration via JSON file or environment variables
- **Fast & Lightweight**: Built with Go for minimal resource usage

## Quick Start

### Option 1: Using Nix Flakes (Recommended)

```bash
HOMELAB_CONFIG=/path/to/config nix run 'github:manwitha1000names/dashboard'
# or if cloned locally
HOMELAB_CONFIG=/path/to/config nix run
```

### Option 2: Traditional Go Build

```bash
# Install Go 1.21 or later
go mod download
go build -o homelab-dashboard
HOMELAB_CONFIG=/path/to/config ./homelab-dashboard
```

The dashboard will be available at `http://localhost:8080`

## Configuration

### Method 1: JSON Configuration File

Create a `config.json` file:

```json
{
  "port": 8080,
  "title": "My Home Lab",
  "services": {
    "Portainer": {
      "port": 9000,
      "url": "https://portainer.mydomain.com"
    },
    "Grafana": {
      "port": 3000,
      "url": "https://grafana.mydomain.com"
    },
    "Prometheus": {
      "port": 9090,
      "url": "https://prometheus.mydomain.com"
    },
    "NextCloud": {
      "port": 8081,
      "url": "https://nextcloud.mydomain.com"
    },
    "Home Assistant": {
      "port": 8123,
      "url": "https://homeassistant.mydomain.com"
    },
    "Pi-hole": {
      "port": 8082,
      "url": "https://pihole.mydomain.com"
    }
  }
}
```

**Service Configuration Format:**

- `port`: Local port number to check for service availability
- `url`: Public URL to display in the UI and open when clicked

The dashboard will check if services are running by connecting to `localhost:<port>`, but display the public URL in the interface. This allows you to:

- Check local service availability without external dependencies
- Display user-friendly public URLs (with custom domains, HTTPS, etc.)
- Separate monitoring logic from UI presentation

### Method 2: Environment Variables

```bash
export HOMELAB_PORT=8080
export HOMELAB_TITLE="My Home Lab"
# JSON encoded
export HOMELAB_SERVICES='{"Grafana": {"port": 3000, "url": "http://localhost:3000"}, ... }'
```

### Method 3: NixOS Configuration

```nix
{
  services.homelab-dashboard = {
    enable = true;
    port = 8080;
    title = "My Home Lab Control Center";
    openFirewall = true;
    services = {
      "Portainer" = { port = 9000; url = "https://portainer.mydomain.com"; };
      "Grafana" = { port = 3000; url = "https://grafana.mydomain.com"; };
      "Prometheus" = { port = 9090; url = "https://prometheus.mydomain.com"; };
      "NextCloud" = { port = 8081; url = "https://nextcloud.mydomain.com"; };
      "Home Assistant" = { port = 8123; url = "https://homeassistant.mydomain.com"; };
      "Pi-hole" = { port = 8082; url = "https://pihole.mydomain.com"; };
    };
  };
}
```

## How Monitoring Works

The dashboard uses a simple but effective approach to monitor your services:

### Port-Based Health Checking

- **Local Port Connectivity**: Checks if services are responding on their configured ports
- **Fast and Reliable**: Simple TCP connection test to `localhost:port`
- **No External Dependencies**: Works entirely locally without network requests
- **Immediate Results**: Quick response times for status updates

## API Endpoints

- `GET /` - Main dashboard page
- `GET /api/services` - JSON API for service status
- `GET /health` - Health check endpoint

## NixOS Integration

### Adding to Your NixOS Configuration

1. **Using Flakes** (Recommended):

```nix
# flake.nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    homelab-dashboard.url = "github:yourusername/homelab-dashboard";
  };

  outputs = { self, nixpkgs, homelab-dashboard }: {
    nixosConfigurations.yourhostname = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        homelab-dashboard.nixosModules.default
        {
          services.homelab-dashboard = {
            enable = true;
            port = 8080;
            openFirewall = true;
            # ... your services configuration
          };
        }
      ];
    };
  };
}
```

2. **Traditional Import**:

```nix
# configuration.nix
{ config, pkgs, ... }:
{
  imports = [
    /path/to/homelab-dashboard/nixos-module.nix
  ];

  services.homelab-dashboard = {
    enable = true;
    port = 8080;
    title = "My Home Lab";
    openFirewall = true;
    services = {
      # Your services here
    };
  };
}
```

### NixOS Module Options

| Option             | Type   | Default                     | Description                                       |
| ------------------ | ------ | --------------------------- | ------------------------------------------------- |
| `enable`           | bool   | `false`                     | Enable the homelab dashboard service              |
| `port`             | int    | `8080`                      | Port to listen on                                 |
| `title`            | string | `"Home Lab Control Center"` | Dashboard title                                   |
| `services`         | attrs  | `{}`                        | Services to monitor (name -> {port, url} mapping) |
| `user`             | string | `"homelab-dashboard"`       | Service user                                      |
| `group`            | string | `"homelab-dashboard"`       | Service group                                     |
| `openFirewall`     | bool   | `false`                     | Open firewall for the service port                |
| `extraEnvironment` | attrs  | `{}`                        | Additional environment variables                  |
| `logLevel`         | enum   | `"info"`                    | Log level (debug, info, warn, error)              |

## Security

The NixOS module includes several security hardening features:

- Runs as unprivileged user
- Restricted filesystem access
- Network restrictions
- System call filtering
- No new privileges
- Memory execution protection
