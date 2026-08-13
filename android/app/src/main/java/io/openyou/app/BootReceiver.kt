package io.openyou.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build

/** Restarts the foreground service after reboot so openyou resumes monitoring. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val serviceIntent = Intent(context, DaemonForegroundService::class.java)
        // Android 14 forbids FGS starts from BOOT_COMPLETED; swallowing avoids a crash.
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(serviceIntent)
            } else {
                context.startService(serviceIntent)
            }
        } catch (e: Exception) {
            android.util.Log.w("BootReceiver", "boot service start rejected: ${e.message}")
        }
    }
}
