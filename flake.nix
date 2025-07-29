{
  description = "Homelab Dashboard - A service status dashboard for home labs";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        homelab-dashboard = pkgs.buildGoModule {
          pname = "homelab-dashboard";
          version = "1.0.0";

          src = ./.;

          vendorHash = null;

          # Include template files in the build
          postInstall = ''
            mkdir -p $out/share/homelab-dashboard/templates
            cp -r templates/* $out/share/homelab-dashboard/templates/
          '';
        };
      in {
        packages = {
          default = homelab-dashboard;
          inherit homelab-dashboard;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gotools
            gopls
            go-outline
            gopkgs
            gocode-gomod
            godef
            golint
          ];
        };

        apps.default = {
          type = "app";
          program = "${homelab-dashboard}/bin/homelab-dashboard";
        };

        checks = { build = homelab-dashboard; };
      }) // {
        nixosModules.default = import ./nixos-module.nix;
        nixosModules.homelab-dashboard = import ./nixos-module.nix;
      };
}
