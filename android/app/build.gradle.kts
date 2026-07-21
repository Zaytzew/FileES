plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "net.filees.mobile"
    compileSdk = 36

    defaultConfig {
        applicationId = "net.filees.mobile"
        minSdk = 24
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0-etap6-skeleton"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // pkg/mobileclient/androidbind built via gomobile bind -- see
    // android/README.md for how to (re)generate this file. Not vendored in
    // SVN: it's a build artifact of committed Go source, not source itself.
    implementation(files("libs/filees-androidbind.aar"))
}
