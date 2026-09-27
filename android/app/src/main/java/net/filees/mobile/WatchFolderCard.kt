package net.filees.mobile

import android.content.Context
import android.content.SharedPreferences
import android.net.Uri
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AlertDialog

object WatchFolderCard {
    fun show(context: Context, tree: Uri) {
        val store = WatchStatusStore(context)
        val scope = WatchStatusStore.scope(context)
        val body = TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Filees_Body)
            setTextIsSelectable(true)
            val pad = context.resources.getDimensionPixelSize(R.dimen.filees_gutter)
            setPadding(pad,pad,pad,pad)
        }
        fun render() {
            val state = store.state(scope,tree)
            val lines = mutableListOf(store.label(state.phase),context.getString(R.string.watch_queue_count,state.waiting))
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
        val listener = SharedPreferences.OnSharedPreferenceChangeListener { _, _ -> body.post { render() } }
        val dialog = AlertDialog.Builder(context).setTitle(tree.lastPathSegment ?: tree.toString())
            .setView(ScrollView(context).apply { addView(body) })
            .setPositiveButton(R.string.action_close,null)
            .setOnDismissListener { store.prefs.unregisterOnSharedPreferenceChangeListener(listener) }.create()
        render(); store.prefs.registerOnSharedPreferenceChangeListener(listener); dialog.show()
    }
}
