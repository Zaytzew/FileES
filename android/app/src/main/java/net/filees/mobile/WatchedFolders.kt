package net.filees.mobile

import android.content.Context
import android.net.Uri
import org.json.JSONArray

/** A fixed server binding; never follows subsequent UI selection changes. */
class WatchedFolders(context: Context, val serverId: String? = FileesSession.current(
    context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE))?.id) {
    private val prefs = context.getSharedPreferences("filees_watched", Context.MODE_PRIVATE)
    private val registry = WatchRegistry(object : WatchRegistry.Store {
        override fun list(key: String): List<String> {
            val array = JSONArray(prefs.getString(key, "[]") ?: "[]")
            return (0 until array.length()).map { array.getString(it) }
        }
        override fun lists(values: Map<String, List<String>>) {
            val editor = prefs.edit()
            values.forEach { (key, list) -> editor.putString(key, JSONArray(list).toString()) }
            if (!editor.commit()) throw java.io.IOException("Cannot persist watch binding")
        }
        override fun flag(key: String): Boolean = prefs.getBoolean(key, false)
        override fun flag(key: String, value: Boolean) {
            if (!prefs.edit().putBoolean(key, value).commit()) throw java.io.IOException("Cannot persist capture receipt")
        }
    }, serverId)
    private fun uris(values: List<String>) = values.mapNotNull { value ->
        Uri.parse(value).takeIf { it.scheme != null }
    }
    fun uris(): List<Uri> = uris(registry.trees())
    fun unassigned(): List<Uri> = uris(registry.unassigned())
    fun add(uri: Uri) = registry.add(uri.toString())
    fun remove(uri: Uri) {
        registry.remove(uri.toString())
        forgetDepth(uri)
    }
    fun removeUnassigned(uri: Uri) = registry.removeUnassigned(uri.toString())
    fun alreadySeen(key: String, repoId: String): Boolean = registry.seen(repoId, key)
    fun markSeen(key: String, repoId: String) = registry.markSeen(repoId, key)

    /** Depth chosen for this tree. Folders added before the choice was stored return null. */
    fun depth(uri: Uri): WatchDepth? {
        val server = serverId ?: return null
        val raw = prefs.getString(depthKey(server, uri), null) ?: return null
        return runCatching { WatchDepth.valueOf(raw) }.getOrNull()
    }

    fun rememberDepth(uri: Uri, depth: WatchDepth) {
        val server = serverId ?: return
        if (!prefs.edit().putString(depthKey(server, uri), depth.name).commit()) {
            throw java.io.IOException("Cannot persist watch depth")
        }
    }

    private fun forgetDepth(uri: Uri) {
        val server = serverId ?: return
        prefs.edit().remove(depthKey(server, uri)).commit()
    }

    private fun depthKey(server: String, uri: Uri) = "depth-v2:$server\u0000$uri"
}
