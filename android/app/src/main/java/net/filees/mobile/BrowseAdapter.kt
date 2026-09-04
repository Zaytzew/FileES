package net.filees.mobile

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

    override fun getItemViewType(position: Int): Int =
        if (rows[position].sectionHeader != null) VIEW_TYPE_HEADER else VIEW_TYPE_ITEM

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): RecyclerView.ViewHolder {
        val inflater = LayoutInflater.from(parent.context)
        return if (viewType == VIEW_TYPE_HEADER) {
            HeaderHolder(inflater.inflate(R.layout.item_browse_header, parent, false))
        } else {
            Holder(inflater.inflate(R.layout.item_browse, parent, false))
        }
    }

    override fun onBindViewHolder(holder: RecyclerView.ViewHolder, position: Int) {
        when (holder) {
            is HeaderHolder -> holder.bind(rows[position])
            is Holder -> holder.bind(rows[position], onOpen, onDownload)
        }
    }

    override fun getItemCount(): Int = rows.size

    class HeaderHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val label: TextView = itemView.findViewById(R.id.textSectionHeader)
        fun bind(row: BrowseRow) {
            label.text = row.sectionHeader
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
        private const val VIEW_TYPE_ITEM = 0
        private const val VIEW_TYPE_HEADER = 1
    }
}
