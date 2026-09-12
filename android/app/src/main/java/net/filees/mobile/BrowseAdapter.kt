package net.filees.mobile

import android.text.format.DateUtils
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.TextView
import androidx.recyclerview.widget.RecyclerView
import com.google.android.material.button.MaterialButton

class BrowseAdapter(
    private val onOpen: (BrowseRow) -> Unit,
    private val onDownload: (BrowseRow) -> Unit,
) : RecyclerView.Adapter<RecyclerView.ViewHolder>() {

    private var rows: List<BrowseRow> = emptyList()

    fun submit(next: List<BrowseRow>) {
        rows = next
        notifyDataSetChanged()
    }

    override fun getItemViewType(position: Int): Int = when (rows[position].kind) {
        BrowseRow.Kind.HERO -> VIEW_HERO
        BrowseRow.Kind.METRICS -> VIEW_METRICS
        BrowseRow.Kind.SERVER -> VIEW_SERVER
        BrowseRow.Kind.FACTS -> VIEW_FACTS
        BrowseRow.Kind.JOURNAL_HEAD -> VIEW_JOURNAL_HEAD
        BrowseRow.Kind.JOURNAL -> VIEW_JOURNAL
        BrowseRow.Kind.HEADER -> VIEW_HEADER
        BrowseRow.Kind.ITEM -> VIEW_ITEM
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): RecyclerView.ViewHolder {
        val inflater = LayoutInflater.from(parent.context)
        return when (viewType) {
            VIEW_HERO -> HeroHolder(inflater.inflate(R.layout.item_home_hero, parent, false))
            VIEW_METRICS -> MetricsHolder(inflater.inflate(R.layout.item_home_metrics, parent, false))
            VIEW_SERVER -> ServerHolder(inflater.inflate(R.layout.item_home_server, parent, false))
            VIEW_FACTS -> FactsHolder(inflater.inflate(R.layout.item_home_facts, parent, false))
            VIEW_JOURNAL_HEAD -> HeaderHolder(inflater.inflate(R.layout.item_browse_header, parent, false))
            VIEW_JOURNAL -> JournalHolder(inflater.inflate(R.layout.item_journal, parent, false))
            VIEW_HEADER -> HeaderHolder(inflater.inflate(R.layout.item_browse_header, parent, false))
            else -> Holder(inflater.inflate(R.layout.item_browse, parent, false))
        }
    }

    override fun onBindViewHolder(holder: RecyclerView.ViewHolder, position: Int) {
        val row = rows[position]
        when (holder) {
            is HeroHolder -> holder.bind(row)
            is MetricsHolder -> holder.bind(row)
            is ServerHolder -> holder.bind(row, onOpen)
            is FactsHolder -> holder.bind(row)
            is JournalHolder -> holder.bind(row)
            is HeaderHolder -> holder.bind(row)
            is Holder -> holder.bind(row, onOpen, onDownload)
        }
    }

    override fun getItemCount(): Int = rows.size

    class HeroHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val copy: TextView = itemView.findViewById(R.id.textHeroCopy)
        private val pulse: TextView = itemView.findViewById(R.id.textPulseValue)
        fun bind(row: BrowseRow) {
            copy.text = row.heroCopy
            pulse.text = row.pulseValue
        }
    }

    class MetricsHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val servers: TextView = itemView.findViewById(R.id.textMetricServers)
        private val repos: TextView = itemView.findViewById(R.id.textMetricRepos)
        private val pending: TextView = itemView.findViewById(R.id.textMetricPending)
        fun bind(row: BrowseRow) {
            servers.text = row.metricServers
            repos.text = row.metricRepos
            pending.text = row.metricPending
        }
    }

    class ServerHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val name: TextView = itemView.findViewById(R.id.textServerPanelName)
        private val meta: TextView = itemView.findViewById(R.id.textServerPanelMeta)
        fun bind(row: BrowseRow, onOpen: (BrowseRow) -> Unit) {
            name.text = row.name
            meta.text = row.serverMeta
            itemView.setOnClickListener {
                if (row.switchServerId.isNotEmpty()) onOpen(row)
            }
            itemView.isClickable = row.switchServerId.isNotEmpty()
        }
    }

    class FactsHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val server: TextView = itemView.findViewById(R.id.textFactServer)
        private val revision: TextView = itemView.findViewById(R.id.textFactRevision)
        private val access: TextView = itemView.findViewById(R.id.textFactAccess)
        private val folder: TextView = itemView.findViewById(R.id.textFactFolder)
        fun bind(row: BrowseRow) {
            server.text = row.factServer
            revision.text = row.factRevision
            access.text = row.factAccess
            folder.text = row.factFolder
        }
    }

    class JournalHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val entry: TextView = itemView.findViewById(R.id.textJournalEntry)
        private val scope: TextView = itemView.findViewById(R.id.textJournalScope)
        private val time: TextView = itemView.findViewById(R.id.textJournalTime)
        fun bind(row: BrowseRow) {
            entry.text = row.journalEntry
            scope.text = row.journalScope
            time.text = if (row.size > 0) {
                DateUtils.getRelativeTimeSpanString(row.size, System.currentTimeMillis(), DateUtils.MINUTE_IN_MILLIS)
            } else {
                row.journalTime
            }
        }
    }

    class HeaderHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val label: TextView = itemView.findViewById(R.id.textSectionHeader)
        fun bind(row: BrowseRow) {
            label.text = row.sectionHeader ?: row.name
        }
    }

    class Holder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val icon: ImageView = itemView.findViewById(R.id.imageBrowseIcon)
        private val title: TextView = itemView.findViewById(R.id.textBrowseName)
        private val meta: TextView = itemView.findViewById(R.id.textBrowseMeta)
        private val download: MaterialButton = itemView.findViewById(R.id.buttonDownload)

        fun bind(row: BrowseRow, onOpen: (BrowseRow) -> Unit, onDownload: (BrowseRow) -> Unit) {
            title.text = row.name
            if (row.directory || row.share) {
                icon.setImageResource(R.drawable.ic_folder)
                meta.text = itemView.context.getString(R.string.browse_directory)
                download.visibility = if (row.share) View.GONE else View.VISIBLE
                download.setOnClickListener { onDownload(row) }
                itemView.setOnClickListener { onOpen(row) }
            } else {
                icon.setImageResource(R.drawable.ic_file)
                meta.text = HumanSize.format(row.size)
                download.visibility = View.VISIBLE
                download.setOnClickListener { onDownload(row) }
                itemView.setOnClickListener { onOpen(row) }
            }
        }
    }

    companion object {
        private const val VIEW_ITEM = 0
        private const val VIEW_HEADER = 1
        private const val VIEW_HERO = 2
        private const val VIEW_METRICS = 3
        private const val VIEW_SERVER = 4
        private const val VIEW_FACTS = 5
        private const val VIEW_JOURNAL_HEAD = 6
        private const val VIEW_JOURNAL = 7
    }
}
