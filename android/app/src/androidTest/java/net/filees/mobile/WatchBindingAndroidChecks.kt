package net.filees.mobile

import android.content.Context
import android.content.ContextWrapper
import android.content.SharedPreferences
import android.net.Uri

/** Adapter acceptance on Android; isolated preferences, no network or SAF reads. */
internal object WatchBindingAndroidChecks {
    fun run(base: Context) {
        val stores = mutableListOf<SharedPreferences>()
        val prefix = "watch-binding-test-${System.nanoTime()}-"
        val context = object : ContextWrapper(base) {
            override fun getSharedPreferences(name: String, mode: Int): SharedPreferences =
                base.getSharedPreferences(prefix + name, mode).also { if (it !in stores) stores.add(it) }
        }
        try {
            val session = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
            val storage = context.getSharedPreferences("filees_watched", Context.MODE_PRIVATE)
            val tree = Uri.parse("content://fixture/tree/camera")
            check(storage.edit().putString("trees", org.json.JSONArray(listOf(tree.toString())).toString())
                .putBoolean("seen_source", true).commit())
            val a = FileesSession.putAndSelect(session, "A.invalid:22", "key-A")
            check(FileesSession.setUploadTarget(session, "repo", "Repo A", a.id))
            val boundA = WatchedFolders(context)
            check(boundA.uris().isEmpty() && boundA.unassigned() == listOf(tree))
            check(!boundA.alreadySeen("source", "repo"))
            boundA.add(tree)
            boundA.markSeen("source", "repo")
            val b = FileesSession.putAndSelect(session, "B.invalid:22", "key-B")
            check(!FileesSession.setUploadTarget(session, "wrong-repo", "Stale dialog", a.id))
            check(FileesSession.current(session)?.id == b.id && FileesSession.current(session)?.uploadRepoId == "")
            val boundB = WatchedFolders(context)
            check(boundB.uris().isEmpty() && !boundB.alreadySeen("source", "repo"))
            check(boundA.uris() == listOf(tree) && boundA.alreadySeen("source", "repo"))
            boundB.add(tree); boundB.remove(tree)
            FileesSession.select(session, a.id)
            val reopened = WatchedFolders(context)
            check(reopened.uris() == listOf(tree) && reopened.unassigned().isEmpty())
            check(reopened.alreadySeen("source", "repo") && !reopened.alreadySeen("source", "other-repo"))
            FileesSession.unpairId(session, a.id)
            val newA = FileesSession.putAndSelect(session, "A.invalid:22", "key-A")
            check(newA.id != a.id && WatchedFolders(context).uris().isEmpty())
        } finally { stores.forEach { check(it.edit().clear().commit()) } }
    }
}
