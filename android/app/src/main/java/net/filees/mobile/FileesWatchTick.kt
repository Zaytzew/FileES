package net.filees.mobile

import android.content.Context
import androidbind.Androidbind

/**
 * One scan of the operator-chosen Android locations. New objects go to
 * mobile-uploads/ and are drained. Used by the 15-minute WorkManager tick
 * and by the foreground activity when it comes back.
 */
object FileesWatchTick {
    fun run(context: Context): Int {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null) ?: return 0
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null) ?: return 0
        val repoId = prefs.getString(FileesSession.PREF_REPO_ID, null) ?: return 0
        if (address.isBlank() || hostKey.isBlank() || repoId.isBlank()) return 0

        val watched = WatchedFolders(context)
        val trees = watched.uris()
        if (trees.isEmpty()) return 0

        val client = Androidbind.newClient(context.filesDir.absolutePath, address, FileesSession.MOBILE_USER, hostKey)
        var queued = 0
        for (tree in trees) {
            for (file in DocumentWalk.tree(context.contentResolver, tree)) {
                val seen = file.uri.toString() + "/" + file.filename
                if (watched.alreadySeen(seen)) continue
                val bytes = context.contentResolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
                client.enqueueUpload(repoId, UploadPaths.parent(file.relativeDir), file.filename, file.contentType, bytes)
                watched.markSeen(seen)
                queued++
            }
        }
        if (queued > 0) {
            client.drainPendingJSON(repoId)
        }
        return queued
    }
}
