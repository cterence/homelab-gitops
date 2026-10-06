{
  description = "Development environment and pre-commit checks for the workbench apps";

  inputs = {
    nixpkgs.url = "nixpkgs/nixos-unstable";

    # The Android SDK as a flake. Only the catbox-android package and
    # the android devShell depend on it.
    android-nixpkgs = {
      url = "github:tadfisher/android-nixpkgs";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    pre-commit-hooks = {
      url = "github:cachix/git-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      android-nixpkgs,
      pre-commit-hooks,
    }:
    let
      goVersion = 27;

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
        "catbox"
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
        // (
          # The Android app shell: nix develop .#android. The SDK
          # matches android/app/build.gradle.kts (compileSdk 34,
          # build-tools 34.0.0, AGP 8.5.2, JDK 17) — bump them
          # together. Hosts without an android-nixpkgs SDK composition
          # (aarch64-linux) don't get the shell.
          let
            system = pkgs.stdenv.hostPlatform.system;
            androidSystems = [
              "x86_64-linux"
              "aarch64-darwin"
            ];
          in
          nixpkgs.lib.optionalAttrs (builtins.elem system androidSystems) {
            android =
              let
                sdk = android-nixpkgs.sdk.${system} (
                  sdkPkgs: with sdkPkgs; [
                    cmdline-tools-latest
                    platform-tools
                    build-tools-34-0-0
                    platforms-android-34
                  ]
                );
              in
              pkgs.mkShell rec {
                packages = [
                  sdk
                  pkgs.jdk17
                  pkgs.gradle
                  pkgs.go
                ];
                ANDROID_HOME = "${sdk}/share/android-sdk";
                ANDROID_SDK_ROOT = "${sdk}/share/android-sdk";
                # AGP downloads a prebuilt aapt2 from Maven that does not
                # run on Nix (unpatched ELF); point it at the SDK's own.
                GRADLE_OPTS = "-Dorg.gradle.project.android.aapt2FromMavenOverride=${ANDROID_HOME}/build-tools/34.0.0/aapt2";
                JAVA_HOME = pkgs.jdk17.home;
              };
          }
        )
      );

      packages = forEachSupportedSystem (
        { pkgs }:
        {
          # The peer CLI for laptops: nix build github.com/cterence/homelab-gitops#catbox
          catbox = pkgs.buildGo127Module {
            pname = "catbox";
            version = "0.1.0";
            src = ./workbench/catbox;
            vendorHash = "sha256-B0NZyZgmJqKRNZ+9iHPPM1LBdx5UpVxkdEkOIlllXTc=";
          };
        }
        // (
          # The Android APK, hermetically built from
          # workbench/catbox/nix/android.nix. Only hosts with an
          # android-nixpkgs SDK composition can build it.
          let
            system = pkgs.stdenv.hostPlatform.system;
            androidSystems = [
              "x86_64-linux"
              "aarch64-darwin"
            ];
          in
          nixpkgs.lib.optionalAttrs (builtins.elem system androidSystems) (
            let
              catboxAndroid = import ./workbench/catbox/nix/android.nix {
                inherit pkgs;
                android-sdk = android-nixpkgs.sdk.${system};
                src = self.outPath + "/workbench/catbox";
                vendorHash = "sha256-B0NZyZgmJqKRNZ+9iHPPM1LBdx5UpVxkdEkOIlllXTc=";
              };
            in
            {
              catbox-android = catboxAndroid.catbox-android;
              catbox-android-bin = catboxAndroid.catbox-android-bin;
            }
          )
        )
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
