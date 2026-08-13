package io.openyou.app

import android.app.Notification
import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.IBinder
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/** Keeps the Go daemon alive via an ongoing notification + START_STICKY, and long-polls
 * its notification queue. */
class DaemonForegroundService : Service() {

    companion object {
        private const val NOTIFICATION_ID = 1001
        private const val POLL_URL = "http://127.0.0.1:8765/api/notifications/poll"
        @Volatile private var daemonThread: Thread? = null

        // @Synchronized makes the check-then-act atomic.
        @Synchronized
        private fun isDaemonAlive(): Boolean = daemonThread?.isAlive == true

        /** Provisions the Keystore master key first (critical on boot — MainActivity never runs). */
        fun startDaemonOnce(ctx: android.content.Context) {
            if (isDaemonAlive()) return
            // Class-level lock guards the singleton if the OS makes two Service instances.
            synchronized(DaemonForegroundService::class.java) {
                if (isDaemonAlive()) return
                // Must precede the daemon start so master.key exists first.
                try {
                    KeystoreManager.provisionMasterKeyFile(ctx)
                } catch (e: Exception) {
                    android.util.Log.w("DaemonService", "keystore provision failed: ${e.message}")
                }
                daemonThread = Thread {
                    try {
                        // JNI blocks for the daemon's lifetime, so isDaemonAlive() reflects reality.
                        MainActivity.startDaemon()
                    } catch (e: Throwable) {
                        // Throwable, not Exception — loadLibrary failures are Errors that would
                        // otherwise kill the thread silently on boot retries.
                        android.util.Log.e("DaemonService", "daemon crashed", e)
                        // daemonThread stays set; the next startDaemonOnce restarts it.
                    }
                }.also { it.isDaemon = true; it.start() }
            }
        }
    }

    @Volatile private var polling = false
    @Volatile private var pollingThread: Thread? = null
    private var consecutiveFailures = 0
    private val maxFailures = 3

    override fun onCreate() {
        super.onCreate()
        NotificationHelper.ensureChannels(this)
        // Android 12+ can reject a background FGS start; degrade, don't crash-loop.
        try {
            // API 34+: startForeground throws unless the service type is passed explicitly.
            if (Build.VERSION.SDK_INT >= 34) {
                startForeground(
                    NOTIFICATION_ID,
                    buildOngoing(),
                    android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
                )
            } else {
                startForeground(NOTIFICATION_ID, buildOngoing())
            }
        } catch (e: Exception) {
            android.util.Log.w("DaemonService", "startForeground failed: ${e.message}")
        }
        startDaemonOnce(applicationContext)
        startPollLoop()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // START_STICKY redelivery or app reopen revives a daemon that died inside the living process.
        startDaemonOnce(applicationContext)
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    // Keep running even when the task is swiped away — openyou is always here.
    override fun onTaskRemoved(rootIntent: Intent?) { /* do not stopSelf */ }

    override fun onDestroy() {
        polling = false
        pollingThread?.interrupt() // end the long-poll immediately, not after its 35s timeout
        super.onDestroy()
    }

    private fun buildOngoing(): Notification {
        val builder = Notification.Builder(this, NotificationHelper.CHANNEL_DAEMON)
            .setContentTitle("openyou 正在运行")
            .setContentText("在后台整理你的信息、检查雷达")
            .setSmallIcon(R.drawable.ic_notification)
            .setOngoing(true)
        return builder.build()
    }

    /** Long-polls the daemon's notification queue; backs off so we never busy-loop it. */
    private fun startPollLoop() {
        if (polling) return
        polling = true
        pollingThread = Thread {
            while (polling) {
                var conn: HttpURLConnection? = null
                var ok = false
                try {
                    conn = (URL(POLL_URL).openConnection() as HttpURLConnection).apply {
                        connectTimeout = 5000
                        readTimeout = 35000 // > server's 25s long-poll
                        requestMethod = "GET"
                    }
                    if (conn.responseCode == 200) {
                        ok = true
                        consecutiveFailures = 0
                        // Cap response body at 64 KB to bound memory use.
                        val body = conn.inputStream.let { stream ->
                            val maxBytes = 64 * 1024
                            val sb = StringBuilder()
                            val buf = CharArray(4096)
                            val reader = stream.bufferedReader()
                            var total = 0
                            var n = reader.read(buf)
                            while (total < maxBytes && n != -1) {
                                sb.append(buf, 0, n)
                                total += n
                                n = reader.read(buf)
                            }
                            sb.toString()
                        }
                        dispatchNotifications(body)
                    }
                } catch (_: Exception) {
                    // Daemon not ready or transient error.
                } finally {
                    conn?.disconnect()
                }
                // Back off on any non-200 so we never busy-loop the daemon.
                if (!ok) {
                    consecutiveFailures++
                    if (consecutiveFailures >= maxFailures) {
                        consecutiveFailures = 0
                        NotificationHelper.postFromPayload(
                            applicationContext,
                            """{"type":"health-warning","id":"daemon-offline","title":"后台服务异常","body":"openyou 暂时无法在后台工作，请打开应用恢复"}"""
                        )
                    }
                    try { Thread.sleep(3000) } catch (_: InterruptedException) { return@Thread }
                }
            }
        }.apply { isDaemon = true; start() }
    }

    private fun dispatchNotifications(json: String) {
        val obj = try { JSONObject(json) } catch (_: Exception) { return }
        val arr = obj.optJSONArray("notifications") ?: return
        for (i in 0 until arr.length()) {
            val item = arr.optJSONObject(i) ?: continue
            NotificationHelper.postFromPayload(applicationContext, item.toString())
        }
    }
}

