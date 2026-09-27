package net.filees.mobile

import android.app.Instrumentation
import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidbind.Androidbind
import java.io.File

/** Actual WorkManager -> dataSync foreground service -> SSH/SVN -> lost ACK -> retry. */
object WatchForegroundChecks {
    fun run(test: Instrumentation, hostKey: String) {
        val context = test.targetContext
        val manager = WorkManager.getInstance(context)
        manager.cancelUniqueWork("filees-watch-tick").result.get()
        manager.cancelUniqueWork("filees-watch-tick-soon").result.get()
        val prefs = context.getSharedPreferences(FileesSession.PREFS,Context.MODE_PRIVATE)
        val saved = prefs.all
        val client = Androidbind.newClient(context.filesDir.absolutePath,"10.0.2.2:22380",FileesSession.MOBILE_USER,hostKey)
        val file = WalkedFile(android.net.Uri.parse("content://net.filees.mobile.test.capture/1048576"),"watch","watch.bin","",1048576)
        val zip = TreeZip.pack(context.contentResolver,listOf(file),context.cacheDir)
        val id = client.enqueueTreeFile("repo-1",UploadPaths.ROOT,1,zip.absolutePath,"[]")
        val jobs = mutableListOf<java.util.UUID>()
        try {
            prefs.edit().putString(FileesSession.PREF_ADDRESS,"10.0.2.2:22380")
                .putString(FileesSession.PREF_HOST_KEY,hostKey).putString(FileesSession.PREF_UPLOAD_REPO_ID,"repo-1").commit()
            var sawForeground = false
            repeat(2) {
                val request = FileesWatchScheduler.request(0,false); jobs += request.id
                manager.enqueue(request).result.get()
                val limit = System.currentTimeMillis()+30000
                var state: WorkInfo.State
                do {
                    val notifications = context.getSystemService(NotificationManager::class.java).activeNotifications
                    sawForeground = sawForeground || notifications.any { it.notification.channelId=="filees-capture-active" && it.notification.flags and Notification.FLAG_FOREGROUND_SERVICE != 0 }
                    state = manager.getWorkInfoById(request.id).get().state
                    if (!state.isFinished) Thread.sleep(50)
                    check(System.currentTimeMillis()<limit) { "watch worker did not finish: $state" }
                } while (!state.isFinished)
                check(state==WorkInfo.State.SUCCEEDED)
                val item = PendingUpload.listFromJson(client.listUploadsJSON("repo-1")).single { it.id==id }
                check(item.delivered == (it==1)) { "wrong ACK state: ${item.state}: ${item.lastError}" }
            }
            check(sawForeground) { "no actual foreground-service notification observed" }
            check(!File(context.filesDir,"uploads/repo-1/$id.bin").exists())
        } finally {
            jobs.forEach { manager.cancelWorkById(it).result.get() }
            zip.delete()
            File(context.filesDir,"uploads/repo-1/$id.json").delete()
            File(context.filesDir,"uploads/repo-1/$id.bin").delete()
            val restore = prefs.edit().clear()
            saved.forEach { (key,value) -> when(value) {
                is String -> restore.putString(key,value); is Boolean -> restore.putBoolean(key,value)
                is Int -> restore.putInt(key,value); is Long -> restore.putLong(key,value)
                is Float -> restore.putFloat(key,value)
                is Set<*> -> { @Suppress("UNCHECKED_CAST") restore.putStringSet(key,value as Set<String>) }
            } }
            restore.commit()
            FileesWatchScheduler.ensure(context)
        }
    }
}
