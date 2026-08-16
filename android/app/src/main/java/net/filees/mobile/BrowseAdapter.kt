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
) : RecyclerView.Adapter<BrowseAdapter.Holder>() {

    private var rows: List<BrowseRow> = emptyList()

    fun submit(next: List<BrowseRow>) {
        rows = next
        notifyDataSetChanged()
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): Holder {
        val view = LayoutInflater.from(parent.context).inflate(R.layout.item_browse, parent, false)
        return Holder(view)
    }

    override fun onBindViewHolder(holder: Holder, position: Int) {
        holder.bind(rows[position], onOpen, onDownload)
    }

    override fun getItemCount(): Int = rows.size

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
                download.visibility = View.GONE
                itemView.setOnClickListener { onOpen(row) }
            } else {
                icon.setImageResource(R.drawable.ic_file)
                meta.text = HumanSize.format(row.size)
                download.visibility = View.VISIBLE
                download.setOnClickListener { onDownload(row) }
                itemView.setOnClickListener { onDownload(row) }
            }
        }
    }
}
