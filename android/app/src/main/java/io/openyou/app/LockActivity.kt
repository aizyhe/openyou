package io.openyou.app

import android.graphics.Color
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity

/** Full-screen gate over the WebView until the user authenticates; only the UI is gated, never the daemon. */
class LockActivity : FragmentActivity() {

    private lateinit var root: LinearLayout
    private lateinit var titleView: TextView
    private lateinit var statusView: TextView
    private lateinit var patternView: PatternLockView
    private lateinit var patternContainer: LinearLayout
    private lateinit var biometricButton: Button

    private var settingUp = false
    private var firstEntry: List<Int>? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)

        settingUp = !LockManager.hasPattern(this)
        buildUi()

        if (!settingUp && canUseBiometric()) {
            showBiometricPrompt()
        }
    }

    /** Block back-press: the lock cannot be dismissed without authenticating. */
    override fun onBackPressed() { /* swallow */ }

    private fun buildUi() {
        val dp = resources.displayMetrics.density
        fun px(v: Int) = (v * dp).toInt()

        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setBackgroundColor(Color.parseColor("#0d1117"))
            layoutParams = ViewGroup.LayoutParams(-1, -1)
            setPadding(px(24), px(64), px(24), px(48))
        }

        titleView = TextView(this).apply {
            text = if (settingUp) "设置解锁图案" else "已锁定"
            setTextColor(Color.WHITE)
            textSize = 22f
            gravity = Gravity.CENTER
        }
        root.addView(titleView, lp(px(8)))

        statusView = TextView(this).apply {
            text = if (settingUp) "绘制一个图案作为解锁凭证" else "使用指纹或图案解锁"
            setTextColor(Color.parseColor("#9AA0A6"))
            textSize = 14f
            gravity = Gravity.CENTER
        }
        root.addView(statusView, lp(px(24)))

        // Square pattern grid: 16dp padding on each side for easy finger reach.
        val dm = resources.displayMetrics
        val size = minOf(dm.widthPixels, dm.heightPixels) - px(32)
        patternView = PatternLockView(this).apply {
            onPatternComplete = { dots -> onPattern(dots) }
        }
        patternContainer = LinearLayout(this).apply {
            addView(patternView, LinearLayout.LayoutParams(size, size))
        }
        root.addView(patternContainer, LinearLayout.LayoutParams(size, size).also {
            it.bottomMargin = px(24)
        })

        biometricButton = Button(this).apply {
            text = "使用指纹/面容"
            visibility = if (!settingUp && canUseBiometric()) View.VISIBLE else View.GONE
            setOnClickListener { showBiometricPrompt() }
        }
        root.addView(biometricButton, lp(0))

        setContentView(root)
    }

    private fun lp(bottom: Int) = LinearLayout.LayoutParams(-2, -2).also { it.bottomMargin = bottom }

    private fun onPattern(dots: List<Int>) {
        if (dots.size < 4) {
            status("至少连接 4 个点")
            patternView.reset()
            return
        }
        if (settingUp) {
            val first = firstEntry
            if (first == null) {
                firstEntry = dots
                status("再次绘制以确认")
                patternView.reset()
            } else if (first == dots) {
                LockManager.setPattern(this, dots)
                unlock()
            } else {
                firstEntry = null
                status("两次不一致，请重新设置")
                patternView.reset()
            }
        } else {
            val lockout = LockManager.lockoutRemainingMs(this)
            if (lockout > 0L) {
                status("尝试过多，请 ${(lockout / 1000) + 1} 秒后再试")
                patternView.reset()
                return
            }
            if (LockManager.verifyPattern(this, dots)) {
                unlock()
            } else {
                val penalty = LockManager.recordFailedAttempt(this)
                if (penalty > 0L) {
                    status("尝试过多，请 ${penalty / 1000} 秒后再试")
                } else {
                    status("图案错误，请重试")
                }
                patternView.reset()
            }
        }
    }

    private fun status(msg: String) {
        statusView.text = msg
    }

    private fun canUseBiometric(): Boolean {
        val bm = BiometricManager.from(this)
        // STRONG only — the lock guards encrypted data; reject WEAK (e.g. 2D face).
        return bm.canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG) ==
            BiometricManager.BIOMETRIC_SUCCESS
    }

    private fun showBiometricPrompt() {
        val executor = ContextCompat.getMainExecutor(this)
        val prompt = BiometricPrompt(this, executor, object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                unlock()
            }
            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                // User cancelled or fell back — leave the pattern grid available.
                status("请使用图案解锁")
            }
        })
        val info = BiometricPrompt.PromptInfo.Builder()
            .setTitle("解锁 openyou")
            .setSubtitle("使用指纹或面容验证")
            .setNegativeButtonText("使用图案")
            .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
            .build()
        prompt.authenticate(info)
    }

    private fun unlock() {
        LockManager.markUnlocked(this)
        setResult(RESULT_OK)
        finish()
    }
}
