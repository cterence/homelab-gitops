// The nix APK build replays the deps FOD's maven repo (first, so
// --offline never needs the network); unset in every other flow.
pluginManagement {
    repositories {
        System.getenv("CATBOX_MAVEN_REPO")?.let { maven(it) }
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        System.getenv("CATBOX_MAVEN_REPO")?.let { maven(it) }
        google()
        mavenCentral()
    }
}

rootProject.name = "catbox"
include(":app")
