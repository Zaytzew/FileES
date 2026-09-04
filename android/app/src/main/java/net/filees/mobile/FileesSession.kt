package net.filees.mobile

import android.content.SharedPreferences
import org.json.JSONArray
import org.json.JSONObject
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
    const val MOBILE_USER = "_filees-mobile"

    private const val PREF_SERVERS = "servers_json"
    private const val PREF_CURRENT_ID = "current_server_id"

    fun migrate(prefs: SharedPreferences) {
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

    fun servers(prefs: SharedPreferences): List<PairedServer> {
        migrate(prefs)
        val raw = prefs.getString(PREF_SERVERS, "[]") ?: "[]"
        val array = JSONArray(raw)
        val out = ArrayList<PairedServer>(array.length())
        for (i in 0 until array.length()) {
            PairedServer.fromJson(array.getJSONObject(i))?.let { out.add(it) }
        }
        return out
    }

    fun current(prefs: SharedPreferences): PairedServer? {
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

    fun putAndSelect(prefs: SharedPreferences, address: String, hostKey: String): PairedServer {
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

    fun select(prefs: SharedPreferences, id: String) {
        val all = servers(prefs)
        if (all.none { it.id == id }) return
        write(prefs, all, id)
    }

    fun rememberProjection(prefs: SharedPreferences, projection: RealmProjection) {
        val cur = current(prefs) ?: return
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
            ),
        )
    }

    fun setSelectedRepo(prefs: SharedPreferences, repoId: String?) {
        val cur = current(prefs) ?: return
        replace(prefs, cur.copy(selectedRepoId = repoId.orEmpty()))
    }

    fun setUploadTarget(prefs: SharedPreferences, repoId: String, repoName: String) {
        val cur = current(prefs) ?: return
        replace(prefs, cur.copy(uploadRepoId = repoId, uploadRepoName = repoName))
    }

    // Forget the active server only. Device identity stays; other pairings stay.
    fun unpair(prefs: SharedPreferences) {
        val cur = current(prefs) ?: run {
            prefs.edit().clear().apply()
            return
        }
        val rest = servers(prefs).filterNot { it.id == cur.id }
        write(prefs, rest, rest.firstOrNull()?.id)
    }

    fun unpairId(prefs: SharedPreferences, id: String) {
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
