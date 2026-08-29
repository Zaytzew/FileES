package net.filees.mobile

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build
import androidx.core.content.getSystemService

class FileesApp : Application() {
    override fun onCreate() {
        super.onCreate()
        TreeZip.sweep(cacheDir)
        createWatchNotificationChannel()
        FileesWatchScheduler.ensure(this)
    }

    private fun createWatchNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val channel = NotificationChannel(
            FileesWatchTick.NOTIFICATION_CHANNEL_ID,
            getString(R.string.notification_channel_watch),
            NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = getString(R.string.notification_channel_watch_description)
        }
        getSystemService<NotificationManager>()?.createNotificationChannel(channel)
    }
}
