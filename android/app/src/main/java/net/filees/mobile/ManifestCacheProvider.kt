package net.filees.mobile

import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import androidbind.Store
import org.json.JSONObject

/**
 * Read-only content:// surface over the manifest cache written by
 * [pkg/mobileclient.Store] via the gomobile facade in
 * [pkg/mobileclient/androidbind]. It never talks to the network itself --
 * that is [androidbind.Client]'s job (run from a WorkManager job, not here).
 *
 * URI shape: content://net.filees.mobile.manifest/<repo_id>. Querying an
 * unknown or not-yet-cached repo_id returns an empty cursor, not an error --
 * concept doc §10.3: the GUI must be able to show "no cached state yet"
 * without treating that as a failure.
 */
class ManifestCacheProvider : ContentProvider() {

    private val columns = arrayOf(
        "path",
        "kind",
        "size",
        "last_changed_revision",
        "content_hash",
    )

    private lateinit var store: Store

    override fun onCreate(): Boolean {
        val context = context ?: return false
        // filesDir, never cacheDir: the whole point of the local store is
        // that it survives memory pressure (concept doc §9.2).
        store = Store(context.filesDir.absolutePath)
        return true
    }

    override fun query(
        uri: Uri,
        projection: Array<out String>?,
        selection: String?,
        selectionArgs: Array<out String>?,
        sortOrder: String?,
    ): Cursor? {
        val repoId = uri.lastPathSegment ?: return MatrixCursor(columns)
        val manifestJson = store.loadManifestJSON(repoId)
        val cursor = MatrixCursor(columns)
        if (manifestJson.isNullOrEmpty()) {
            return cursor
        }
        val manifest = JSONObject(manifestJson)
        val entries = manifest.optJSONArray("entries") ?: return cursor
        for (i in 0 until entries.length()) {
            val entry = entries.getJSONObject(i)
            cursor.addRow(
                arrayOf(
                    entry.optString("path"),
                    entry.optString("kind"),
                    entry.optLong("size"),
                    entry.optLong("last_changed_revision"),
                    if (entry.isNull("content_hash")) null else entry.optString("content_hash"),
                ),
            )
        }
        return cursor
    }

    // The manifest cache is written only by Client.Refresh over the mobile
    // protocol (REFRESH_MANIFEST) -- this provider is read-only by design.
    override fun getType(uri: Uri): String? = null

    override fun insert(uri: Uri, values: ContentValues?): Uri? =
        throw UnsupportedOperationException("manifest cache is read-only")

    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int =
        throw UnsupportedOperationException("manifest cache is read-only")

    override fun update(
        uri: Uri,
        values: ContentValues?,
        selection: String?,
        selectionArgs: Array<out String>?,
    ): Int = throw UnsupportedOperationException("manifest cache is read-only")
}
