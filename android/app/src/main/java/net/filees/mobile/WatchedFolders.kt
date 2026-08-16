package net.filees.mobile

import android.content.Context
import android.net.Uri
import androidx.core.content.edit
import org.json.JSONArray

/** Persisted SAF trees that are scanned for auto-upload into mobile-uploads/. */
class WatchedFolders(context: Context) {
    private val prefs = context.getSharedPreferences("filees_watched", Context.MODE_PRIVATE)

    fun uris(): List<Uri> {
        val raw = prefs.getString(KEY_TREES, "[]") ?: "[]"
        val array = JSONArray(raw)
        return (0 until array.length()).mapNotNull { Uri.parse(array.optString(it)).takeIf { uri -> uri.scheme != null } }
    }

    fun add(uri: Uri) {
        val next = (uris() + uri).distinctBy { it.toString() }
        saveUris(next)
    }

    fun remove(uri: Uri) {
        saveUris(uris().filterNot { it.toString() == uri.toString() })
    }

    fun alreadySeen(key: String): Boolean = prefs.getBoolean(seenKey(key), false)

    fun markSeen(key: String) {
        prefs.edit { putBoolean(seenKey(key), true) }
    }

    fun seenKeyFor(uri: Uri, size: Long, modified: Long): String =
        "${uri}_${size}_$modified"

    private fun saveUris(uris: List<Uri>) {
        val array = JSONArray()
        uris.forEach { array.put(it.toString()) }
        prefs.edit { putString(KEY_TREES, array.toString()) }
    }

    companion object {
        private const val KEY_TREES = "trees"
        private fun seenKey(key: String) = "seen_$key"
    }
}
