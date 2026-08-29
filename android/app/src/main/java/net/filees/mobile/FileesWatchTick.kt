package net.filees.mobile

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidbind.Androidbind

/**
 * One scan of the operator-chosen Android locations. New objects go to
 * mobile-uploads/ and are drained one file at a time. Used by the 5-minute
 * WorkManager tick and by the foreground activity when it comes back.
 */
object FileesWatchTick {
    const val NOTIFICATION_CHANNEL_ID = "filees-watch-uploads"
    private const val NOTIFICATION_ID = 1001

    fun run(context: Context): Int {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null) ?: return 0
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null) ?: return 0
        // Deliberately PREF_UPLOAD_REPO_ID, not PREF_REPO_ID (the browser's
        // transient "which repo am I looking at" field, cleared on every
        // MainActivity#goUp back to the top-level list) - see its own doc
        // comment in FileesSession.kt for why the two must not be the same
        // preference.
        val repoId = prefs.getString(FileesSession.PREF_UPLOAD_REPO_ID, null) ?: return 0
        val repoName = prefs.getString(FileesSession.PREF_UPLOAD_REPO_NAME, null) ?: repoId
        if (address.isBlank() || hostKey.isBlank() || repoId.isBlank()) return 0

        val watched = WatchedFolders(context)
        val trees = watched.uris()
        if (trees.isEmpty()) return 0

        val client = Androidbind.newClient(
            context.filesDir.absolutePath,
            DialAddress.resolve(address),
            FileesSession.MOBILE_USER,
            hostKey,
        )
        var sent = 0
        for (tree in trees) {
            for (file in DocumentWalk.tree(context.contentResolver, tree)) {
                val seen = file.uri.toString() + "/" + file.filename
                if (watched.alreadySeen(seen)) continue
                val bytes = context.contentResolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
                client.enqueueUpload(repoId, UploadPaths.parent(file.relativeDir), file.filename, file.contentType, bytes)
                UploadDrain.drainOrThrow(client, repoId)
                watched.markSeen(seen)
                sent++
            }
        }
        if (sent > 0) notifySent(context, sent, repoName)
        return sent
    }

    // The only user-visible sign this silent, periodic background tick did
    // anything at all - previously nothing told the user a watched folder
    // had (or had not) actually been uploaded.
    private fun notifySent(context: Context, count: Int, repoName: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            return
        }
        val notification = NotificationCompat.Builder(context, NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_file)
            .setContentTitle(context.getString(R.string.notification_watch_sent))
            .setContentText(context.getString(R.string.notification_watch_sent_text, count, repoName))
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        NotificationManagerCompat.from(context).notify(NOTIFICATION_ID, notification)
    }
}
