package net.filees.mobile

import android.Manifest
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.work.*
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/** Periodic discovery/download. Installing remains an explicit system action. */
object AutoUpdate {
    const val EXTRA_ABOUT = "show_update_card"
    private const val ENABLED = "android_auto_update"
    private const val READY = "android_update_ready"
    private const val UNIQUE = "filees-auto-update"
    private const val NOTICE = 1004
    fun enabled(context: Context) = context.getSharedPreferences(FileesSession.PREFS,0).getBoolean(ENABLED,false)
    fun setEnabled(context: Context, value: Boolean) {
        context.getSharedPreferences(FileesSession.PREFS,0).edit().putBoolean(ENABLED,value).commit()
        ensure(context)
    }
    fun ensure(context: Context) {
        val wm = WorkManager.getInstance(context)
        if (!enabled(context)) { wm.cancelUniqueWork(UNIQUE); return }
        val request = PeriodicWorkRequestBuilder<AutoUpdateWorker>(24,TimeUnit.HOURS)
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL,30,TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).setRequiresStorageNotLow(true).build())
            .build()
        wm.enqueueUniquePeriodicWork(UNIQUE,ExistingPeriodicWorkPolicy.KEEP,request)
    }
    fun saveReady(context: Context, offer: ApkUpdate.Offer) {
        val json = JSONObject().put("state",offer.state).put("version",offer.version).put("sequence",offer.sequence)
            .put("sha256",offer.sha256).put("size",offer.size).put("url",offer.url)
        context.getSharedPreferences(FileesSession.PREFS,0).edit().putString(READY,json.toString()).commit()
    }
    fun ready(context: Context): ApkUpdate.Offer? = runCatching {
        val raw = context.getSharedPreferences(FileesSession.PREFS,0).getString(READY,null) ?: return null
        val j = JSONObject(raw)
        ApkUpdate.Offer("available","",j.getString("version"),j.getLong("sequence"),j.getString("sha256"),j.getLong("size"),j.getString("url"))
    }.getOrNull()
    fun clearReady(context: Context) {
        context.getSharedPreferences(FileesSession.PREFS,0).edit().remove(READY).apply()
        NotificationManagerCompat.from(context).cancel(NOTICE)
    }
    internal fun prepare(context: Context, offer: ApkUpdate.Offer, stopped: () -> Boolean,
                         fetch: () -> java.io.File, newer: (java.io.File) -> Boolean,
                         announce: (ApkUpdate.Offer) -> Unit = { notifyReady(context,it) }) {
        if(stopped() || !enabled(context)) return
        val apk = fetch()
        if(stopped() || !enabled(context)) return
        if(!newer(apk)) { clearReady(context); return }
        val previous = ready(context)
        saveReady(context,offer)
        if(previous?.sha256 != offer.sha256) announce(offer)
    }

    fun notifyReady(context: Context, offer: ApkUpdate.Offer) {
        if (android.os.Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(context,Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val intent = Intent(context,SettingsActivity::class.java).putExtra(EXTRA_ABOUT,true)
        val open = PendingIntent.getActivity(context,NOTICE,intent,PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val notification = NotificationCompat.Builder(context,FileesWatchTick.NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_file).setContentTitle(context.getString(R.string.update_ready_title))
            .setContentText(context.getString(R.string.update_available,offer.version)).setContentIntent(open).setAutoCancel(true).build()
        NotificationManagerCompat.from(context).notify(NOTICE,notification)
    }
}

class AutoUpdateWorker(context: Context, params: WorkerParameters): Worker(context,params) {
    private val cancellation = ApkUpdate.DownloadCancellation()
    override fun onStopped() { cancellation.cancel(); super.onStopped() }
    override fun doWork(): Result {
        val context = applicationContext
        if (!AutoUpdate.enabled(context)) return Result.success()
        return try {
            val offer = ApkUpdate.inspect(context)
            if (isStopped || !AutoUpdate.enabled(context)) return Result.success()
            if (offer.state == "error") return Result.retry()
            if (offer.state != "available") { if(offer.state == "current") AutoUpdate.clearReady(context); return Result.success() }
            AutoUpdate.prepare(context,offer,{ isStopped },
                { ApkUpdate.download(context,offer,cancellation) },
                { ApkUpdate.archiveVersionCode(context,it) > ApkUpdate.installedVersionCode(context) })
            Result.success()
        } catch (_: Exception) { if(isStopped || !AutoUpdate.enabled(context)) Result.success() else Result.retry() }
    }
}
