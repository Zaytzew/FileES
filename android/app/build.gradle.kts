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
        versionCode = 8
        versionName = "0.1.7-browse"
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

    // QR scanning for mobile pairing (concept doc §4.2). Deliberately ZXing,
    // not ML Kit: no Google Play Services dependency, works on any device
    // regardless of GMS availability -- a firm project preference, even at
    // the cost of a less polished scanner UX than ML Kit would give.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
}
