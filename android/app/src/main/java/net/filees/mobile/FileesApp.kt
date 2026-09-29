package net.filees.mobile

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build
import androidx.core.content.getSystemService

class FileesApp : Application() {
    override fun attachBaseContext(base: Context) {
        super.attachBaseContext(FileesLocale.wrap(base))
    }

    override fun onCreate() {
        super.onCreate()
        TreeZip.sweep(cacheDir)
        runCatching {
            DownloadCache.sweep(cacheDir, keepApkName = AutoUpdate.ready(this)?.sha256?.let { "filees-update-$it.apk" })
        }
        FileesDeviceGate.install(this)
        createWatchNotificationChannel()
        FileesWatchScheduler.ensure(this)
        AutoUpdate.ensure(this)
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
