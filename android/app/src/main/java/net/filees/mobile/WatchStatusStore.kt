package net.filees.mobile

import android.content.Context
import android.net.Uri
import android.provider.DocumentsContract
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/** Local presentation of capture facts, scoped to server + destination + SAF tree.
 * Queue receipts remain authoritative. An interrupted run is never complete. */
class WatchStatusStore(context: Context) {
    val prefs = context.getSharedPreferences("filees_watch_status", Context.MODE_PRIVATE)
    private val context = context.applicationContext
    data class State(val phase: String, val checked: Long, val waiting: Int, val errors: List<String>, val queue: List<String>, val events: List<String>, val stopReason: Int)
    companion object {
        // Local pause reason, not a JobScheduler stop code.
        const val START_NOT_ALLOWED = -1001
        private val guard = Any()
        private val active = java.util.concurrent.ConcurrentHashMap.newKeySet<String>()
        private val busy = setOf("checking", "preparing", "sending")
        fun scope(context: Context): String {
            val p = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
            return (FileesSession.current(p)?.id ?: "") + ":" + p.getString(FileesSession.PREF_UPLOAD_REPO_ID, "")
        }
        fun belongs(source: String, tree: Uri): Boolean = try {
            val uri = Uri.parse(source.substringBeforeLast('/'))
            uri.authority == tree.authority && DocumentsContract.getTreeDocumentId(uri) == DocumentsContract.getTreeDocumentId(tree)
        } catch (_: Exception) { false }
    }
    private fun key(scope: String, tree: Uri) = "$scope|$tree"
    private fun read(scope: String, tree: Uri): JSONObject = runCatching { JSONObject(prefs.getString(key(scope,tree), "{}")!!) }.getOrDefault(JSONObject())
    private fun strings(array: JSONArray?): List<String> = if (array == null) emptyList() else (0 until array.length()).map { array.optString(it) }
    private fun write(scope: String, tree: Uri, mutate: (JSONObject) -> Unit) = synchronized(guard) {
        val record = read(scope,tree)
        mutate(record)
        // Tiny UI record: durable across process death, never used as a receipt.
        prefs.edit().putString(key(scope,tree),record.toString()).commit()
        Unit
    }
    fun begin(scope: String, trees: List<Uri>): String {
        val token = UUID.randomUUID().toString(); active.add(token)
        trees.forEach { tree -> write(scope,tree) { it.put("token",token).put("phase","checking").put("errors",JSONArray()).put("scanned",false).remove("stop_reason") } }
        return token
    }
    fun end(token: String) { active.remove(token) }
    fun phase(scope: String, tree: Uri, phase: String) = write(scope,tree) { it.put("phase",phase) }
    fun paused(scope: String, tree: Uri, reason: Int) = write(scope,tree) { it.put("phase","paused").put("stop_reason",reason) }
    fun scanned(scope: String, tree: Uri) = write(scope,tree) { it.put("scanned",true).put("checked",System.currentTimeMillis()) }
    fun problem(scope: String, tree: Uri, error: String) = write(scope,tree) {
        it.put("errors",JSONArray((strings(it.optJSONArray("errors")) + error).distinct().take(10))).put("phase","error")
    }
    fun updateQueue(scope: String, trees: List<Uri>, items: List<PendingUpload>, finish: Boolean = false) {
        val unassigned = items.any { !it.delivered && it.state != "discarded" && it.sources.isEmpty() }
        for (tree in trees) write(scope,tree) { record ->
            val pending = items.filter { !it.delivered && it.state != "discarded" && it.sources.any { source -> belongs(source,tree) } }
            val count = pending.sumOf { item -> item.sources.count { belongs(it,tree) }.coerceAtLeast(1) }
            val rows = pending.flatMap { item ->
                val suffix = when { item.needsDecision -> context.getString(R.string.watch_state_attention); item.state == "uploading" -> context.getString(R.string.watch_state_sending); else -> context.getString(R.string.watch_state_waiting) }
                item.sources.filter { belongs(it,tree) }.map { "${it.substringAfterLast('/')} · $suffix" + if(item.lastError.isBlank()) "" else "\n${mobileRecoveryMessage(context,item.lastError) ?: item.lastError}" }
            }.take(200)
            record.put("waiting",count).put("queue",JSONArray(rows))
            record.put("queue_errors",JSONArray(pending.filter { it.needsDecision || it.lastError.isNotBlank() }.map { mobileRecoveryMessage(context,it.lastError) ?: it.lastError.ifBlank { context.getString(R.string.watch_state_attention) } }.distinct().take(10)))
            if (finish || (record.optString("phase") !in busy && record.optString("phase") != "paused")) {
                val errors = strings(record.optJSONArray("errors")) + strings(record.optJSONArray("queue_errors"))
                val next = when { errors.isNotEmpty() -> "error"; count > 0 || unassigned -> "waiting"; record.optBoolean("scanned") -> "complete"; else -> "unknown" }
                if (finish) {
                    val events = strings(record.optJSONArray("events"))
                    val previous = record.optString("last_result")
                    val result = "$next:$count:${errors.joinToString()}"
                    if (previous != result) {
                        val line = "${java.text.DateFormat.getDateTimeInstance(java.text.DateFormat.SHORT,java.text.DateFormat.SHORT).format(java.util.Date())} · ${label(next)}" + if(errors.isEmpty()) "" else "\n${errors.first()}"
                        record.put("events",JSONArray((listOf(line)+events).take(30))).put("last_result",result)
                    }
                }
                record.put("phase",next)
            }
        }
        updateSummary(scope,items)
    }
    fun updateSummary(scope: String, items: List<PendingUpload>) {
        val pending = items.filter { !it.delivered && it.state != "discarded" }
        prefs.edit().putInt("$scope|pending",pending.sumOf { it.fileCount }).putBoolean("$scope|attention",pending.any { it.needsDecision || it.lastError.isNotBlank() }).apply()
    }
    fun state(scope: String, tree: Uri): State {
        val record = read(scope,tree)
        var phase = record.optString("phase","unknown")
        if (phase in busy && record.optString("token") !in active) phase = "waiting"
        return State(phase,record.optLong("checked"),record.optInt("waiting"),
            (strings(record.optJSONArray("errors"))+strings(record.optJSONArray("queue_errors"))).map { mobileRecoveryMessage(context,it) ?: it }.distinct(),strings(record.optJSONArray("queue")),strings(record.optJSONArray("events")),record.optInt("stop_reason",androidx.work.WorkInfo.STOP_REASON_NOT_STOPPED))
    }
    fun label(phase: String): String = context.getString(when(phase) {
        "idle" -> R.string.watch_state_idle
        "checking" -> R.string.watch_state_checking; "preparing" -> R.string.watch_state_preparing
        "sending" -> R.string.watch_state_sending; "complete" -> R.string.watch_state_complete
        "waiting" -> R.string.watch_state_waiting; "error" -> R.string.watch_state_attention
        "paused" -> R.string.watch_state_paused
        else -> R.string.watch_state_unknown
    })
    fun pauseDescription(reason: Int): String = context.getString(when(reason) {
        START_NOT_ALLOWED -> R.string.watch_pause_start
        androidx.work.WorkInfo.STOP_REASON_DEVICE_STATE -> R.string.watch_pause_device
        androidx.work.WorkInfo.STOP_REASON_TIMEOUT -> R.string.watch_pause_timeout
        androidx.work.WorkInfo.STOP_REASON_CONSTRAINT_CONNECTIVITY -> R.string.watch_pause_network
        else -> R.string.watch_pause_system
    })
    fun summary(scope: String, trees: List<Uri>): Pair<String,String> {
        val states = trees.map { state(scope,it) }
        val count = prefs.getInt("$scope|pending",0)
        val phase = listOf("sending","preparing","checking","paused","error","waiting","unknown").firstOrNull { p -> states.any { it.phase == p } }
            ?: if(prefs.getBoolean("$scope|attention",false)) "error" else if(count>0) "waiting" else if(states.isNotEmpty()) "complete" else "idle"
        return label(phase) to context.getString(R.string.watch_queue_count,count)
    }
}
