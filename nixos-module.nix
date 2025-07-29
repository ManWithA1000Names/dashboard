{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.homelab-dashboard;

  configFile = pkgs.writeText "homelab-dashboard-config.json" (builtins.toJSON {
    port = cfg.port;
    title = cfg.title;
    services = cfg.services;
  });

  homelab-dashboard = pkgs.buildGoModule rec {
    pname = "homelab-dashboard";
    version = "1.0.0";

    src = ./.;

    vendorHash = null;

    # Include template files in the build
    postInstall = ''
      mkdir -p $out/share/homelab-dashboard/templates
      cp -r templates/* $out/share/homelab-dashboard/templates/
    '';

    meta = with lib; {
      description = "Home lab service status dashboard";
      homepage = "https://github.com/homelab/dashboard";
      license = licenses.mit;
      maintainers = [ maintainers.homelab ];
    };
  };

in {
  options.services.homelab-dashboard = {
    enable = mkEnableOption "homelab dashboard service";

    port = mkOption {
      type = types.int;
      default = 8080;
      description = "Port on which the dashboard will listen";
    };

    title = mkOption {
      type = types.str;
      default = "Home Lab Control Center";
      description = "Title displayed on the dashboard";
    };

    services = mkOption {
      type = types.attrsOf (types.submodule {
        options = {
          port = mkOption {
            type = types.int;
            description = "Port to check for service availability";
            example = 9000;
          };
          url = mkOption {
            type = types.str;
            description = "Public URL to display in the UI";
            example = "https://portainer.mydomain.com";
          };
        };
      });
      default = {
        "Portainer" = { port = 9000; url = "http://localhost:9000"; };
        "Grafana" = { port = 3000; url = "http://localhost:3000"; };
        "Prometheus" = { port = 9090; url = "http://localhost:9090"; };
        "NextCloud" = { port = 8081; url = "http://localhost:8081"; };
        "Home Assistant" = { port = 8123; url = "http://localhost:8123"; };
        "Pi-hole" = { port = 8082; url = "http://localhost:8082"; };
      };
      description = "Services to monitor with their local ports and public URLs";
      example = {
        "My Service" = { port = 8080; url = "https://myservice.mydomain.com"; };
        "Another Service" = { port = 3000; url = "https://other.mydomain.com"; };
      };
    };

    user = mkOption {
      type = types.str;
      default = "homelab-dashboard";
      description = "User account under which the dashboard runs";
    };

    group = mkOption {
      type = types.str;
      default = "homelab-dashboard";
      description = "Group under which the dashboard runs";
    };

    openFirewall = mkOption {
      type = types.bool;
      default = false;
      description = "Whether to open the firewall for the dashboard port";
    };

    extraEnvironment = mkOption {
      type = types.attrsOf types.str;
      default = {};
      description = "Extra environment variables to pass to the service";
      example = {
        "HOMELAB_CUSTOM_VAR" = "value";
      };
    };

    logLevel = mkOption {
      type = types.enum [ "debug" "info" "warn" "error" ];
      default = "info";
      description = "Log level for the dashboard service";
    };
  };

  config = mkIf cfg.enable {
    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.group;
      description = "Homelab dashboard service user";
      home = "/var/lib/homelab-dashboard";
      createHome = true;
    };

    users.groups.${cfg.group} = {};

    systemd.services.homelab-dashboard = {
      description = "Home Lab Dashboard Service";
      after = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];

      environment = cfg.extraEnvironment // {
        HOMELAB_CONFIG = configFile;
        HOMELAB_PORT = toString cfg.port;
        HOMELAB_TITLE = cfg.title;
        HOMELAB_SERVICES = builtins.toJSON (mapAttrs (name: config: { inherit (config) port url; }) cfg.services);
        LOG_LEVEL = cfg.logLevel;
      };

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = cfg.group;
        ExecStart = "${homelab-dashboard}/bin/homelab-dashboard";
        WorkingDirectory = "${homelab-dashboard}/share/homelab-dashboard";
        Restart = "always";
        RestartSec = "10";

        # Security settings
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ReadWritePaths = [ "/var/lib/homelab-dashboard" ];

        # Network access restrictions
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" ];

        # Process restrictions
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;

        # Capabilities
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";

        # System call filtering
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" "~@debug" "~@mount" "~@reboot" "~@swap" ];
      };
    };

    # Open firewall if requested
    networking.firewall.allowedTCPPorts = mkIf cfg.openFirewall [ cfg.port ];

    # Add the package to system packages
    environment.systemPackages = [ homelab-dashboard ];
  };
}
