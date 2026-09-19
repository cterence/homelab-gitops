{
  description = "Development environment and pre-commit checks for the workbench apps";

  inputs = {
    nixpkgs.url = "nixpkgs/nixos-unstable";

    pre-commit-hooks = {
      url = "github:cachix/git-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      pre-commit-hooks,
    }:
    let
      goVersion = 26; # Change this to update the whole stack

      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forEachSupportedSystem =
        f:
        nixpkgs.lib.genAttrs supportedSystems (
          system:
          f {
            pkgs = import nixpkgs {
              inherit system;
              overlays = [ self.overlays.default ];
              config.allowUnfree = true;
            };
          }
        );

      goApps = [
        "go-healthcheck"
        "lastfm-scrobble-deduplicator"
        "rangemusique"
      ];

      # Per-app hooks mirroring the pre-commit setups of the original repos
      # (gofmt, golangci-lint, govet). Each app is its own Go module, so the
      # tools run from the app directory. Staticcheck checks are enforced via
      # golangci-lint, which enables the staticcheck linter in .golangci.yaml.
      # Entries are absolute store paths so the hooks also work when invoked
      # from environments without the dev shell on PATH (e.g. VS Code git).
      goAppHooks =
        pkgs: app:
        let
          runInApp = pkgs.writeShellScript "precommit-${app}" ''
            export PATH="${pkgs.go}/bin:${pkgs.golangci-lint}/bin:$PATH"
            cd workbench/${app}
            exec "$@"
          '';
        in
        {
          "${app}-golangci-lint" = {
            enable = true;
            entry = "${runInApp} golangci-lint run";
            files = "^workbench/${app}/";
            pass_filenames = false;
            extraPackages = with pkgs; [
              go
              golangci-lint
            ];
          };
          "${app}-govet" = {
            enable = true;
            entry = "${runInApp} go vet ./...";
            files = "^workbench/${app}/";
            pass_filenames = false;
            extraPackages = [ pkgs.go ];
          };
        };

      hooks =
        pkgs:
        {
          # Secret detection, replacing the previous .pre-commit-config.yaml.
          # Absolute store path so the hook also works outside the dev shell.
          gitleaks = {
            enable = true;
            entry = "${pkgs.gitleaks}/bin/gitleaks protect --staged --redact --verbose";
            pass_filenames = false;
            extraPackages = [ pkgs.gitleaks ];
          };

          gofmt = {
            enable = true;
            files = "^workbench/.*\\.go$";
          };
        }
        // nixpkgs.lib.foldl' (acc: app: acc // goAppHooks pkgs app) { } goApps;

      # The .worktrees directory holds full repo copies that would otherwise
      # be linted and shadow the real sources in the check build.
      src = nixpkgs.lib.cleanSourceWith {
        src = ./.;
        filter =
          path: type:
          !(
            type == "directory"
            && builtins.elem (baseNameOf path) [
              ".git"
              ".worktrees"
            ]
          );
      };
    in
    {
      overlays.default = final: prev: {
        go = final."go_1_${toString goVersion}";
      };

      devShells = forEachSupportedSystem (
        { pkgs }:
        {
          default = pkgs.mkShell {
            inherit (self.checks.${pkgs.stdenv.hostPlatform.system}.pre-commit-check) shellHook;
            packages = with pkgs; [
              gitleaks
              go
              gotools
              golangci-lint
              uv
              vault
              self.checks.${pkgs.stdenv.hostPlatform.system}.pre-commit-check.enabledPackages
            ];
          };
        }
      );

      checks = forEachSupportedSystem (
        { pkgs }:
        {
          pre-commit-check = pre-commit-hooks.lib.${pkgs.stdenv.hostPlatform.system}.run {
            inherit src;
            # prek is the fast Rust reimplementation of pre-commit.
            package = pkgs.prek;
            hooks = hooks pkgs;
          };
        }
      );
    };
}
