package io.openyou.app

import android.content.Context
import android.content.SharedPreferences
import android.os.SystemClock
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKeys
import java.security.MessageDigest
import java.security.SecureRandom

/** App-Lock state: salted pattern hash + timeout in EncryptedSharedPreferences (raw pattern
 * never persisted). Bookkeeping is in-memory, so cold start always locks. */
object LockManager {

    private const val PREFS = "openyou_lock_prefs"
    private const val KEY_PATTERN_HASH = "pattern_hash"
    private const val KEY_PATTERN_SALT = "pattern_salt"
    private const val KEY_TIMEOUT_MIN = "timeout_minutes"

    val TIMEOUT_OPTIONS = intArrayOf(1, 5, 15)
    private const val DEFAULT_TIMEOUT_MIN = 5

    /** elapsedRealtime() of the last successful unlock; 0 == never (cold start). */
    @Volatile private var lastUnlockElapsed: Long = 0L

    /** Set true on manual lock / screen-off to force the next onResume to lock. */
    @Volatile private var forceLock: Boolean = false

    /** True when EncryptedSharedPreferences is unavailable; the plaintext fallback must not get sensitive keys. */
    @Volatile private var useFallback: Boolean = false

    private const val KEY_FAILED_ATTEMPTS = "failed_attempts"
    private const val KEY_LOCKOUT_UNTIL = "lockout_until"   // wall-clock ms (System.currentTimeMillis)

    /** Consecutive failed pattern attempts; persisted so process restart can't reset the counter. */
    private fun getFailedAttempts(ctx: Context): Int = prefs(ctx).getInt(KEY_FAILED_ATTEMPTS, 0)
    private fun setFailedAttempts(ctx: Context, v: Int) { prefs(ctx).edit().putInt(KEY_FAILED_ATTEMPTS, v).apply() }

    /** Wall-clock ms until pattern entry is locked out; survives device reboot. */
    private fun getLockoutUntilMs(ctx: Context): Long = prefs(ctx).getLong(KEY_LOCKOUT_UNTIL, 0L)
    private fun setLockoutUntilMs(ctx: Context, v: Long) { prefs(ctx).edit().putLong(KEY_LOCKOUT_UNTIL, v).apply() }

    private const val MAX_ATTEMPTS = 5
    private const val LOCKOUT_MS = 30_000L

    private fun prefs(ctx: Context): SharedPreferences {
        return try {
            val p = openEncryptedPrefs(ctx)
            useFallback = false
            p
        } catch (e: Exception) {
            // Corrupt prefs would crash every resume; reset once (hash lost → first-run setup, not a lock-out).
            android.util.Log.w("LockManager", "encrypted prefs corrupt, resetting: ${e.message}")
            deletePrefsFile(ctx)
            try {
                val p = openEncryptedPrefs(ctx)
                useFallback = false
                p
            } catch (e2: Exception) {
                // Keystore-level fault — a rebuild can't fix it; plaintext fallback keeps the app usable.
                android.util.Log.w("LockManager", "keystore-level prefs failure, using plaintext fallback: ${e2.message}")
                useFallback = true
                ctx.getSharedPreferences(PREFS + "_fallback", Context.MODE_PRIVATE)
            }
        }
    }

    private fun openEncryptedPrefs(ctx: Context): SharedPreferences {
        val masterKeyAlias = MasterKeys.getOrCreate(MasterKeys.AES256_GCM_SPEC)
        return EncryptedSharedPreferences.create(
            PREFS,
            masterKeyAlias,
            ctx,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    private fun deletePrefsFile(ctx: Context) {
        try {
            // deleteSharedPreferences needs API 24+; minSdk is 31 so it's always present.
            ctx.deleteSharedPreferences(PREFS)
        } catch (_: Exception) {
        }
    }

    /** Records a successful unlock; clears any pending force-lock. */
    fun markUnlocked(ctx: Context) {
        lastUnlockElapsed = SystemClock.elapsedRealtime()
        forceLock = false
        setFailedAttempts(ctx, 0)
        setLockoutUntilMs(ctx, 0L)
    }

    /** Ms remaining in lockout (0 = none); wall-clock so it survives reboot. */
    fun lockoutRemainingMs(ctx: Context): Long {
        val lockoutUntilMs = getLockoutUntilMs(ctx)
        if (lockoutUntilMs == 0L) return 0L
        val remaining = lockoutUntilMs - System.currentTimeMillis()
        return if (remaining > 0L) remaining else 0L
    }

    /** Bumps the counter; at MAX_ATTEMPTS starts the lockout window (resetting it) and returns its ms. */
    fun recordFailedAttempt(ctx: Context): Long {
        val attempts = getFailedAttempts(ctx) + 1
        if (attempts >= MAX_ATTEMPTS) {
            setFailedAttempts(ctx, 0)
            // Wall-clock so the lockout survives reboot.
            setLockoutUntilMs(ctx, System.currentTimeMillis() + LOCKOUT_MS)
            return LOCKOUT_MS
        }
        setFailedAttempts(ctx, attempts)
        return 0L
    }

    /** Forces the lock on the next check (manual lock button / ACTION_SCREEN_OFF). */
    fun lockNow() {
        forceLock = true
    }

    /** True when the lock must show. Fast path uses only in-memory state to avoid the
     * Keystore init side-effect that re-showed the lock after fingerprint unlock. */
    fun needsUnlock(ctx: Context): Boolean {
        if (forceLock) return true
        if (lastUnlockElapsed == 0L) return true
        // Shortest-timeout fast path avoids the Keystore init side-effect behind the post-fingerprint bug.
        val elapsed = SystemClock.elapsedRealtime() - lastUnlockElapsed
        val minTimeout = TIMEOUT_OPTIONS.min() * 60_000L
        if (elapsed < minTimeout) return false
        return elapsed >= timeoutMinutes(ctx) * 60_000L
    }

    fun timeoutMinutes(ctx: Context): Int =
        prefs(ctx).getInt(KEY_TIMEOUT_MIN, DEFAULT_TIMEOUT_MIN)

    fun setTimeoutMinutes(ctx: Context, minutes: Int) {
        val v = if (minutes in TIMEOUT_OPTIONS) minutes else DEFAULT_TIMEOUT_MIN
        prefs(ctx).edit().putInt(KEY_TIMEOUT_MIN, v).apply()
    }

    fun hasPattern(ctx: Context): Boolean {
        // Fallback plaintext prefs → treat as no pattern so setup prompts again.
        if (useFallback) return false
        return prefs(ctx).getString(KEY_PATTERN_HASH, null) != null
    }

    /** Stores a fresh salt + SHA-256(salt || dots) so the dot sequence never hits disk. */
    fun setPattern(ctx: Context, dots: List<Int>) {
        // Never persist the hash in plaintext fallback storage.
        if (useFallback) {
            android.util.Log.w("LockManager", "pattern not saved: keystore unavailable")
            return
        }
        val salt = ByteArray(16).also { SecureRandom().nextBytes(it) }
        val hash = hashPattern(salt, dots)
        prefs(ctx).edit()
            .putString(KEY_PATTERN_SALT, salt.toHex())
            .putString(KEY_PATTERN_HASH, hash.toHex())
            .apply()
    }

    fun verifyPattern(ctx: Context, dots: List<Int>): Boolean {
        val p = prefs(ctx)
        val saltHex = p.getString(KEY_PATTERN_SALT, null) ?: return false
        val expected = p.getString(KEY_PATTERN_HASH, null) ?: return false
        val actual = hashPattern(saltHex.fromHex(), dots).toHex()
        return constantTimeEquals(actual, expected)
    }

    private fun hashPattern(salt: ByteArray, dots: List<Int>): ByteArray {
        val patternBytes = ByteArray(dots.size) { dots[it].toByte() }
        val md = MessageDigest.getInstance("SHA-256")
        md.update(salt)
        md.update(patternBytes)
        return md.digest()
    }

    /** Length-constant string compare to avoid leaking the hash via timing. */
    private fun constantTimeEquals(a: String, b: String): Boolean {
        if (a.length != b.length) return false
        var diff = 0
        for (i in a.indices) diff = diff or (a[i].code xor b[i].code)
        return diff == 0
    }

    private fun ByteArray.toHex(): String =
        joinToString("") { "%02x".format(it) }

    private fun String.fromHex(): ByteArray =
        ByteArray(length / 2) { substring(it * 2, it * 2 + 2).toInt(16).toByte() }
}
