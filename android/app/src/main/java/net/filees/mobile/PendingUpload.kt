package net.filees.mobile

import org.json.JSONArray
import org.json.JSONObject

/**
 * Mirrors pkg/mobileclient.PendingUpload's JSON shape exactly (see
 * DrainPendingJSON/ListUploadsJSON in pkg/mobileclient/androidbind). Kept as
 * a plain data holder -- the state machine itself lives entirely in Go;
 * this side only ever displays it and asks for a discard.
 */
data class PendingUpload(
    val id: String,
    val parentPath: String,
    val filename: String,
    val size: Long,
    val state: String,
    val outcome: String,
    val existingSha256: String,
    val lastError: String,
) {
    /** conflict/parked need an explicit user decision -- never auto-resolved
     * (concept doc §6.4, §9.3, §10.2). Only these get a Discard action. */
    val needsDecision: Boolean
        get() = state == "conflict" || state == "parked"

    companion object {
        fun listFromJson(json: String): List<PendingUpload> {
            if (json.isBlank() || json == "null") return emptyList()
            val array = JSONArray(json)
            return (0 until array.length()).map { i ->
                val o: JSONObject = array.getJSONObject(i)
                PendingUpload(
                    id = o.optString("id"),
                    parentPath = o.optString("parent_path"),
                    filename = o.optString("filename"),
                    size = o.optLong("size"),
                    state = o.optString("state"),
                    outcome = o.optString("outcome"),
                    existingSha256 = o.optString("existing_sha256"),
                    lastError = o.optString("last_error"),
                )
            }
        }
    }
}
