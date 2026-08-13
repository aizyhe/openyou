package io.openyou.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import org.json.JSONObject

/** Creates channels and posts OS notifications from Go-daemon JSON payloads. */
object NotificationHelper {

    // -v3 suffix: channel display names are frozen by the system, so renames need new IDs.
    const val CHANNEL_ALERTS = "openyou-alerts-v3"       // HIGH: star alert
    const val CHANNEL_INSIGHTS = "openyou-insights-v3"   // DEFAULT: pattern / memory confirm
    const val CHANNEL_REMINDERS = "openyou-reminders-v3" // LOW: feedback / health
    const val CHANNEL_DAEMON = "openyou-daemon-v3"       // LOW: foreground-service ongoing

    private val LEGACY_CHANNELS = listOf(
        "openyou-alerts", "openyou-insights", "openyou-reminders", "openyou-daemon",
        "openyou-alerts-v2", "openyou-insights-v2", "openyou-reminders-v2", "openyou-daemon-v2"
    )

    fun ensureChannels(ctx: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = ctx.getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ALERTS, "openyou · 警报", NotificationManager.IMPORTANCE_HIGH)
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_INSIGHTS, "openyou · 发现", NotificationManager.IMPORTANCE_DEFAULT)
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_REMINDERS, "openyou · 提醒", NotificationManager.IMPORTANCE_LOW)
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_DAEMON, "openyou · 运行", NotificationManager.IMPORTANCE_LOW)
        )
        // Delete the orphaned legacy channels; a no-op on fresh installs.
        LEGACY_CHANNELS.forEach { nm.deleteNotificationChannel(it) }
    }

    private fun channelFor(type: String): String = when (type) {
        "star-alert" -> CHANNEL_ALERTS
        "pattern-discovery", "memory-confirm" -> CHANNEL_INSIGHTS
        else -> CHANNEL_REMINDERS // feedback-reminder, health-warning, unknown
    }

    fun postFromPayload(ctx: Context, payloadJson: String) {
        ensureChannels(ctx)
        val nm = ctx.getSystemService(NotificationManager::class.java) ?: return
        val p = try { JSONObject(payloadJson) } catch (_: Exception) { return }

        val type = p.optString("type", "reminder")
        val title = p.optString("title", "openyou")
        val body = p.optString("body", "")
        // Payload is untrusted; allowlist target_tab before it reaches evaluateJavascript.
        val rawTab = p.optString("target_tab", "")
        val targetTab = when (rawTab) {
            "home", "time", "context", "consult" -> rawTab
            else -> ""
        }

        val intent = Intent(ctx, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
            if (targetTab.isNotEmpty()) putExtra("target_tab", targetTab)
        }
        val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M)
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        else PendingIntent.FLAG_UPDATE_CURRENT
        // PendingIntent ignores extras — encode the destination or taps jump to the newest target_tab.
        val pending = PendingIntent.getActivity(ctx, "$type|$targetTab".hashCode(), intent, flags)

        val builder = Notification.Builder(ctx, channelFor(type))
            .setContentTitle(title)
            .setContentText(body)
            .setSmallIcon(R.drawable.ic_notification)
            .setAutoCancel(true)
            .setContentIntent(pending)
        if (body.length > 40) {
            builder.setStyle(Notification.BigTextStyle().bigText(body))
        }

        // Stable id per notification type so repeats replace rather than stack.
        val id = p.optString("id", type).hashCode()
        nm.notify(id, builder.build())
    }
}
