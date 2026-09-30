package net.filees.mobile

import android.content.Context
import android.content.SharedPreferences
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.UUID

data class PairedServer(
    val id: String,
    val address: String,
    val hostKey: String,
    val displayName: String = "",
    val realmAlias: String = "",
    val generatedAt: String = "",
    val selectedRepoId: String = "",
    val uploadRepoId: String = "",
    val uploadRepoName: String = "",
    val leadingColor: String = "",
) {
    fun label(): String {
        val name = displayName.ifBlank { address }
        return if (realmAlias.isNotBlank()) "$name · $realmAlias" else name
    }

    fun toJson(): JSONObject = JSONObject()
        .put("id", id)
        .put("address", address)
        .put("host_key", hostKey)
        .put("display_name", displayName)
        .put("realm_alias", realmAlias)
        .put("generated_at", generatedAt)
        .put("selected_repo_id", selectedRepoId)
        .put("upload_repo_id", uploadRepoId)
        .put("upload_repo_name", uploadRepoName)
        .put("leading_color", leadingColor)

    companion object {
        fun fromJson(o: JSONObject): PairedServer? {
            val id = o.optString("id")
            val address = o.optString("address")
            val hostKey = o.optString("host_key")
            if (id.isBlank() || address.isBlank() || hostKey.isBlank()) return null
            return PairedServer(
                id = id,
                address = address,
                hostKey = hostKey,
                displayName = o.optString("display_name"),
                realmAlias = o.optString("realm_alias"),
                generatedAt = o.optString("generated_at"),
                selectedRepoId = o.optString("selected_repo_id"),
                uploadRepoId = o.optString("upload_repo_id"),
                uploadRepoName = o.optString("upload_repo_name"),
                leadingColor = o.optString("leading_color"),
            )
        }
    }
}

object FileesSession {
    const val PREFS = "filees_connection"
    const val PREF_ADDRESS = "address"
    const val PREF_HOST_KEY = "host_public_key"
    const val PREF_REPO_ID = "selected_repo_id"
    // Deliberately separate from PREF_REPO_ID, which tracks "which repo is
    // currently open in the browser" and is CLEARED on navigating back to
    // the top-level list (MainActivity#goUp) - the watched-folder upload
    // target must survive that, or every tick after leaving a repository
    // silently no-ops (found live: added a watched folder, took a photo,
    // nothing happened, no explanation).
    const val PREF_UPLOAD_REPO_ID = "watch_upload_repo_id"
    const val PREF_UPLOAD_REPO_NAME = "watch_upload_repo_name"
    const val PREF_DETAILS = "last_details"
    const val PREF_SERVER_DISPLAY_NAME = "server_display_name"
    const val PREF_REALM_ALIAS = "realm_alias"
    const val PREF_VIEW_GENERATED_AT = "view_generated_at"
    const val PREF_ACKED_SHOUTS = "acked_shouts"
    private const val PREF_JOURNAL = "phone_journal_json"
    private const val JOURNAL_CAP = 12
    private const val JOURNAL_KEEP_MIN = 3
    private const val JOURNAL_MAX_AGE_MS = 48L * 60 * 60 * 1000
    private const val JOURNAL_ARCHIVE = "journal-archive.jsonl"
    private const val JOURNAL_ARCHIVE_MAX_BYTES = 512L * 1024
    private val JOURNAL_LOCK = Any()
    const val MOBILE_USER = "_filees-mobile"

    private const val PREF_SERVERS = "servers_json"
    private const val PREF_CURRENT_ID = "current_server_id"

    @Synchronized fun migrate(prefs: SharedPreferences) {
        if (prefs.contains(PREF_SERVERS)) return
        val address = prefs.getString(PREF_ADDRESS, null) ?: return
        val hostKey = prefs.getString(PREF_HOST_KEY, null) ?: return
        if (address.isBlank() || hostKey.isBlank()) return
        val server = PairedServer(
            id = UUID.randomUUID().toString(),
            address = address,
            hostKey = hostKey,
            displayName = prefs.getString(PREF_SERVER_DISPLAY_NAME, "") ?: "",
            realmAlias = prefs.getString(PREF_REALM_ALIAS, "") ?: "",
            generatedAt = prefs.getString(PREF_VIEW_GENERATED_AT, "") ?: "",
            selectedRepoId = prefs.getString(PREF_REPO_ID, "") ?: "",
            uploadRepoId = prefs.getString(PREF_UPLOAD_REPO_ID, "") ?: "",
            uploadRepoName = prefs.getString(PREF_UPLOAD_REPO_NAME, "") ?: "",
        )
        write(prefs, listOf(server), server.id)
    }

    @Synchronized fun servers(prefs: SharedPreferences): List<PairedServer> {
        migrate(prefs)
        val raw = prefs.getString(PREF_SERVERS, "[]") ?: "[]"
        val array = JSONArray(raw)
        val out = ArrayList<PairedServer>(array.length())
        for (i in 0 until array.length()) {
            PairedServer.fromJson(array.getJSONObject(i))?.let { out.add(it) }
        }
        return out
    }

    @Synchronized fun current(prefs: SharedPreferences): PairedServer? {
        val all = servers(prefs)
        if (all.isEmpty()) return null
        val id = prefs.getString(PREF_CURRENT_ID, null)
        return all.firstOrNull { it.id == id } ?: all.first()
    }

    fun serverLabel(prefs: SharedPreferences): String {
        val cur = current(prefs) ?: return ""
        return cur.displayName.ifBlank { cur.address }
    }

    fun barLabel(prefs: SharedPreferences): String {
        val cur = current(prefs) ?: return ""
        val label = cur.label()
        return if (servers(prefs).size > 1) "$label ▾" else label
    }

    @Synchronized fun putAndSelect(prefs: SharedPreferences, address: String, hostKey: String): PairedServer {
        val list = servers(prefs).toMutableList()
        val index = list.indexOfFirst { it.address == address }
        val server = if (index >= 0) {
            list[index].copy(hostKey = hostKey)
        } else {
            PairedServer(id = UUID.randomUUID().toString(), address = address, hostKey = hostKey)
        }
        if (index >= 0) list[index] = server else list.add(server)
        write(prefs, list, server.id)
        return server
    }

    @Synchronized fun select(prefs: SharedPreferences, id: String) {
        val all = servers(prefs)
        if (all.none { it.id == id }) return
        write(prefs, all, id)
    }

    @Synchronized fun rememberProjection(prefs: SharedPreferences, projection: RealmProjection, expectedServerId: String? = null) {
        val cur = current(prefs) ?: return
        if (expectedServerId != null && cur.id != expectedServerId) return
        val uploadOk = cur.uploadRepoId.isBlank() ||
            projection.shares.any { it.repoId == cur.uploadRepoId && it.canCapture }
        replace(
            prefs,
            cur.copy(
                displayName = projection.serverDisplayName.ifBlank { cur.displayName },
                realmAlias = projection.realmAlias.ifBlank { cur.realmAlias },
                generatedAt = projection.generatedAt.ifBlank { cur.generatedAt },
                uploadRepoId = if (uploadOk) cur.uploadRepoId else "",
                uploadRepoName = if (uploadOk) cur.uploadRepoName else "",
                leadingColor = projection.leadingColor.ifBlank { cur.leadingColor },
            ),
        )
    }

    @Synchronized fun setSelectedRepo(prefs: SharedPreferences, repoId: String?) {
        val cur = current(prefs) ?: return
        replace(prefs, cur.copy(selectedRepoId = repoId.orEmpty()))
    }

    @Synchronized fun setUploadTarget(prefs: SharedPreferences, repoId: String, repoName: String, expectedServerId: String? = null): Boolean {
        val cur = current(prefs) ?: return false
        if (expectedServerId != null && cur.id != expectedServerId) return false
        replace(prefs, cur.copy(uploadRepoId = repoId, uploadRepoName = repoName))
        return true
    }

    // Forget the active server only. Device identity stays; other pairings stay.
    @Synchronized fun unpair(prefs: SharedPreferences) {
        val cur = current(prefs) ?: run {
            prefs.edit().clear().apply()
            return
        }
        val rest = servers(prefs).filterNot { it.id == cur.id }
        write(prefs, rest, rest.firstOrNull()?.id)
    }

    @Synchronized fun unpairId(prefs: SharedPreferences, id: String) {
        val rest = servers(prefs).filterNot { it.id == id }
        val next = if (prefs.getString(PREF_CURRENT_ID, null) == id) rest.firstOrNull()?.id else prefs.getString(PREF_CURRENT_ID, null)
        write(prefs, rest, next)
    }

    fun shoutId(repoId: String, revision: Long): String = "shout:$repoId:$revision"

    fun isShoutAcked(prefs: SharedPreferences, id: String): Boolean {
        val array = JSONArray(prefs.getString(PREF_ACKED_SHOUTS, "[]") ?: "[]")
        for (i in 0 until array.length()) {
            if (array.optString(i) == id) return true
        }
        return false
    }

    fun ackShouts(prefs: SharedPreferences, ids: List<String>) {
        if (ids.isEmpty()) return
        val array = JSONArray(prefs.getString(PREF_ACKED_SHOUTS, "[]") ?: "[]")
        val have = HashSet<String>()
        for (i in 0 until array.length()) {
            have.add(array.optString(i))
        }
        have.addAll(ids)
        val next = JSONArray()
        have.forEach { next.put(it) }
        prefs.edit().putString(PREF_ACKED_SHOUTS, next.toString()).apply()
    }

    data class PhoneJournalEntry(
        val at: Long,
        val scope: String,
        val entry: String,
    )

    /** Adds an entry, then retires old ones (see [retireJournal]). */
    fun pushJournal(prefs: SharedPreferences, scope: String, entry: String, shoutId: String = "", archive: File? = null) {
        synchronized(JOURNAL_LOCK) {
            val prev = JSONArray(prefs.getString(PREF_JOURNAL, "[]") ?: "[]")
            if (shoutId.isNotEmpty()) {
                for (i in 0 until prev.length()) {
                    if (prev.getJSONObject(i).optString("shout_id") == shoutId) return
                }
            }
            val all = ArrayList<JSONObject>()
            val row = JSONObject()
                .put("at", System.currentTimeMillis())
                .put("scope", scope)
                .put("entry", entry)
            if (shoutId.isNotEmpty()) row.put("shout_id", shoutId)
            all.add(row)
            val seen = HashSet<String>()
            if (shoutId.isNotEmpty()) seen.add(shoutId)
            for (i in 0 until prev.length()) {
                val o = prev.getJSONObject(i)
                val id = o.optString("shout_id")
                if (id.isNotEmpty() && !seen.add(id)) continue
                all.add(o)
            }
            retire(prefs, all, archive)
        }
    }

    /** Applies the retention rule without adding an entry; entries also age out while the app is idle. */
    fun sweepJournal(prefs: SharedPreferences, archive: File?) {
        synchronized(JOURNAL_LOCK) {
            val array = JSONArray(prefs.getString(PREF_JOURNAL, "[]") ?: "[]")
            retire(prefs, (0 until array.length()).map { array.getJSONObject(it) }, archive)
        }
    }

    fun journalArchive(context: Context): File = File(context.filesDir, JOURNAL_ARCHIVE)

    // Newest first. Always keep the JOURNAL_KEEP_MIN newest; beyond them an entry
    // stays only while younger than JOURNAL_MAX_AGE_MS, and never past JOURNAL_CAP.
    // Retired entries are appended to the local archive first; if that write fails
    // the aged ones stay (still bounded by the cap) rather than being lost.
    private fun retire(prefs: SharedPreferences, all: List<JSONObject>, archive: File?) {
        val now = System.currentTimeMillis()
        val keep = ArrayList<JSONObject>()
        val retired = ArrayList<JSONObject>()
        val aged = ArrayList<JSONObject>()
        all.forEachIndexed { i, o ->
            when {
                i >= JOURNAL_CAP -> retired.add(o)
                i >= JOURNAL_KEEP_MIN && now - o.optLong("at") > JOURNAL_MAX_AGE_MS -> aged.add(o)
                else -> keep.add(o)
            }
        }
        if (aged.isNotEmpty() || retired.isNotEmpty()) {
            if (archive != null && appendArchive(archive, aged + retired, now)) {
                // archived: drop them from the live journal
            } else {
                keep.addAll(aged)
                keep.sortByDescending { it.optLong("at") }
            }
        }
        val next = JSONArray()
        keep.forEach { next.put(it) }
        prefs.edit().putString(PREF_JOURNAL, next.toString()).apply()
    }

    private fun appendArchive(file: File, entries: List<JSONObject>, now: Long): Boolean = try {
        // One line per entry. One rotated generation keeps the archive itself bounded.
        if (file.length() > JOURNAL_ARCHIVE_MAX_BYTES) {
            val old = File(file.path + ".1")
            old.delete()
            file.renameTo(old)
        }
        file.appendText(entries.joinToString("") { JSONObject(it.toString()).put("archived_at", now).toString() + "\n" })
        true
    } catch (_: Exception) { false }

    fun journal(prefs: SharedPreferences): List<PhoneJournalEntry> {
        val array = JSONArray(prefs.getString(PREF_JOURNAL, "[]") ?: "[]")
        val seen = HashSet<String>()
        val out = ArrayList<PhoneJournalEntry>(array.length())
        for (i in 0 until array.length()) {
            val o = array.getJSONObject(i)
            val id = o.optString("shout_id")
            if (id.isNotEmpty() && !seen.add(id)) continue
            out.add(
                PhoneJournalEntry(
                    at = o.optLong("at"),
                    scope = o.optString("scope"),
                    entry = o.optString("entry"),
                ),
            )
        }
        return out
    }

    private fun replace(prefs: SharedPreferences, server: PairedServer) {
        val list = servers(prefs).map { if (it.id == server.id) server else it }
        write(prefs, list, server.id)
    }

    private fun write(prefs: SharedPreferences, list: List<PairedServer>, currentId: String?) {
        val array = JSONArray()
        list.forEach { array.put(it.toJson()) }
        val editor = prefs.edit()
        editor.putString(PREF_SERVERS, array.toString())
        val cur = list.firstOrNull { it.id == currentId } ?: list.firstOrNull()
        if (cur == null) {
            editor.remove(PREF_CURRENT_ID)
            editor.remove(PREF_ADDRESS)
            editor.remove(PREF_HOST_KEY)
            editor.remove(PREF_SERVER_DISPLAY_NAME)
            editor.remove(PREF_REALM_ALIAS)
            editor.remove(PREF_VIEW_GENERATED_AT)
            editor.remove(PREF_REPO_ID)
            editor.remove(PREF_UPLOAD_REPO_ID)
            editor.remove(PREF_UPLOAD_REPO_NAME)
        } else {
            editor.putString(PREF_CURRENT_ID, cur.id)
            editor.putString(PREF_ADDRESS, cur.address)
            editor.putString(PREF_HOST_KEY, cur.hostKey)
            editor.putString(PREF_SERVER_DISPLAY_NAME, cur.displayName)
            editor.putString(PREF_REALM_ALIAS, cur.realmAlias)
            editor.putString(PREF_VIEW_GENERATED_AT, cur.generatedAt)
            editor.putString(PREF_REPO_ID, cur.selectedRepoId)
            editor.putString(PREF_UPLOAD_REPO_ID, cur.uploadRepoId)
            editor.putString(PREF_UPLOAD_REPO_NAME, cur.uploadRepoName)
        }
        editor.apply()
    }
}
