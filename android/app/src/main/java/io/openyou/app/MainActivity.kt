package io.openyou.app

import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.WindowManager
import android.webkit.JavascriptInterface
import android.widget.FrameLayout
import android.webkit.JsResult
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.core.content.ContextCompat

class MainActivity : Activity() {
    private lateinit var webView: WebView

    private var loadRetries = 0

    // Held because WebView returns the picked Uri asynchronously.
    private var fileChooserCallback: android.webkit.ValueCallback<Array<android.net.Uri>>? = null

    // Import streams the picked archive through a cache file so it never
    // accumulates as one giant string in JS or Kotlin memory.
    private var importTempFile: java.io.File? = null
    private var importTempOut: java.io.OutputStream? = null

    private fun discardImportTemp() {
        try { importTempOut?.close() } catch (_: Exception) {}
        importTempOut = null
        try { importTempFile?.delete() } catch (_: Exception) {}
        importTempFile = null
    }

    // True while LockActivity is up so onResume doesn't re-launch the lock screen.
    @Volatile private var lockScreenShowing = false

    // Screen-off forces a re-lock on next resume.
    private val screenOffReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (intent?.action == Intent.ACTION_SCREEN_OFF) LockManager.lockNow()
        }
    }

    companion object {
        init { System.loadLibrary("agent") }

        // JNI: start the Go daemon (blocks on http.Serve; run on a thread).
        @JvmStatic external fun startDaemon()

        private const val REQ_UNLOCK = 4001
        private const val REQ_POST_NOTIFICATIONS = 4002
        private const val REQ_FILE_CHOOSER = 4003

        // The only origin the WebView loads; anything else opens externally.
        private const val APP_HOST = "127.0.0.1"
        private const val APP_URL = "http://127.0.0.1:8765/"
        private const val MAX_LOAD_RETRIES = 20

        // MainActivity is exported, so only whitelisted tokens may reach evaluateJavascript.
        private val ALLOWED_TABS = setOf("home", "time", "context", "consult")

        private fun safeTab(raw: String?): String? =
            if (raw != null && ALLOWED_TABS.contains(raw)) raw else null
    }

    private fun switchToTab(raw: String?) {
        val tab = safeTab(raw) ?: return
        webView.evaluateJavascript("window.switchTab&&switchTab('$tab')", null)
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Dark background prevents white flash while WebView loads.
        window.decorView.setBackgroundColor(0xff0d1117.toInt())
        title = ""

        // Keystore provisioning lives in the service; duplicating it here could re-provision
        // a different key and break encrypted data. The service owns the daemon lifecycle.
        val svc = Intent(this, DaemonForegroundService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(svc)
        } else {
            startService(svc)
        }

        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        webView = WebView(this).apply {
            // Dark background prevents white flash before page loads.
            setBackgroundColor(0xff0d1117.toInt())
            // ArkWeb shows an address bar for HTTP URLs; clip it behind the status bar.
            val density = resources.displayMetrics.density
            layoutParams = FrameLayout.LayoutParams(-1, -1).apply {
                topMargin = (-48 * density).toInt()
            }
            settings.apply {
                javaScriptEnabled = true; domStorageEnabled = true
                allowFileAccess = false; allowContentAccess = false
                mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW
            }
            addJavascriptInterface(JsBridge(), "openCodeBridge")
            webViewClient = object : WebViewClient() {
                override fun onPageFinished(view: WebView?, url: String?) {
                    super.onPageFinished(view, url)
                    loadRetries = 0
                    view?.evaluateJavascript("javascript:window.openCodeBridgeReady&&openCodeBridgeReady()", null)
                    switchToTab(intent?.getStringExtra("target_tab"))
                }

                // Stay on the daemon origin; open anything else in the system handler.
                override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                    val uri = request?.url ?: return false
                    // Require http: and port 8765 too, not just the host.
                    if (uri.host == APP_HOST &&
                        uri.scheme == "http" &&
                        uri.port == 8765) return false
                    return try {
                        startActivity(Intent(Intent.ACTION_VIEW, uri).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                        true
                    } catch (_: Exception) {
                        true // no external handler → swallow rather than load in-app
                    }
                }

                // Daemon may still be booting; retry with backoff, not a permanent error page.
                override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: WebResourceError?) {
                    super.onReceivedError(view, request, error)
                    if (request?.isForMainFrame == true && loadRetries < MAX_LOAD_RETRIES) {
                        loadRetries++
                        view?.postDelayed({ view.loadUrl(APP_URL) }, 500L * loadRetries)
                    }
                }
            }
            webChromeClient = object : WebChromeClient() {
                // Keep the activity title clean.
                override fun onReceivedTitle(view: WebView?, title: String?) {
                    // Don't call super — prevents URL from appearing in title bar.
                }
                // Without these, JS alert/confirm hang the JS context; show native dialogs.
                override fun onJsAlert(view: WebView?, url: String?, message: String?, result: JsResult?): Boolean {
                    AlertDialog.Builder(this@MainActivity)
                        .setMessage(message)
                        .setPositiveButton("确定") { _, _ -> result?.confirm() }
                        .setOnCancelListener { result?.cancel() }
                        .show()
                    return true
                }
                override fun onJsConfirm(view: WebView?, url: String?, message: String?, result: JsResult?): Boolean {
                    AlertDialog.Builder(this@MainActivity)
                        .setMessage(message)
                        .setPositiveButton("确定") { _, _ -> result?.confirm() }
                        .setNegativeButton("取消") { _, _ -> result?.cancel() }
                        .setOnCancelListener { result?.cancel() }
                        .show()
                    return true
                }

                // WebView doesn't open a picker itself — the host app must, returning the Uri via callback.
                override fun onShowFileChooser(
                    view: WebView?,
                    callback: android.webkit.ValueCallback<Array<android.net.Uri>>?,
                    fileChooserParams: FileChooserParams?
                ): Boolean {
                    // Resolve any stale pending chooser first.
                    fileChooserCallback?.onReceiveValue(null)
                    fileChooserCallback = callback
                    val intent = fileChooserParams?.createIntent()
                        ?: Intent(Intent.ACTION_GET_CONTENT).apply {
                            addCategory(Intent.CATEGORY_OPENABLE)
                            type = "application/zip"
                        }
                    return try {
                        @Suppress("DEPRECATION")
                        startActivityForResult(intent, REQ_FILE_CHOOSER)
                        true
                    } catch (e: Exception) {
                        fileChooserCallback = null
                        android.util.Log.w("MainActivity", "file chooser unavailable: ${e.message}")
                        false
                    }
                }
            }
            loadUrl(APP_URL)
        }
        setContentView(webView)

        // Screen-off is a re-lock trigger.
        // Android 13+ requires receivers to declare export status explicitly.
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.TIRAMISU) {
            registerReceiver(screenOffReceiver, IntentFilter(Intent.ACTION_SCREEN_OFF), RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            registerReceiver(screenOffReceiver, IntentFilter(Intent.ACTION_SCREEN_OFF))
        }

        // Android 13+ needs runtime consent or star alerts are silently dropped.
        requestNotificationPermissionIfNeeded()
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(
                arrayOf(android.Manifest.permission.POST_NOTIFICATIONS),
                REQ_POST_NOTIFICATIONS,
            )
        }
    }

    /** Gates the WebView behind the App Lock on cold start / background timeout /
     * manual lock / screen-off; the daemon keeps running regardless. */
    override fun onResume() {
        super.onResume()
        // Skip re-evaluation if LockActivity is already up, to avoid relaunching it.
        if (lockScreenShowing) return
        if (LockManager.needsUnlock(this)) {
            webView.visibility = View.INVISIBLE
            lockScreenShowing = true
            // TODO: migrate to ActivityResultLauncher — startActivityForResult is deprecated in API 30+
            @Suppress("DEPRECATION")
            startActivityForResult(Intent(this, LockActivity::class.java), REQ_UNLOCK)
        } else {
            webView.visibility = View.VISIBLE
        }
    }

    // TODO: migrate to registerForActivityResult(ActivityResultContracts.StartActivityForResult())
    @Suppress("DEPRECATION")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_FILE_CHOOSER) {
            val cb = fileChooserCallback
            fileChooserCallback = null
            if (cb != null) {
                cb.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(resultCode, data))
            } else if (resultCode == RESULT_OK) {
                // Callback was lost to an activity recreation — say so instead of dropping silently.
                android.widget.Toast.makeText(this, "File pick was interrupted — please retry", android.widget.Toast.LENGTH_LONG).show()
            }
            return
        }
        if (requestCode == REQ_UNLOCK) {
            lockScreenShowing = false
            if (resultCode == RESULT_OK) {
                webView.visibility = View.VISIBLE
            } else {
                // Not unlocked — do not expose the WebView; leave the app.
                moveTaskToBack(true)
            }
        }
    }

    override fun onDestroy() {
        try { unregisterReceiver(screenOffReceiver) } catch (_: Exception) {}
        // Resolve any pending file chooser against the destroyed activity.
        try { fileChooserCallback?.onReceiveValue(null); fileChooserCallback = null } catch (_: Exception) {}
        discardImportTemp()
        // Cancel pending boot-retries so none fire against the torn-down view.
        try {
            webView.handler?.removeCallbacksAndMessages(null)
            webView.stopLoading()
            (webView.parent as? android.view.ViewGroup)?.removeView(webView)
            webView.destroy()
        } catch (_: Exception) {}
        super.onDestroy()
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        setIntent(intent) // so a later reload reflects the warm-launch target_tab
        // Notification tapped while running → jump to the target tab (whitelisted).
        switchToTab(intent?.getStringExtra("target_tab"))
    }

    // TODO: migrate to OnBackPressedDispatcher.addCallback — onBackPressed is deprecated in API 33+
    @Suppress("DEPRECATION")
    override fun onBackPressed() { if (webView.canGoBack()) webView.goBack() else super.onBackPressed() }

    inner class JsBridge {
        @JavascriptInterface fun getPlatform() = "android"
        @JavascriptInterface fun getAppVersion() = try { packageManager.getPackageInfo(packageName, 0).versionName ?: "unknown" } catch (_: Exception) { "unknown" }
        @JavascriptInterface fun toast(msg: String) { runOnUiThread { android.widget.Toast.makeText(this@MainActivity, msg, android.widget.Toast.LENGTH_SHORT).show() } }

        // Chunked streaming via Kotlin↔daemon; one giant base64 string would OOM the WebView.

        /** Export: POST extras to the daemon and stream the zip into Download/H. */
        @JavascriptInterface fun exportBackup(fileName: String, extrasJson: String): String {
            val safe = fileName.replace('/', '_').replace('\\', '_').ifBlank { "openyou-backup.zip" }
            var conn: java.net.HttpURLConnection? = null
            var uri: android.net.Uri? = null
            return try {
                conn = java.net.URL("http://127.0.0.1:8765/api/backup/export")
                    .openConnection() as java.net.HttpURLConnection
                conn.requestMethod = "POST"
                conn.setRequestProperty("Content-Type", "application/json")
                conn.connectTimeout = 10_000
                conn.readTimeout = 120_000
                conn.doOutput = true
                conn.outputStream.use { it.write(extrasJson.toByteArray(Charsets.UTF_8)) }
                if (conn.responseCode != 200) return jsonErr("daemon HTTP ${conn.responseCode}")
                val warns = conn.getHeaderField("X-Backup-Warnings") ?: "0"
                val values = android.content.ContentValues().apply {
                    put(android.provider.MediaStore.Downloads.DISPLAY_NAME, safe)
                    put(android.provider.MediaStore.Downloads.MIME_TYPE, "application/zip")
                    put(android.provider.MediaStore.Downloads.RELATIVE_PATH, "Download/H")
                }
                uri = contentResolver.insert(android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                    ?: return jsonErr("MediaStore insert failed")
                val out = contentResolver.openOutputStream(uri)
                if (out == null) {
                    contentResolver.delete(uri, null, null)
                    return jsonErr("cannot open output stream")
                }
                out.use { os -> conn.inputStream.use { inp -> inp.copyTo(os) } }
                org.json.JSONObject()
                    .put("ok", true)
                    .put("name", queryDisplayName(uri) ?: safe)
                    .put("warnings", warns)
                    .toString()
            } catch (e: Exception) {
                // Remove the orphaned MediaStore row so no half-written zip remains.
                if (uri != null) try { contentResolver.delete(uri, null, null) } catch (_: Exception) {}
                android.util.Log.w("MainActivity", "exportBackup failed: ${e.message}")
                jsonErr(e.message ?: "export failed")
            } finally {
                conn?.disconnect()
            }
        }

        /** Import step 1: open a temp file the picked archive is streamed into. */
        @JavascriptInterface fun beginImport(): Boolean {
            discardImportTemp()
            return try {
                val f = java.io.File.createTempFile("openyou-import-", ".zip", cacheDir)
                importTempFile = f
                importTempOut = java.io.BufferedOutputStream(java.io.FileOutputStream(f))
                true
            } catch (e: Exception) {
                android.util.Log.w("MainActivity", "beginImport failed: ${e.message}")
                false
            }
        }

        /** Import step 2: append one base64 chunk (~3MB raw each from JS). */
        @JavascriptInterface fun appendImportChunk(b64: String): Boolean {
            val out = importTempOut ?: return false
            return try {
                out.write(android.util.Base64.decode(b64, android.util.Base64.DEFAULT))
                true
            } catch (e: Exception) {
                android.util.Log.w("MainActivity", "appendImportChunk failed: ${e.message}")
                false
            }
        }

        /** Import step 3: stream the temp file to the daemon; return its JSON response verbatim. */
        @JavascriptInterface fun finishImport(): String {
            try { importTempOut?.close() } catch (_: Exception) {}
            importTempOut = null
            val f = importTempFile
            importTempFile = null
            if (f == null) return jsonErr("no import in progress")
            var conn: java.net.HttpURLConnection? = null
            return try {
                conn = java.net.URL("http://127.0.0.1:8765/api/backup/import")
                    .openConnection() as java.net.HttpURLConnection
                conn.requestMethod = "POST"
                conn.setRequestProperty("Content-Type", "application/json")
                conn.connectTimeout = 10_000
                conn.readTimeout = 120_000
                conn.doOutput = true
                conn.setFixedLengthStreamingMode(f.length())
                java.io.BufferedOutputStream(conn.outputStream).use { out ->
                    java.io.BufferedInputStream(java.io.FileInputStream(f)).use { inp -> inp.copyTo(out) }
                }
                val code = conn.responseCode
                val stream = if (code in 200..299) conn.inputStream else conn.errorStream
                stream?.bufferedReader()?.use { it.readText() } ?: jsonErr("empty daemon response")
            } catch (e: Exception) {
                android.util.Log.w("MainActivity", "finishImport failed: ${e.message}")
                jsonErr(e.message ?: "import failed")
            } finally {
                conn?.disconnect()
                f.delete()
            }
        }

        /** MediaStore's actual display name (suffixes "(1)" on collision). */
        private fun queryDisplayName(uri: android.net.Uri): String? {
            return try {
                contentResolver.query(
                    uri,
                    arrayOf(android.provider.MediaStore.Downloads.DISPLAY_NAME),
                    null, null, null
                )?.use { c -> if (c.moveToFirst()) c.getString(0) else null }
            } catch (_: Exception) {
                null
            }
        }

        private fun jsonErr(msg: String): String =
            try { org.json.JSONObject().put("ok", false).put("error", msg).toString() }
            catch (_: Exception) { "{\"ok\":false,\"error\":\"backup transfer failed\"}" }

        /** Manual lock: gates the WebView on the next resume. */
        @JavascriptInterface fun lockNow() { LockManager.lockNow() }
        /** Re-lock timeout in minutes (1/5/15). */
        @JavascriptInterface fun getLockTimeout() = LockManager.timeoutMinutes(this@MainActivity)
        @JavascriptInterface fun setLockTimeout(minutes: Int) { LockManager.setTimeoutMinutes(this@MainActivity, minutes) }
    }
}
