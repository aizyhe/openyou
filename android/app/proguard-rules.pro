# openyou — keep app entry points, strip unused AndroidX transitive deps.
# Obfuscation disabled: the app is a thin WebView shell, obfuscation adds no value.
-dontobfuscate

# Keep all application classes
-keep class io.openyou.app.** { *; }

# Keep JNI native methods
-keepclasseswithmembernames class * { native <methods>; }

# NOTE: If -dontobfuscate is removed in the future, add these keep rules to
# prevent JNI bridge classes from being renamed (renaming breaks native calls):
#   -keep class io.openyou.app.DaemonLib { *; }
#   -keep class io.openyou.app.KeystoreManager { *; }

# Keep WebView JavaScript interface methods
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}

# Keep annotations (needed for JNI, JavascriptInterface, etc.)
-keepattributes *Annotation*
-keepattributes SourceFile,LineNumberTable

# Tink (security-crypto dependency) references compile-only errorprone annotations.
# These are never present at runtime; missing them is harmless.
-dontwarn com.google.errorprone.annotations.**
-dontwarn javax.annotation.**
-dontwarn javax.annotation.concurrent.**
