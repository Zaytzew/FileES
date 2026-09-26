import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val releaseKeyDir = listOfNotNull(
    System.getenv("FILEES_ANDROID_HOME")?.let { file(it) },
    file("/home/acme/.filees"),
    file("${System.getProperty("user.home")}/.filees"),
).firstOrNull { file("$it/android-release.p12").isFile } ?: file("${System.getProperty("user.home")}/.filees")
val releaseKeystore = file("$releaseKeyDir/android-release.p12")
val releaseKeyProps = file("$releaseKeyDir/android-release.properties")
val buildingRelease = gradle.startParameter.taskNames.any { it.contains("Release", ignoreCase = true) }
if (buildingRelease && (!releaseKeystore.isFile || !releaseKeyProps.isFile)) {
    error("Release APK requires ${releaseKeystore}. Debug builds do not use this key.")
}

android {
    namespace = "net.filees.mobile"
    compileSdk = 36

    defaultConfig {
        applicationId = "net.filees.mobile"
        minSdk = 24
        targetSdk = 36
        versionCode = 54
        versionName = "0.1.17+r1643"
    }

    signingConfigs {
        create("release") {
            if (releaseKeystore.isFile && releaseKeyProps.isFile) {
                val props = Properties().apply { releaseKeyProps.inputStream().use { load(it) } }
                storeFile = releaseKeystore
                storePassword = props.getProperty("storePassword")
                keyAlias = props.getProperty("keyAlias")
                keyPassword = props.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("release")
        }
    }

    // lintVitalAnalyzeRelease crashes on this build host (opaque "25.0.4.1"
    // worker failure, no lint finding attached) - AGP 8.7.2 was only tested
    // up to compileSdk 35 (see the build's own warning) and this machine's
    // lint SDK component may have moved past what it expects. assembleRelease
    // otherwise builds and installs fine; run `./gradlew lint` by hand if you
    // want the report. 2026-09-26.
    lint {
        checkReleaseBuilds = false
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        viewBinding = true
    }
}

dependencies {
    // pkg/mobileclient/androidbind built via gomobile bind -- see
    // android/README.md for how to (re)generate this file. Not vendored in
    // SVN: it's a build artifact of committed Go source, not source itself.
    implementation(files("libs/filees-androidbind.aar"))

    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.constraintlayout:constraintlayout:2.2.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.recyclerview:recyclerview:1.3.2")
    implementation("androidx.work:work-runtime-ktx:2.9.1")
    implementation("androidx.biometric:biometric:1.1.0")
    implementation("androidx.lifecycle:lifecycle-process:2.8.7")

    // QR scanning for mobile pairing (concept doc §4.2). Deliberately ZXing,
    // not ML Kit: no Google Play Services dependency, works on any device
    // regardless of GMS availability -- a firm project preference, even at
    // the cost of a less polished scanner UX than ML Kit would give.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
}
