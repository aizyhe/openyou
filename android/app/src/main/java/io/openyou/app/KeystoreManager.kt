package io.openyou.app

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKeys
import java.io.File
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Protects the daemon's data key with a hardware-backed Keystore master key; Go has no JNI
 * key-injection hook, so the bridge unwraps to master.key before the daemon starts (future: JNI). */
object KeystoreManager {

    private const val ANDROID_KEYSTORE = "AndroidKeyStore"
    private const val MASTER_KEY_ALIAS = "openyou_master_key"
    private const val PREFS = "openyou_keystore_prefs"
    private const val KEY_WRAPPED_DATA_KEY = "wrapped_data_key" // Base64 nonce||ciphertext
    private const val DATA_KEY_SIZE = 32                        // AES-256 for the daemon
    private const val GCM_IV_LEN = 12
    private const val GCM_TAG_BITS = 128

    /** Writes the unwrapped key to the exact path the daemon reads — MUST run before it
     * starts; on failure the daemon falls back to its own key generation. */
    fun provisionMasterKeyFile(ctx: Context) {
        var dataKey: ByteArray? = null
        try {
            dataKey = getOrCreateDataKey(ctx)
            val openyouDir = File(ctx.filesDir, "openyou").apply { mkdirs() }
            val keyFile = File(openyouDir, "master.key")
            // Only rewrite when content differs, to avoid churn on every launch.
            val current = if (keyFile.exists()) keyFile.readBytes() else ByteArray(0)
            if (!current.contentEquals(dataKey)) {
                keyFile.writeBytes(dataKey)
                keyFile.setReadable(false, false)
                keyFile.setReadable(true, true)   // owner-only
                keyFile.setWritable(false, false)
                keyFile.setWritable(true, true)
            }
            current.fill(0)
        } catch (e: Exception) {
            // Non-fatal: daemon falls back to its own file-based key generation.
            android.util.Log.w("KeystoreManager", "provisionMasterKeyFile failed: ${e.message}")
        } finally {
            // Wipe the plaintext data key from the heap once it's on disk.
            dataKey?.fill(0)
        }
    }

    /** Returns the plaintext 32-byte data key, generating+wrapping it on first call. */
    fun getOrCreateDataKey(ctx: Context): ByteArray {
        val masterKey = getOrCreateKeystoreKey()
        val prefs = prefs(ctx)
        val stored = prefs.getString(KEY_WRAPPED_DATA_KEY, null)
        if (stored != null) {
            return unwrap(masterKey, Base64.decode(stored, Base64.NO_WRAP))
        }
        // Migration safety: adopt a pre-Keystore master.key so existing encrypted data stays decryptable.
        var dataKey = adoptExistingKey(ctx)
        if (dataKey == null) {
            dataKey = ByteArray(DATA_KEY_SIZE).also { SecureRandom().nextBytes(it) }
        }
        val wrapped = wrap(masterKey, dataKey)
        prefs.edit().putString(KEY_WRAPPED_DATA_KEY, Base64.encodeToString(wrapped, Base64.NO_WRAP)).apply()
        return dataKey
    }

    // Upgrade safety: reuse the daemon's pre-existing master.key.
    private fun adoptExistingKey(ctx: Context): ByteArray? {
        val f = File(File(ctx.filesDir, "openyou"), "master.key")
        if (f.exists()) {
            val b = f.readBytes()
            if (b.size == DATA_KEY_SIZE) return b
        }
        return null
    }

    private fun getOrCreateKeystoreKey(): SecretKey {
        val ks = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (ks.getKey(MASTER_KEY_ALIAS, null) as? SecretKey)?.let { return it }

        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        val spec = KeyGenParameterSpec.Builder(
            MASTER_KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            // Background workers need the key without a user challenge.
            .setUserAuthenticationRequired(false)
            .build()
        gen.init(spec)
        return gen.generateKey()
    }

    /** Returns nonce(12) || ciphertext||tag. */
    private fun wrap(key: SecretKey, plain: ByteArray): ByteArray {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key)
        val iv = cipher.iv // Keystore GCM generates a random 12-byte IV
        val ct = cipher.doFinal(plain)
        return iv + ct
    }

    private fun unwrap(key: SecretKey, wrapped: ByteArray): ByteArray {
        val iv = wrapped.copyOfRange(0, GCM_IV_LEN)
        val ct = wrapped.copyOfRange(GCM_IV_LEN, wrapped.size)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(GCM_TAG_BITS, iv))
        return cipher.doFinal(ct)
    }

    private fun prefs(ctx: Context): android.content.SharedPreferences {
        return try {
            val masterKeyAlias = MasterKeys.getOrCreate(MasterKeys.AES256_GCM_SPEC)
            EncryptedSharedPreferences.create(
                PREFS,
                masterKeyAlias,
                ctx,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
            )
        } catch (e: Exception) {
            android.util.Log.e(TAG, "EncryptedSharedPreferences unavailable, using plain fallback", e)
            ctx.getSharedPreferences("openyou_ks_fallback", Context.MODE_PRIVATE)
        }
    }

    private const val TAG = "KeystoreManager"
}
