package net.filees.mobile

/** Persisted watch bindings. Legacy trees never acquire an owner implicitly. */
internal class WatchRegistry(private val store: Store, val serverId: String?) {
    interface Store {
        fun list(key: String): List<String>
        fun lists(values: Map<String, List<String>>)
        fun flag(key: String): Boolean
        fun flag(key: String, value: Boolean)
    }

    fun trees(): List<String> = synchronized(guard) {
        if (serverId.isNullOrBlank()) emptyList() else store.list(treeKey())
    }
    fun unassigned(): List<String> = synchronized(guard) { store.list(LEGACY_TREES) }

    // Called only after explicit destination/backlog consent, never on read.
    fun add(uri: String) = synchronized(guard) {
        require(!serverId.isNullOrBlank()) { "A watch requires a paired server" }
        store.lists(mapOf(
            treeKey() to (trees() + uri).distinct(),
            LEGACY_TREES to unassigned().filterNot { it == uri },
        ))
    }
    fun remove(uri: String) = synchronized(guard) {
        if (!serverId.isNullOrBlank()) store.lists(mapOf(treeKey() to trees().filterNot { it == uri }))
    }
    fun removeUnassigned(uri: String) = synchronized(guard) {
        store.lists(mapOf(LEGACY_TREES to unassigned().filterNot { it == uri }))
    }
    fun seen(repoId: String, source: String): Boolean = store.flag(seenKey(repoId, source))
    fun markSeen(repoId: String, source: String) = store.flag(seenKey(repoId, source), true)

    private fun treeKey() = "trees-v2:" + component(serverId.orEmpty())
    private fun seenKey(repoId: String, source: String): String {
        require(!serverId.isNullOrBlank() && repoId.isNotBlank()) { "A receipt requires a server and repository" }
        return "seen-v2:" + listOf(serverId, repoId, source).joinToString("") { component(it) }
    }
    companion object {
        private const val LEGACY_TREES = "trees"
        private val guard = Any()
        // Length prefixes keep arbitrary source strings / IDs unambiguous.
        private fun component(value: String) = "${value.length}:$value"
    }
}
