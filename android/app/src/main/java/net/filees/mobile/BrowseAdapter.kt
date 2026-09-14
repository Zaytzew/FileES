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
        BrowseRow.Kind.ADD_SERVER -> VIEW_ADD_SERVER
        BrowseRow.Kind.ITEM -> VIEW_ITEM
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): RecyclerView.ViewHolder {
        val inflater = LayoutInflater.from(parent.context)
        return when (viewType) {
            VIEW_HERO -> HeroHolder(inflater.inflate(R.layout.item_home_hero, parent, false))
            VIEW_METRICS -> MetricsHolder(inflater.inflate(R.layout.item_home_metrics, parent, false))
            VIEW_SERVER -> ServerHolder(inflater.inflate(R.layout.item_home_server, parent, false))
            VIEW_FACTS -> FactsHolder(inflater.inflate(R.layout.item_home_facts, parent, false))
            VIEW_JOURNAL_HEAD -> JournalPanelHolder(inflater.inflate(R.layout.item_journal_panel, parent, false))
            VIEW_JOURNAL -> JournalEntryHolder(inflater.inflate(R.layout.item_journal, parent, false))
            VIEW_HEADER -> HeaderHolder(inflater.inflate(R.layout.item_browse_header, parent, false))
            VIEW_ADD_SERVER -> AddServerHolder(inflater.inflate(R.layout.item_add_server, parent, false))
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
            is JournalPanelHolder -> holder.bind(row)
            is JournalEntryHolder -> holder.bind(row)
            is HeaderHolder -> holder.bind(row)
            is AddServerHolder -> holder.bind(row, onOpen)
            is Holder -> holder.bind(row, onOpen, onDownload)
        }
    }

    override fun getItemCount(): Int = rows.size

    class HeroHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val copy: TextView = itemView.findViewById(R.id.textHeroCopy)
        fun bind(row: BrowseRow) {
            copy.text = row.heroCopy
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

    // Header of a server panel. Its folders follow as separate items; this
    // holder no longer builds them, it only draws the top of the card.
    class ServerHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val name: TextView = itemView.findViewById(R.id.textServerPanelName)
        private val meta: TextView = itemView.findViewById(R.id.textServerPanelMeta)
        private val chevron: View = itemView.findViewById(R.id.textServerChevron)
        private val accentBar: View = itemView.findViewById(R.id.viewRealmAccent)
        private val eyebrow: TextView = itemView.findViewById(R.id.textServerEyebrow)
        fun bind(row: BrowseRow, onOpen: (BrowseRow) -> Unit) {
            itemView.setBackgroundResource(panelBackground(row.panel))
            val accent = RealmAccent.parse(row.accentColor)
            accentBar.setBackgroundColor(accent)
            eyebrow.setTextColor(accent)
            name.text = row.name
            meta.text = row.serverMeta
            val switch = row.switchServerId.isNotEmpty()
            chevron.visibility = if (switch) View.VISIBLE else View.GONE
            itemView.setOnClickListener(if (switch) View.OnClickListener { onOpen(row) } else null)
            itemView.isClickable = switch
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

    // Header of the phone journal panel. SINGLE means no entries follow, so
    // the empty note shows inside the same card.
    class JournalPanelHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val empty: View = itemView.findViewById(R.id.textJournalEmpty)
        fun bind(row: BrowseRow) {
            itemView.setBackgroundResource(panelBackground(row.panel))
            empty.visibility = if (row.panel == BrowseRow.Panel.SINGLE) View.VISIBLE else View.GONE
        }
    }

    class JournalEntryHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val entry: TextView = itemView.findViewById(R.id.textJournalEntry)
        private val scope: TextView = itemView.findViewById(R.id.textJournalScope)
        private val time: TextView = itemView.findViewById(R.id.textJournalTime)
        private val divider: View = itemView.findViewById(R.id.dividerJournal)
        fun bind(row: BrowseRow) {
            itemView.setBackgroundResource(panelBackground(row.panel))
            entry.text = row.journalEntry
            scope.text = row.journalScope
            time.text = if (row.size > 0) {
                DateUtils.getRelativeTimeSpanString(
                    row.size,
                    System.currentTimeMillis(),
                    DateUtils.MINUTE_IN_MILLIS,
                )
            } else {
                row.journalTime
            }
            divider.visibility = if (row.panel == BrowseRow.Panel.BOTTOM) View.GONE else View.VISIBLE
        }
    }

    class AddServerHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val button: MaterialButton = itemView.findViewById(R.id.buttonAddServerRow)
        fun bind(row: BrowseRow, onOpen: (BrowseRow) -> Unit) {
            button.setOnClickListener { onOpen(row) }
        }
    }

    class HeaderHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val label: TextView = itemView.findViewById(R.id.textSectionHeader)
        fun bind(row: BrowseRow) {
            applyPanel(itemView, row.panel)
            label.text = row.sectionHeader ?: row.name
        }
    }

    class Holder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val icon: ImageView = itemView.findViewById(R.id.imageBrowseIcon)
        private val title: TextView = itemView.findViewById(R.id.textBrowseName)
        private val meta: TextView = itemView.findViewById(R.id.textBrowseMeta)
        private val download: MaterialButton = itemView.findViewById(R.id.buttonDownload)
        private val divider: View = itemView.findViewById(R.id.dividerBrowse)

        fun bind(row: BrowseRow, onOpen: (BrowseRow) -> Unit, onDownload: (BrowseRow) -> Unit) {
            applyPanel(itemView, row.panel)
            // The card's own edge closes the last row; a hairline above it
            // would read as a double line.
            divider.visibility = when (row.panel) {
                BrowseRow.Panel.BOTTOM, BrowseRow.Panel.SINGLE -> View.GONE
                else -> View.VISIBLE
            }
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
        private const val VIEW_ADD_SERVER = 8
    }
}

// Background for a row's position in a panel. Always applied on bind, NONE
// included, because holders are recycled between the home screen, where rows
// sit in cards, and folder browsing, where they do not.
private fun panelBackground(panel: BrowseRow.Panel): Int = when (panel) {
    BrowseRow.Panel.TOP -> R.drawable.bg_filees_panel_top
    BrowseRow.Panel.MIDDLE -> R.drawable.bg_filees_panel_middle
    BrowseRow.Panel.BOTTOM -> R.drawable.bg_filees_panel_bottom
    BrowseRow.Panel.SINGLE -> R.drawable.bg_filees_panel
    BrowseRow.Panel.NONE -> 0
}

// Rows inside a panel keep the inset the old nested container gave them;
// outside a panel they sit on the list gutter as before.
private fun applyPanel(view: View, panel: BrowseRow.Panel) {
    view.setBackgroundResource(panelBackground(panel))
    val side = if (panel == BrowseRow.Panel.NONE) {
        0
    } else {
        view.resources.getDimensionPixelSize(R.dimen.filees_panel_inset)
    }
    view.setPaddingRelative(side, view.paddingTop, side, view.paddingBottom)
}
