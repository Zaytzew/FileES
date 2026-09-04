package net.filees.mobile

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidbind.Androidbind
import androidbind.Client
import java.io.File

/**
 * One scan of the operator-chosen Android locations. New objects go to
 * mobile-uploads/. Eight or more new files in one tree are packed as
 * UPLOAD_TREE; smaller bursts drain one object per session. Used by the
 * 5-minute WorkManager tick and by the foreground activity when it comes
 * back.
 */
object FileesWatchTick {
    const val NOTIFICATION_CHANNEL_ID = "filees-watch-uploads"
    private const val NOTIFICATION_ID = 1001
    private const val NOTIFICATION_FAIL_ID = 1002
    private const val NOTIFICATION_WAIT_ID = 1003

    fun run(context: Context): Int {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        FileesSession.migrate(prefs)
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
        var waiting = 0
        try {
            for (tree in trees) {
                val unseen = DocumentWalk.tree(context.contentResolver, tree).filterNot {
                    watched.alreadySeen(it.uri.toString() + "/" + it.filename)
                }
                if (unseen.isEmpty()) continue
                val result = if (FolderPreflight.of(unseen).pack) {
                    sendPacked(context, client, watched, repoId, unseen)
                } else {
                    sendOneByOne(context, client, watched, repoId, unseen)
                }
                sent += result.first
                waiting += result.second
            }
        } catch (e: Exception) {
            notifyMessage(
                context,
                context.getString(R.string.notification_watch_failed),
                e.message ?: context.getString(R.string.error_send),
                NOTIFICATION_FAIL_ID,
            )
            throw e
        }
        if (sent > 0) notifySent(context, sent, repoName)
        if (waiting > 0) {
            notifyMessage(
                context,
                context.getString(R.string.notification_watch_waiting),
                context.getString(R.string.notification_watch_waiting_text, waiting),
                NOTIFICATION_WAIT_ID,
            )
        }
        return sent
    }

    // Same threshold as the foreground "Dodaj folder" path: eight or more
    // new files in one watched tree become one UPLOAD_TREE session, not a
    // storm of SSH handshakes (TREE_INGEST). Smaller bursts stay one-by-one.
    private fun sendPacked(
        context: Context,
        client: Client,
        watched: WatchedFolders,
        repoId: String,
        files: List<WalkedFile>,
    ): Pair<Int, Int> {
        var zip: File? = null
        try {
            zip = TreeZip.pack(context.contentResolver, files, context.cacheDir)
            client.uploadTreeFile(repoId, UploadPaths.ROOT, files.size.toLong(), zip.absolutePath)
            files.forEach { watched.markSeen(it.uri.toString() + "/" + it.filename) }
            return files.size to 0
        } finally {
            zip?.delete()
        }
    }

    private fun sendOneByOne(
        context: Context,
        client: Client,
        watched: WatchedFolders,
        repoId: String,
        files: List<WalkedFile>,
    ): Pair<Int, Int> {
        var sent = 0
        var waiting = 0
        for (file in files) {
            val bytes = context.contentResolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
            client.enqueueUpload(repoId, UploadPaths.parent(file.relativeDir), file.filename, file.contentType, bytes)
            val report = UploadDrain.run(client, repoId)
            if (report.transportError != null) {
                throw RuntimeException(report.transportError)
            }
            watched.markSeen(file.uri.toString() + "/" + file.filename)
            sent++
            waiting = report.decisions.size
        }
        return sent to waiting
    }

    // The only user-visible sign this silent, periodic background tick did
    // anything at all - previously nothing told the user a watched folder
    // had (or had not) actually been uploaded.
    private fun notifySent(context: Context, count: Int, repoName: String) {
        notifyMessage(
            context,
            context.getString(R.string.notification_watch_sent),
            context.getString(R.string.notification_watch_sent_text, count, repoName),
            NOTIFICATION_ID,
        )
    }

    private fun notifyMessage(context: Context, title: String, text: String, id: Int) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            return
        }
        val notification = NotificationCompat.Builder(context, NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_file)
            .setContentTitle(title)
            .setContentText(text)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        NotificationManagerCompat.from(context).notify(id, notification)
    }
}
