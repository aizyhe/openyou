plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "io.openyou.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "io.openyou.app"
        minSdk = 31
        targetSdk = 34
        versionCode = 1
        versionName = "1.0.1"
    }

    buildTypes {
        debug {
            isMinifyEnabled = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
        release {
            isMinifyEnabled = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
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
    // App Lock: fingerprint/face + pattern fallback.
    // biometric pulls in androidx.fragment (LockActivity extends FragmentActivity).
    implementation("androidx.biometric:biometric:1.1.0")
    // EncryptedSharedPreferences for the salted pattern hash and lock config
    // and the Keystore-wrapped data key.
    // Use the stable 1.0.0 release (1.1.0-alpha06 was an unstable alpha).
    implementation("androidx.security:security-crypto:1.0.0")
}
