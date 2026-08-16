package net.filees.mobile

import android.content.Context
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import androidx.recyclerview.widget.RecyclerView

class PendingUploadsAdapter(
    private val onDiscard: (PendingUpload) -> Unit,
) : RecyclerView.Adapter<PendingUploadsAdapter.ViewHolder>() {

    private var items: List<PendingUpload> = emptyList()

    fun submit(newItems: List<PendingUpload>) {
        items = newItems
        notifyDataSetChanged()
    }

    override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): ViewHolder {
        val view = LayoutInflater.from(parent.context).inflate(R.layout.item_pending_upload, parent, false)
        return ViewHolder(view)
    }

    override fun onBindViewHolder(holder: ViewHolder, position: Int) {
        holder.bind(items[position], onDiscard)
    }

    override fun getItemCount(): Int = items.size

    companion object {
        private fun outcomeReason(context: Context, outcome: String): String {
            val id = when (outcome) {
                "DESTINATION_GONE" -> R.string.upload_outcome_destination_gone
                "ACCESS_REVOKED" -> R.string.upload_outcome_access_revoked
                "REPO_INACTIVE" -> R.string.upload_outcome_repo_inactive
                "POLICY_REJECTED" -> R.string.upload_outcome_policy_rejected
                "NAME_TAKEN_DIFF" -> R.string.upload_outcome_name_taken_diff
                "NAME_TAKEN_SAME" -> R.string.upload_outcome_name_taken_same
                else -> 0
            }
            return if (id != 0) context.getString(id) else ""
        }
    }

    class ViewHolder(itemView: View) : RecyclerView.ViewHolder(itemView) {
        private val summary: android.widget.TextView = itemView.findViewById(R.id.textUploadSummary)
        private val discard: com.google.android.material.button.MaterialButton =
            itemView.findViewById(R.id.buttonDiscardUpload)

        fun bind(item: PendingUpload, onDiscard: (PendingUpload) -> Unit) {
            val stateLabel = itemView.resources.getIdentifier(
                "upload_state_" + item.state.replace('-', '_'),
                "string",
                itemView.context.packageName,
            )
            val stateText = if (stateLabel != 0) itemView.context.getString(stateLabel) else item.state
            val path = if (item.parentPath.isEmpty()) item.filename else "${item.parentPath}/${item.filename}"
            var text = "$path (${item.size} B) — $stateText"
            val reason = PendingUploadsAdapter.outcomeReason(itemView.context, item.outcome)
            if (reason.isNotEmpty()) {
                text += "\n$reason"
            }
            if (item.state == "conflict" && item.existingSha256.isNotEmpty()) {
                text += "\nistniejący sha256: ${item.existingSha256.take(16)}…"
            }
            if (item.lastError.isNotEmpty()) {
                text += "\n${item.lastError}"
            }
            summary.text = text
            discard.visibility = if (item.needsDecision) View.VISIBLE else View.GONE
            discard.setOnClickListener { onDiscard(item) }
        }
    }
}
