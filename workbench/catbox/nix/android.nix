# The Android app, built hermetically: nix build .#catbox-android.
#
# The catbox binary is a nix cross-build (buildGoModule, GOOS=android),
# the maven artifacts are one fixed-output fetch laid out as a maven
# repo, and the APK itself builds offline from it. Only hosts with an
# android-nixpkgs SDK composition can build it (x86_64-linux,
# aarch64-darwin).
#
# The gradle build signs with the repo's pinned debug keystore
# (android/keystore-debug.keystore), so every build — local, nix, CI
# — produces the same signature and installs over the previous one.
{
  pkgs,
  android-sdk,
  src,
  vendorHash,
}:

let
  sdk = android-sdk (
    sdkPkgs: with sdkPkgs; [
      cmdline-tools-latest
      platform-tools
      build-tools-34-0-0
      platforms-android-34
    ]
  );

  # The catbox binary as the app ships it: arm64, packaged as a ".so"
  # so the package manager extracts it to the executable
  # nativeLibraryDir.
  catbox-android-bin = (pkgs.buildGo127Module) {
    pname = "catbox-android-bin";
    version = "0.1.0";
    inherit src vendorHash;
    doCheck = false;
    env.CGO_ENABLED = "0";
    # module.nix pins GOOS/GOARCH to the host platform (darwin here);
    # exporting in preBuild — after the env is set, before go build —
    # is what actually crosses to android/arm64.
    preBuild = ''
      export GOOS=android GOARCH=arm64
    '';
    postBuild = ''
      # go puts cross binaries in $GOPATH/bin/android_arm64; module.nix
      # normalizes that only for a cross stdenv, and this one is native.
      mv "$GOPATH/bin/android_arm64"/* "$GOPATH/bin/"
    '';
    postInstall = ''
      install -D $out/bin/catbox $out/lib/arm64-v8a/libcatbox.so
      rm $out/bin/catbox
    '';
  };

  gradleEnv = {
    ANDROID_HOME = "${sdk}/share/android-sdk";
    ANDROID_SDK_ROOT = "${sdk}/share/android-sdk";
    JAVA_HOME = pkgs.jdk17.home;
    # AGP downloads a prebuilt aapt2 from Maven that does not run on
    # Nix (unpatched ELF); point it at the SDK's own. Kotlin compiles
    # in-process: no daemon spawns under nix.
    GRADLE_OPTS = "-Dorg.gradle.project.android.aapt2FromMavenOverride=${sdk}/share/android-sdk/build-tools/34.0.0/aapt2 -Dkotlin.compiler.execution.strategy=in-process";
    # The nix daemon may export a stale or missing CA path; gradle's
    # HTTPS fetches deserve a real one.
    NIX_SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
  };

  # One networked build that fetches every maven artifact; the APK
  # derivation replays them offline from a local maven repo. The
  # artifacts (files-2.1) are byte-identical across builds and
  # platforms, but gradle's own cache state embeds PIDs and download
  # order, so the cache as a whole hashes differently on every build.
  # The derivation name must stay constant: nix keys a fixed-output
  # path by name+hash, so a version-stamped name forces a refetch per
  # commit.
  gradleDeps = pkgs.stdenv.mkDerivation {
    name = "catbox-gradle-deps";
    src = src + "/android";
    nativeBuildInputs = [
      pkgs.jdk17
      pkgs.gradle
    ];
    inherit (gradleEnv)
      ANDROID_HOME
      ANDROID_SDK_ROOT
      JAVA_HOME
      GRADLE_OPTS
      NIX_SSL_CERT_FILE
      ;
    buildPhase = ''
      runHook preBuild
      # Deterministic writable homes: AGP insists on creating
      # ~/.android, and the nix build HOME is not writable.
      export HOME=$NIX_BUILD_TOP/home
      export ANDROID_USER_HOME=$HOME/.android
      export GRADLE_USER_HOME=$NIX_BUILD_TOP/gradle-home
      mkdir -p "$HOME" "$ANDROID_USER_HOME" "$GRADLE_USER_HOME"
      # `command gradle` bypasses nixpkgs' gradle wrapper function,
      # which forces --offline (its MITM-cache design): this is the
      # one build that needs the network.
      command gradle --no-daemon --console=plain assembleDebug
      runHook postBuild
    '';
    installPhase = ''
      # files-2.1 is <group>/<artifact>/<version>/<sha1>/<file>. The
      # sha1 shard is dropped and the file renamed to the canonical
      # maven name.
      mkdir -p $out/repo
      cd "$GRADLE_USER_HOME/caches/modules-2/files-2.1"
      find . -mindepth 5 -maxdepth 5 -type f \
        | awk -F/ -v out="$out/repo" '{
            g = $2; gsub(/\./, "/", g);
            art = $3; ver = $4; name = $6;
            base = art "-" ver;
            ext = name; sub(/^.*\./, "", ext);
            dest = (index(name, base "-") == 1 || index(name, base ".") == 1) \
              ? name : base "." ext;
            dir = out "/" g "/" art "/" ver;
            print "mkdir -p " dir;
            print "cp \"" $0 "\" " dir "/" dest;
          }' | sh -e
    '';
    outputHashAlgo = "sha256";
    outputHashMode = "recursive";
    outputHash = "sha256-vFu6PEVWcab8Gj4SC5JPWYGbPk9a99wYr3qXgNZJEOE=";
  };
in
{
  # The binary cross-build, for build-native.sh's devshell loop.
  inherit catbox-android-bin;

  # nix build .#catbox-android → the debug APK itself as the output
  # (a single-file output): result IS the apk.
  catbox-android = pkgs.stdenv.mkDerivation {
    name = "catbox-android-0.1.0.apk";
    src = src + "/android";
    nativeBuildInputs = [
      pkgs.jdk17
      pkgs.gradle
    ];
    inherit (gradleEnv)
      ANDROID_HOME
      ANDROID_SDK_ROOT
      JAVA_HOME
      GRADLE_OPTS
      NIX_SSL_CERT_FILE
      ;
    buildPhase = ''
      runHook preBuild
      export HOME=$NIX_BUILD_TOP/home
      export ANDROID_USER_HOME=$HOME/.android
      export GRADLE_USER_HOME=$NIX_BUILD_TOP/gradle-home
      mkdir -p "$HOME" "$ANDROID_USER_HOME" "$GRADLE_USER_HOME"
      # settings.gradle.kts puts the deps FOD's maven repo first when
      # this is set; --offline does the rest.
      export CATBOX_MAVEN_REPO="${gradleDeps}/repo"
      # The catbox binary is a nix output, not a checked-in binary.
      install -D "${catbox-android-bin}/lib/arm64-v8a/libcatbox.so" \
        app/src/main/jniLibs/arm64-v8a/libcatbox.so
      gradle --offline --no-daemon --console=plain assembleDebug
      runHook postBuild
    '';
    installPhase = ''
      cp app/build/outputs/apk/debug/app-debug.apk $out
    '';
  };
}
