.PHONY: catbox-apk

# catbox-apk: the Android app as result.apk (adb wants the extension,
# hence -o). Hermetic: the catbox binary is a nix cross-build and the
# maven deps are one pinned fixed-output fetch.
catbox-apk:
	nix build -o result.apk .#catbox-android
