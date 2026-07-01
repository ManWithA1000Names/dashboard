{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.homelab-dashboard;

  # Read-only config file baked into the Nix store.
  # Only port, title and services (URLs) are needed — port-based health
  # checks were removed, so the service port sub-field is kept solely for
  # JSON round-trip compatibility with hand-written configs.
  configFile = pkgs.writeText "homelab-dashboard-config.json" (builtins.toJSON {
    port     = cfg.port;
    title    = cfg.title;
    services = mapAttrs (_: svc: { inherit (svc) url; }) cfg.services;
  });

  homelab-dashboard = pkgs.buildGoModule {
    pname   = "homelab-dashboard";
    version = "1.0.0";

    src = ./.;

    vendorHash = null;

    # Ship the HTML template alongside the binary so the runtime path-search
    # (filepath.Dir(os.Args[0]) + "/../share/…") resolves correctly.
    postInstall = ''
      mkdir -p $out/share/homelab-dashboard/templates
      cp -r templates/* $out/share/homelab-dashboard/templates/
    '';

    meta = {
      description = "Minimalist homelab shortcut dashboard";
      homepage    = "https://github.com/homelab/dashboard";
      license     = lib.licenses.mit;
    };
  };

in {
  options.services.homelab-dashboard = {

    enable = mkEnableOption "homelab dashboard";

    port = mkOption {
      type    = types.int;
      default = 8080;
      description = "TCP port the dashboard listens on.";
    };

    title = mkOption {
      type    = types.str;
      default = "Dashboard";
      description = "Page title shown in the browser tab and on the dashboard.";
    };

    services = mkOption {
      type = types.attrsOf (types.submodule {
        options = {
          url = mkOption {
            type        = types.str;
            description = "URL this shortcut points to.";
            example     = "https://grafana.mydomain.com";
          };
          # Kept for backward compatibility with existing NixOS configs that
          # set a port value.  The dashboard no longer does port-based health
          # checks so this field is written into config.json but ignored at
          # runtime.
          port = mkOption {
            type    = types.int;
            default = 0;
            description = "(Legacy) Port number — no longer used.";
          };
        };
      });
      default = {
        "Portainer"      = { url = "http://localhost:9000"; };
        "Grafana"        = { url = "http://localhost:3000"; };
        "Prometheus"     = { url = "http://localhost:9090"; };
        "NextCloud"      = { url = "http://localhost:8081"; };
        "Home Assistant" = { url = "http://localhost:8123"; };
        "Pi-hole"        = { url = "http://localhost:8082"; };
      };
      description = ''
        Built-in shortcuts (hardcoded).  These are not editable at runtime;
        use the dashboard UI to add extra shortcuts that survive restarts.
      '';
    };

    user = mkOption {
      type    = types.str;
      default = "homelab-dashboard";
      description = "System user the dashboard process runs as.";
    };

    group = mkOption {
      type    = types.str;
      default = "homelab-dashboard";
      description = "System group the dashboard process runs as.";
    };

    openFirewall = mkOption {
      type    = types.bool;
      default = false;
      description = "Open the firewall for the dashboard port.";
    };

    extraEnvironment = mkOption {
      type    = types.attrsOf types.str;
      default = {};
      description = "Additional environment variables passed to the service.";
      example = { "HOMELAB_CONFIG" = "/etc/homelab/config.json"; };
    };

  };

  config = mkIf cfg.enable {

    users.users.${cfg.user} = {
      isSystemUser = true;
      group        = cfg.group;
      description  = "Homelab dashboard service user";
    };

    users.groups.${cfg.group} = {};

    systemd.services.homelab-dashboard = {
      description = "Homelab Dashboard";
      after       = [ "network.target" ];
      wantedBy    = [ "multi-user.target" ];

      environment = cfg.extraEnvironment // {
        # Point the binary at the Nix-store config.  Port and title are also
        # passed as env overrides so they take effect even if config parsing
        # somehow fails (belt-and-suspenders).
        HOMELAB_CONFIG = configFile;
        HOMELAB_PORT   = toString cfg.port;
        HOMELAB_TITLE  = cfg.title;
      };

      serviceConfig = {
        Type  = "simple";
        User  = cfg.user;
        Group = cfg.group;

        ExecStart = "${homelab-dashboard}/bin/homelab-dashboard";

        # The binary resolves all writable paths relative to CWD:
        #   shortcuts.json   → $STATE_DIRECTORY/shortcuts.json
        #   favicon-cache/   → $STATE_DIRECTORY/favicon-cache/
        #
        # StateDirectory creates /var/lib/homelab-dashboard, sets ownership
        # to cfg.user, and makes it writable even under ProtectSystem=strict.
        StateDirectory     = "homelab-dashboard";
        StateDirectoryMode = "0750";
        WorkingDirectory   = "/var/lib/homelab-dashboard";

        Restart    = "on-failure";
        RestartSec = "10";

        # ── Hardening ───────────────────────────────────────────────────────
        NoNewPrivileges  = true;
        PrivateTmp       = true;
        ProtectSystem    = "strict";   # whole fs read-only except StateDirectory
        ProtectHome      = true;

        # Outbound HTTP is required for favicon resolution.
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" ];

        LockPersonality        = true;
        MemoryDenyWriteExecute = true;
        RestrictRealtime       = true;
        RestrictSUIDSGID       = true;
        CapabilityBoundingSet  = "";
        AmbientCapabilities    = "";
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" "~@debug" "~@mount" "~@reboot" "~@swap" ];
      };
    };

    networking.firewall.allowedTCPPorts = mkIf cfg.openFirewall [ cfg.port ];

    environment.systemPackages = [ homelab-dashboard ];
  };
}
