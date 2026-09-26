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

        var sent = 0
        var waiting = 0
        // Catches Throwable, not just Exception, and now wraps client
        // construction too: an OutOfMemoryError from TreeZip.pack (a >1 GB
        // file in a watch backlog, live 2026-09-26) is an Error, so it used
        // to slip past a catch(Exception) here and above in
        // FileesWatchWorker.doWork with no journal entry and no
        // notification - the only trace was WorkManager's own logcat output.
        // TreeZip now streams instead of buffering whole files, but this
        // stays broad so any future failure of this shape still reaches the
        // user instead of vanishing.
        try {
            val client = Androidbind.newClient(
                context.filesDir.absolutePath,
                DialAddress.resolve(address),
                FileesSession.MOBILE_USER,
                hostKey,
            )
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
        } catch (t: Throwable) {
            recordJournal(context, sent, waiting, repoName, t.message ?: "")
            notifyMessage(
                context,
                context.getString(R.string.notification_watch_failed),
                t.message ?: context.getString(R.string.error_send),
                NOTIFICATION_FAIL_ID,
            )
            throw t
        }
        recordJournal(context, sent, waiting, repoName, null)
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

    // Real outcomes only. A tick that found nothing does not touch the journal.
    private fun recordJournal(context: Context, sent: Int, waiting: Int, repoName: String, failure: String?) {
        if (sent <= 0 && waiting <= 0 && failure == null) return
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        if (sent > 0) {
            FileesSession.pushJournal(
                prefs,
                repoName,
                context.resources.getQuantityString(R.plurals.journal_watch_sent, sent, sent),
            )
        }
        if (waiting > 0) {
            FileesSession.pushJournal(
                prefs,
                repoName,
                context.resources.getQuantityString(R.plurals.journal_watch_waiting, waiting, waiting),
            )
        }
        if (failure != null) {
            FileesSession.pushJournal(prefs, repoName, failureSentence(context, failure))
        }
    }

    // The catalog sentence alone hid the real cause live, 2026-09-26: a
    // status-70 dispatch failure got bucketed under a generic "server does
    // not accept this yet" entry (an needle match on the operation name, not
    // the true reason), and the journal - unlike the interactive transport
    // error dialog - never showed the raw text underneath it. Appending raw
    // here mirrors what showTransportError already does for user-triggered
    // sends, so a background failure is diagnosable from the app alone.
    private fun failureSentence(context: Context, raw: String): String {
        val text = raw.trim()
        if (text.isEmpty()) return context.getString(R.string.journal_watch_failed)
        val catalog = try {
            val lang = context.resources.configuration.locales[0].language
            Androidbind.explainIn(text, lang).trim()
        } catch (_: Exception) {
            try {
                Androidbind.explain(text).trim()
            } catch (_: Exception) {
                ""
            }
        }
        val sentence = catalog.ifBlank { context.getString(R.string.journal_watch_failed) }
        return "$sentence\n$text"
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
        // BigTextStyle, not just setContentText: a raw sshtransport error
        // (e.g. "sshtransport: session failed: ...") easily exceeds the one
        // line a collapsed notification shows, and without an explicit
        // expanded style some OEM skins render it permanently truncated
        // with no way to see the rest - reported live on ColorOS/Oppo,
        // 2026-09-26.
        val notification = NotificationCompat.Builder(context, NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_file)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        NotificationManagerCompat.from(context).notify(id, notification)
    }
}
