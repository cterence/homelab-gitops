.PHONY: catbox-apk catbox-emu catbox-emu-stop catbox-emu-install

CATBOX_ANDROID := workbench/catbox/android

# catbox-apk: the Android app as result.apk (adb wants the extension,
# hence -o). Hermetic: the catbox binary is a nix cross-build and the
# maven deps are one pinned fixed-output fetch.
catbox-apk:
	nix build -o result.apk .#catbox-android

# catbox-emu: launch the catbox AVD (created on first run) from the
# android devshell. Ctrl-C stops the emulator.
catbox-emu:
	nix develop .#android -c bash -c 'avdmanager list avd | grep -q "Name: catbox" || echo no | avdmanager create avd -n catbox -k "system-images;android-36;default;arm64-v8a" -d pixel_7; exec $$ANDROID_HOME/emulator/emulator -avd catbox -gpu host'

# catbox-emu-stop: shut the emulator down cleanly. A no-op (still
# exit 0) when it is not running.
catbox-emu-stop:
	nix develop .#android -c bash -c 'adb emu kill || true'

# catbox-emu-install: build the debug APK (with the cross-compiled
# binary) and install + launch it on the emulator. Starts with the
# emulator already running via catbox-emu: it waits for the device.
catbox-emu-install:
	cd $(CATBOX_ANDROID) && ./build-native.sh
	nix develop .#android -c bash -c 'cd $(CATBOX_ANDROID) && gradle -q :app:assembleDebug && adb wait-for-device && adb wait-for-device shell "while [ -z \$$(getprop sys.boot_completed) ]; do sleep 1; done" && adb install -r app/build/outputs/apk/debug/app-debug.apk && adb shell am start -n cloud.terence.catbox/.MainActivity'
