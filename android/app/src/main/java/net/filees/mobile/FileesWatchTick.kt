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
 *
 * The look itself is local: the folder walk and the upload queue do not
 * resolve a name or open SSH. A new file in any watched tree is written to
 * that queue before a foreground service or a dial, and that queue item is
 * what starts the send. Android may refuse the service; the dial still
 * happens, and a kill leaves the same id for the next tick. A conflict
 * waits for the person. The stored send-depth is not consulted here.
 */
object FileesWatchTick {
    const val NOTIFICATION_CHANNEL_ID = "filees-watch-uploads"
    private const val NOTIFICATION_PROGRESS_ID = 1001
    private const val NOTIFICATION_FAIL_ID = 1002
    private const val NOTIFICATION_WAIT_ID = 1003
    private const val NOTIFICATION_SENT_ID = 1005

    fun run(context: Context, cancel: CaptureCancellation = CaptureCancellation(), onWork: (String) -> Unit = {}): Int = CaptureCoordinator.run(cancel) {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        FileesSession.migrate(prefs)
        // One persisted server record: never mix the endpoint of A with B's target
        // if the UI switches servers while this worker is starting.
        val server = FileesSession.current(prefs) ?: return@run 0
        val address = server.address
        val hostKey = server.hostKey
        val repoId = server.uploadRepoId
        val repoName = server.uploadRepoName.ifBlank { repoId }
        if (address.isBlank() || hostKey.isBlank() || repoId.isBlank()) return@run 0
        val watched = WatchedFolders(context, server.id)
        val result = CaptureTransfers.Result()
        val status = WatchStatusStore(context)
        val scope = "${server.id}:$repoId"
        val trees = watched.uris()
        val token = status.begin(scope, trees)
        // A foreground window may promote this worker to a foreground service,
        // which is the "Wysyłanie" notification. Android refuses that start
        // from the background; the send still runs, and the same text is
        // posted as an ordinary notification instead.
        var foregroundRefused = false
        try {
            // Store handle only. Passing the paired host:port does not dial:
            // list, enqueue and the seen-receipt live under filesDir.
            val local = Androidbind.newClient(context.filesDir.absolutePath, address, FileesSession.MOBILE_USER, hostKey)
            cancel.attach(local)
            val before = PendingUpload.listFromJson(local.listUploadsJSON(repoId)).associateBy { it.id }
            val pendingCount = before.values.filter { it.state == "pending-create" || it.state == "uploading" }.sumOf { it.fileCount }
            status.updateQueue(scope,trees,before.values.toList())
            // Promotion keeps a long send alive. A refusal must not skip the
            // walk or the dial: the new file is the trigger, in every tree.
            fun promote(text: String) {
                if (foregroundRefused) {
                    notifyProgress(context, text)
                    return
                }
                try {
                    onWork(text)
                } catch (e: Exception) {
                    if (cancel.isCancelled || !FileesWatchWorker.backgroundStartDenied(e)) throw e
                    foregroundRefused = true
                    notifyProgress(context, text)
                }
            }
            if (pendingCount > 0) promote(context.getString(R.string.watch_queue_count, pendingCount))
            before.values.filter { it.delivered }.forEach { CaptureTransfers.acknowledge(local, repoId, it, watched) }
            for (tree in trees) {
                cancel.check()
                try {
                    val unseen = DocumentWalk.tree(context.contentResolver, tree, cancel).filterNot {
                        watched.alreadySeen(CaptureTransfers.source(it), repoId)
                    }
                    status.scanned(scope,tree)
                    if (unseen.isEmpty()) continue
                    status.phase(scope,tree,"preparing")
                    val part = CaptureTransfers.send(context, local, repoId, unseen, FolderPreflight.of(unseen).pack, cancel, watched, queueOnly = true, progress = {
                        status.phase(scope,tree,"preparing")
                        status.updateQueue(scope,trees,PendingUpload.listFromJson(local.listUploadsJSON(repoId)))
                    })
                    result.sent += part.sent
                    result.errors += part.errors
                    part.errors.forEach { status.problem(scope,tree,it) }
                    status.updateQueue(scope,trees,PendingUpload.listFromJson(local.listUploadsJSON(repoId)))
                } catch (e: Exception) {
                    if (FileesWatchWorker.backgroundStartDenied(e)) throw e
                    cancel.check()
                    status.problem(scope,tree,e.message ?: context.getString(R.string.error_send))
                    result.errors += "${tree.lastPathSegment}: ${e.message}"
                }
            }
            val queued = PendingUpload.listFromJson(local.listUploadsJSON(repoId))
                .filter { it.state == "pending-create" || it.state == "uploading" }
                .sortedBy { it.lastAttemptAt }
            if (queued.isEmpty()) {
                val resting = PendingUpload.listFromJson(local.listUploadsJSON(repoId)).filter { it.state != "discarded" }
                status.updateQueue(scope,trees,resting,finish = true)
                local.pruneDeliveredUploads(repoId)
            } else {
                // The queue item already exists. Ask for the foreground service
                // when the walk did not need one, then dial either way.
                promote(context.getString(R.string.watch_queue_count, queued.sumOf { it.fileCount }))
                clearStartDeferred(context)
                // Android's answer wins. The saved address is only for a miss,
                // and it is replaced only after a handshake accepts the pinned key.
                val dial = try {
                    DialAddress.resolve(address)
                } catch (e: java.net.UnknownHostException) {
                    DialAddress.cached(context, server.id, address) ?: throw e
                }
                val sender = Androidbind.newClient(context.filesDir.absolutePath, dial, FileesSession.MOBILE_USER, hostKey)
                cancel.attach(sender)
                var connected = false
                try {
                    for (item in queued) {
                        cancel.check()
                        val remaining = PendingUpload.listFromJson(sender.listUploadsJSON(repoId)).filter { !it.delivered && it.state != "discarded" }.sumOf { it.fileCount }
                        promote(context.getString(R.string.watch_queue_count, remaining))
                        trees.filter { tree -> item.sources.any { WatchStatusStore.belongs(it,tree) } }
                            .forEach { status.phase(scope,it,"sending") }
                        sender.sendUploadJSON(repoId,item.id)
                        connected = true
                        status.updateQueue(scope,trees,PendingUpload.listFromJson(sender.listUploadsJSON(repoId)),finish = true)
                    }
                } catch (e: Exception) {
                    if (FileesWatchWorker.backgroundStartDenied(e) || cancel.isCancelled) throw e
                    result.errors += e.message ?: context.getString(R.string.error_send)
                }
                if (connected) DialAddress.remember(context, server.id, address, dial)
                val drained = PendingUpload.listFromJson(sender.listUploadsJSON(repoId)).filter { it.state != "discarded" }
                status.updateQueue(scope,trees,drained,finish = true)
                for (item in drained) {
                    if (item.delivered) {
                        CaptureTransfers.acknowledge(sender, repoId, item, watched)
                        if (before[item.id]?.delivered != true) result.sent += item.fileCount
                    } else if (queued.any { it.id == item.id }) {
                        result.waiting += item.fileCount
                        if (item.lastError.isNotBlank()) result.errors += item.lastError
                    }
                }
                sender.pruneDeliveredUploads(repoId)
            }
        } catch (e: Exception) {
            if (FileesWatchWorker.backgroundStartDenied(e)) {
                NotificationManagerCompat.from(context).cancel(NOTIFICATION_PROGRESS_ID)
                trees.forEach { status.paused(scope,it,WatchStatusStore.START_NOT_ALLOWED) }
                val notice = context.getSharedPreferences("filees_watch_scheduler",Context.MODE_PRIVATE)
                if (!notice.getBoolean("start_deferred_notice",false)) {
                    notifyMessage(context,context.getString(R.string.watch_state_paused),context.getString(R.string.watch_pause_start),NOTIFICATION_WAIT_ID)
                    notice.edit().putBoolean("start_deferred_notice",true).apply()
                }
                android.util.Log.i("FileesWatch", "Foreground start deferred; queue preserved")
                throw e
            }
            if (cancel.isCancelled) {
                NotificationManagerCompat.from(context).cancel(NOTIFICATION_PROGRESS_ID)
                trees.forEach { status.paused(scope,it,cancel.stopReason) }
                throw e
            }
            trees.forEach { status.problem(scope,it,e.message ?: context.getString(R.string.error_send)) }
            cancel.check()
            result.errors += e.message ?: context.getString(R.string.error_send)
        } finally { status.end(token) }
        val failure = result.errors.distinct().take(5).joinToString("\n").ifBlank { null }
        val announce = recordJournal(context, scope, result.sent, result.waiting, repoName, failure)
        if (announce && failure != null) notifyMessage(context, context.getString(R.string.notification_watch_failed), failure, NOTIFICATION_FAIL_ID)
        else if (failure == null && result.waiting > 0) notifyMessage(context, context.getString(R.string.notification_watch_waiting),
            context.getString(R.string.notification_watch_waiting_text, result.waiting), NOTIFICATION_WAIT_ID)
        if (result.sent > 0) {
            NotificationManagerCompat.from(context).cancel(NOTIFICATION_PROGRESS_ID)
            notifyMessage(
                context,
                context.getString(R.string.notification_watch_sent),
                context.getString(R.string.notification_watch_sent_text, result.sent, repoName),
                NOTIFICATION_SENT_ID,
            )
        } else if (foregroundRefused) {
            NotificationManagerCompat.from(context).cancel(NOTIFICATION_PROGRESS_ID)
        }
        result.sent
    }

    internal fun clearStartDeferred(context: Context) {
        val prefs = context.getSharedPreferences("filees_watch_scheduler",Context.MODE_PRIVATE)
        if (prefs.getBoolean("start_deferred_notice",false)) {
            NotificationManagerCompat.from(context).cancel(NOTIFICATION_WAIT_ID)
            prefs.edit().remove("start_deferred_notice").apply()
        }
    }

    // Real outcomes only. A tick that found nothing does not touch the journal.
    // The same standing failure is one line, not a new one every five minutes.
    // Returns whether this failure should also raise a notification.
    private fun recordJournal(context: Context, scope: String, sent: Int, waiting: Int, repoName: String, failure: String?): Boolean {
        val standing = context.getSharedPreferences("filees_watch_scheduler", Context.MODE_PRIVATE)
        val key = "standing:$scope"
        if (sent <= 0 && waiting <= 0 && failure == null) {
            if (standing.contains(key)) standing.edit().remove(key).apply()
            NotificationManagerCompat.from(context).cancel(NOTIFICATION_FAIL_ID)
            return false
        }
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        val archive = FileesSession.journalArchive(context)
        var announce = false
        if (failure != null) {
            if (standing.getString(key, null) != failure) {
                standing.edit().putString(key, failure).apply()
                announce = true
                FileesSession.pushJournal(prefs, repoName, failureSentence(context, failure), archive = archive)
            }
        } else if (standing.contains(key)) {
            standing.edit().remove(key).apply()
            NotificationManagerCompat.from(context).cancel(NOTIFICATION_FAIL_ID)
        }
        if (sent > 0) {
            FileesSession.pushJournal(
                prefs,
                repoName,
                context.resources.getQuantityString(R.plurals.journal_watch_sent, sent, sent),
                archive = archive,
            )
        }
        if (waiting > 0 && (failure == null || announce)) {
            FileesSession.pushJournal(
                prefs,
                repoName,
                context.resources.getQuantityString(R.plurals.journal_watch_waiting, waiting, waiting),
                archive = archive,
            )
        }
        return announce
    }

    // The catalog sentence alone hid the real cause live, 2026-09-26: a
    // status-70 dispatch failure got bucketed under a generic "server does
    // not accept this yet" entry (an needle match on the operation name, not
    // the true reason), and the journal - unlike the interactive transport
    // error dialog - never showed the raw text underneath it. Appending raw
    // here mirrors what showTransportError already does for user-triggered
    // sends, so a background failure is diagnosable from the app alone.
    private fun failureSentence(context: Context, raw: String): String {
        mobileRecoveryMessage(context,raw)?.let { return it }
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

    private fun notifyProgress(context: Context, text: String) {
        notifyMessage(context, context.getString(R.string.watch_state_sending), text, NOTIFICATION_PROGRESS_ID, onlyAlertOnce = true)
    }

    private fun notifyMessage(context: Context, title: String, text: String, id: Int, onlyAlertOnce: Boolean = false) {
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
            .setContentIntent(android.app.PendingIntent.getActivity(context,0,android.content.Intent(context,MainActivity::class.java),
                android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE))
            .setAutoCancel(true)
            .setOnlyAlertOnce(onlyAlertOnce)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        NotificationManagerCompat.from(context).notify(id, notification)
    }
}
