package net.filees.mobile

import androidbind.Client

object UploadDrain {
    data class Report(
        val items: List<PendingUpload>,
        val decisions: List<PendingUpload>,
        val transportError: String?,
    )

    fun run(client: Client, repoId: String): Report {
        val items = PendingUpload.listFromJson(client.drainPendingJSON(repoId))
        val failed = items.firstOrNull { it.state == "pending-create" && it.lastError.isNotBlank() }
        return Report(
            items = items,
            decisions = items.filter { it.needsDecision },
            transportError = failed?.let {
                val where = listOf(it.parentPath, it.filename).filter { part -> part.isNotBlank() }.joinToString("/")
                "$where: ${it.lastError}"
            },
        )
    }
}
