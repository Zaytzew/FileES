package net.filees.mobile

import android.content.Context
import android.content.SharedPreferences
import android.net.Uri
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.Observer
import androidx.work.WorkInfo
import androidx.work.WorkManager

object WatchFolderCard {
    fun show(context: Context, tree: Uri) {
        val store = WatchStatusStore(context)
        val scope = WatchStatusStore.scope(context)
        var scheduled = emptyList<WorkInfo>()
        val work = WorkManager.getInstance(context).getWorkInfosByTagLiveData(FileesWatchWorker::class.java.name)
        val body = TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Filees_Body)
            setTextIsSelectable(true)
            val pad = context.resources.getDimensionPixelSize(R.dimen.filees_gutter)
            setPadding(pad,pad,pad,pad)
        }
        fun render() {
            val state = store.state(scope,tree)
            val pending = scheduled.filter { it.state == WorkInfo.State.ENQUEUED }
                .minByOrNull { it.nextScheduleTimeMillis }
            val stopped = android.os.Build.VERSION.SDK_INT >= 31 && pending != null && pending.runAttemptCount > 0 &&
                pending.stopReason != WorkInfo.STOP_REASON_NOT_STOPPED
            val busy = state.phase in setOf("checking","preparing","sending")
            val phase = if (!busy && stopped) "paused" else state.phase
            val lines = mutableListOf(store.label(phase),context.getString(R.string.watch_queue_count,state.waiting))
            if (phase == "paused") lines += store.pauseDescription(if(stopped) pending!!.stopReason else state.stopReason)
            if (!busy && state.waiting > 0 && pending != null) {
                val earliest = pending.nextScheduleTimeMillis
                lines += if (earliest > System.currentTimeMillis() && earliest != Long.MAX_VALUE)
                    context.getString(R.string.watch_retry_earliest,java.text.DateFormat.getTimeInstance().format(java.util.Date(earliest)))
                else context.getString(R.string.watch_retry_scheduled)
            }
            if (state.checked > 0) lines += context.getString(R.string.watch_last_checked,java.text.DateFormat.getDateTimeInstance().format(java.util.Date(state.checked)))
            lines += context.getString(R.string.watch_scope_note)
            lines += state.errors
            lines += "\n${context.getString(R.string.watch_queue_title)}"
            lines += state.queue.ifEmpty { listOf(context.getString(R.string.watch_queue_empty)) }
            if (state.waiting > 200) lines += context.getString(R.string.watch_queue_more)
            lines += "\n${context.getString(R.string.watch_journal_title)}"
            lines += state.events
            body.text = lines.joinToString("\n\n")
        }
        val observer = Observer<List<WorkInfo>> { scheduled = it; render() }
        val listener = SharedPreferences.OnSharedPreferenceChangeListener { _, _ -> body.post { render() } }
        val dialog = AlertDialog.Builder(context).setTitle(tree.lastPathSegment ?: tree.toString())
            .setView(ScrollView(context).apply { addView(body) })
            .setPositiveButton(R.string.action_close,null)
            .setOnDismissListener { store.prefs.unregisterOnSharedPreferenceChangeListener(listener); work.removeObserver(observer) }.create()
        render(); store.prefs.registerOnSharedPreferenceChangeListener(listener); dialog.show()
        (context as? LifecycleOwner)?.let { work.observe(it,observer) }
    }
}
