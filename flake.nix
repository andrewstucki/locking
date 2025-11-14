{
  inputs = {
    nixpkgs.url = "nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    devshell = {
      url = "github:numtide/devshell";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{ self
    , devshell
    , flake-parts
    , nixpkgs
    }: flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" ];

      imports = [
        devshell.flakeModule
      ];

      perSystem = { self', system, ... }:
        let
          lib = pkgs.lib;
          pkgs = import nixpkgs {
            inherit system;
            overlays = [
              # Load in various overrides for custom packages and version pinning.
              (import ./support/overlay.nix { pkgs = pkgs; })
            ];
          };
        in
        {
          formatter = pkgs.nixpkgs-fmt;

          packages.devshell = self'.devShells.default;

          devshells.default = {
            env = [
              { name = "GOROOT"; value = "${pkgs.go_1_24}/share/go"; }
              { name = "KUBEBUILDER_ASSETS"; eval = "$(setup-envtest use -p path 1.32.x)"; }
              { name = "PATH"; eval = "$(pwd)/.build:$PATH"; }
            ];

            packages = [
              pkgs.go-task
              pkgs.go_1_24
              pkgs.k3d # Kind alternative that allows adding/removing Nodes.
              pkgs.kubectl
              pkgs.setup-envtest # Kubernetes provided test utilities
              pkgs.vcluster
              pkgs.linkerd
            ] ++ lib.optionals pkgs.stdenv.isLinux [
              pkgs.sysctl # Used to adjust ulimits on linux systems (Namely, CI).
            ];
          };
        };
    };
}
