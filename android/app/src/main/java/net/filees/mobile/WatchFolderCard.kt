package net.filees.mobile

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.RadioGroup
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import com.google.android.material.radiobutton.MaterialRadioButton

/** One folder: send range, stop watching, and a short journal extract. */
object WatchFolderCard {
    interface Actions {
        fun depth(): WatchDepth?
        fun label(depth: WatchDepth): String
        fun apply(depth: WatchDepth, done: (Boolean) -> Unit)
        fun recent(): List<String>
        fun unwatch()
    }

    fun show(context: Context, title: String, actions: Actions) {
        val view = android.view.LayoutInflater.from(context).inflate(R.layout.dialog_watch_folder, null)
        val group = view.findViewById<RadioGroup>(R.id.groupWatchDepth)
        val unwatch = view.findViewById<View>(R.id.buttonUnwatch)
        val recent = view.findViewById<LinearLayout>(R.id.listWatchRecent)
        val ids = linkedMapOf<WatchDepth, Int>()
        for (depth in WatchDepth.entries) {
            val id = View.generateViewId()
            ids[depth] = id
            group.addView(MaterialRadioButton(context).apply {
                this.id = id
                text = actions.label(depth)
                minHeight = context.resources.getDimensionPixelSize(R.dimen.filees_touch_min)
                isAllCaps = false
                setTextAppearance(R.style.TextAppearance_Filees_Body)
                layoutParams = RadioGroup.LayoutParams(
                    RadioGroup.LayoutParams.MATCH_PARENT,
                    RadioGroup.LayoutParams.WRAP_CONTENT,
                )
            })
        }
        val lines = actions.recent()
        if (lines.isEmpty()) {
            recent.addView(meta(context, context.getString(R.string.watch_card_recent_empty)))
        } else {
            lines.forEachIndexed { index, line ->
                if (index > 0) recent.addView(hairline(context))
                recent.addView(meta(context, line).apply {
                    setTextAppearance(R.style.TextAppearance_Filees_Body)
                    setPadding(0, context.resources.getDimensionPixelSize(R.dimen.filees_space), 0, context.resources.getDimensionPixelSize(R.dimen.filees_space))
                })
            }
        }
        var suppress = false
        var generation = 0
        fun select(depth: WatchDepth?) {
            suppress = true
            if (depth == null) group.clearCheck() else group.check(ids.getValue(depth))
            suppress = false
        }
        fun busy(value: Boolean) {
            for (i in 0 until group.childCount) group.getChildAt(i).isEnabled = !value
            unwatch.isEnabled = !value
        }
        select(actions.depth())
        val dialog = AlertDialog.Builder(context)
            .setTitle(title)
            .setView(view)
            .setPositiveButton(R.string.action_close, null)
            .create()
        group.setOnCheckedChangeListener { _, checkedId ->
            if (suppress) return@setOnCheckedChangeListener
            val depth = ids.entries.firstOrNull { it.value == checkedId }?.key ?: return@setOnCheckedChangeListener
            val previous = actions.depth()
            if (depth == previous) return@setOnCheckedChangeListener
            val ticket = ++generation
            busy(true)
            actions.apply(depth) done@{ ok ->
                if (!dialog.isShowing || ticket != generation) return@done
                busy(false)
                if (!ok) select(previous) else select(depth)
            }
        }
        unwatch.setOnClickListener {
            dialog.dismiss()
            actions.unwatch()
        }
        dialog.show()
    }

    private fun meta(context: Context, value: String) = TextView(context).apply {
        setTextAppearance(R.style.TextAppearance_Filees_Meta)
        text = value
    }

    private fun hairline(context: Context) = View(context).apply {
        layoutParams = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, context.resources.getDimensionPixelSize(R.dimen.filees_hairline))
        setBackgroundColor(context.getColor(R.color.filees_line))
    }
}
