package net.filees.mobile

import androidbind.Client

object UploadDrain {
    fun drainOrThrow(client: Client, repoId: String): List<PendingUpload> {
        val items = PendingUpload.listFromJson(client.drainPendingJSON(repoId))
        val failed = items.firstOrNull { it.state == "pending-create" && it.lastError.isNotBlank() }
        if (failed != null) {
            val where = listOf(failed.parentPath, failed.filename).filter { it.isNotBlank() }.joinToString("/")
            throw RuntimeException("$where: ${failed.lastError}")
        }
        return items
    }
}
