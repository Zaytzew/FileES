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
 * mobile-uploads/. Eight or more new files in one tree are packed as
 * UPLOAD_TREE; smaller bursts drain one object per session. Used by the
 * 5-minute WorkManager tick and by the foreground activity when it comes
 * back.
 */
object FileesWatchTick {
    const val NOTIFICATION_CHANNEL_ID = "filees-watch-uploads"
    private const val NOTIFICATION_FAIL_ID = 1002
    private const val NOTIFICATION_WAIT_ID = 1003

    fun run(context: Context, cancel: CaptureCancellation = CaptureCancellation(), onWork: (String) -> Unit = {}): Int = CaptureCoordinator.run(cancel) {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        FileesSession.migrate(prefs)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null) ?: return@run 0
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null) ?: return@run 0
        val repoId = prefs.getString(FileesSession.PREF_UPLOAD_REPO_ID, null) ?: return@run 0
        val repoName = prefs.getString(FileesSession.PREF_UPLOAD_REPO_NAME, null) ?: repoId
        if (address.isBlank() || hostKey.isBlank() || repoId.isBlank()) return@run 0
        val watched = WatchedFolders(context)
        val result = CaptureTransfers.Result()
        val status = WatchStatusStore(context)
        val scope = WatchStatusStore.scope(context)
        val trees = watched.uris()
        val token = status.begin(scope, trees)
        try {
            val client = Androidbind.newClient(context.filesDir.absolutePath, DialAddress.resolve(address), FileesSession.MOBILE_USER, hostKey)
            cancel.attach(client)
            val before = PendingUpload.listFromJson(client.listUploadsJSON(repoId)).associateBy { it.id }
            val ownedSources = before.values.flatMap { it.sources }.toSet()
            before.values.filter { it.delivered }.forEach { item -> item.sources.forEach { watched.markSeen(it) } }
            for (tree in trees) {
                cancel.check()
                try {
                    val unseen = DocumentWalk.tree(context.contentResolver, tree, cancel).filterNot {
                        watched.alreadySeen(CaptureTransfers.source(it))
                    }
                    status.scanned(scope,tree)
                    if (unseen.isEmpty()) continue
                    if (unseen.any { CaptureTransfers.source(it) !in ownedSources }) onWork(context.getString(R.string.watch_state_preparing))
                    status.phase(scope,tree,"preparing")
                    val part = CaptureTransfers.send(context, client, repoId, unseen, FolderPreflight.of(unseen).pack, cancel, watched, queueOnly = true, progress = {
                        status.phase(scope,tree,"preparing")
                        status.updateQueue(scope,trees,PendingUpload.listFromJson(client.listUploadsJSON(repoId)))
                    })
                    result.sent += part.sent
                    result.errors += part.errors
                    part.errors.forEach { status.problem(scope,tree,it) }
                    status.updateQueue(scope,trees,PendingUpload.listFromJson(client.listUploadsJSON(repoId)))
                } catch (e: Exception) {
                    cancel.check()
                    status.problem(scope,tree,e.message ?: context.getString(R.string.error_send))
                    result.errors += "${tree.lastPathSegment}: ${e.message}"
                }
            }
            val queued = PendingUpload.listFromJson(client.listUploadsJSON(repoId))
                .filter { it.state == "pending-create" || it.state == "uploading" }
                .sortedBy { it.lastAttemptAt }
            status.updateQueue(scope,trees,PendingUpload.listFromJson(client.listUploadsJSON(repoId)),finish = true)
            for (item in queued) {
                cancel.check()
                val remaining = PendingUpload.listFromJson(client.listUploadsJSON(repoId)).filter { !it.delivered && it.state != "discarded" }.sumOf { it.fileCount }
                onWork(context.getString(R.string.watch_queue_count,remaining))
                trees.filter { tree -> item.sources.any { WatchStatusStore.belongs(it,tree) } }
                    .forEach { status.phase(scope,it,"sending") }
                client.sendUploadJSON(repoId,item.id)
                status.updateQueue(scope,trees,PendingUpload.listFromJson(client.listUploadsJSON(repoId)),finish = true)
            }
            val drained = PendingUpload.listFromJson(client.listUploadsJSON(repoId)).filter { it.state != "discarded" }
            status.updateQueue(scope,trees,drained,finish = true)
            for (item in drained) {
                if (item.delivered) {
                    item.sources.forEach { watched.markSeen(it) }
                    if (before[item.id]?.delivered != true) result.sent += item.fileCount
                } else {
                    result.waiting += item.fileCount
                    if (item.lastError.isNotBlank()) result.errors += item.lastError
                }
            }

        } catch (e: Exception) {
            if (cancel.isCancelled) {
                trees.forEach { status.paused(scope,it,cancel.stopReason) }
                throw e
            }
            trees.forEach { status.problem(scope,it,e.message ?: context.getString(R.string.error_send)) }
            cancel.check()
            result.errors += e.message ?: context.getString(R.string.error_send)
        } finally { status.end(token) }
        val failure = result.errors.distinct().take(5).joinToString("\n").ifBlank { null }
        recordJournal(context, result.sent, result.waiting, repoName, failure)
        if (failure != null) notifyMessage(context, context.getString(R.string.notification_watch_failed), failure, NOTIFICATION_FAIL_ID)
        else if (result.waiting > 0) notifyMessage(context, context.getString(R.string.notification_watch_waiting),
            context.getString(R.string.notification_watch_waiting_text, result.waiting), NOTIFICATION_WAIT_ID)
        // Receipt and journal are the success record; successful background work is silent.
        result.sent
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
