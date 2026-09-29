package net.filees.mobile

/** Exercises the production registry with a storage double, without Android APIs. */
internal object WatchRegistryChecks {
    private class MemoryStore : WatchRegistry.Store {
        val lists = mutableMapOf<String, List<String>>()
        val flags = mutableMapOf<String, Boolean>()
        var rejectWrites = false
        override fun list(key: String) = lists[key].orEmpty()
        override fun lists(values: Map<String, List<String>>) {
            check(!rejectWrites) { "disk failure" }
            lists.putAll(values.mapValues { it.value.toList() })
        }
        override fun flag(key: String) = flags[key] == true
        override fun flag(key: String, value: Boolean) {
            check(!rejectWrites) { "disk failure" }
            flags[key] = value
        }
        fun reopen() = MemoryStore().also { it.lists.putAll(lists); it.flags.putAll(flags) }
    }

    @JvmStatic fun main(args: Array<String>) {
        run()
        println("WatchRegistryChecks: 8 cases PASS")
    }
    fun run() {
        legacyNeverGuessesAnOwner()
        explicitAdoptionOnlyMovesChosenTree()
        bindingsRemainSeparate()
        receiptsBelongToServerAndRepository()
        oldReceiptsAreNotAdopted()
        missingServerFailsClosed()
        failedWriteLeavesMigrationUnfinished()
        concurrentAddsDoNotLoseBindings()
    }

    private fun legacyNeverGuessesAnOwner() {
        val store = MemoryStore().apply { lists["trees"] = listOf("camera", "docs") }
        for (id in listOf(null, "A", "B", "A")) {
            val registry = WatchRegistry(store, id)
            check(registry.trees().isEmpty())
            check(registry.unassigned() == listOf("camera", "docs"))
        }
        check(store.lists.keys == setOf("trees")) // reads perform no migration, even with one server
    }

    private fun explicitAdoptionOnlyMovesChosenTree() {
        val store = MemoryStore().apply { lists["trees"] = listOf("camera", "docs") }
        val a = WatchRegistry(store, "A")
        a.add("camera"); a.add("camera")
        check(a.trees() == listOf("camera"))
        check(a.unassigned() == listOf("docs"))
        val restarted = WatchRegistry(store.reopen(), "A")
        check(restarted.trees() == listOf("camera") && restarted.unassigned() == listOf("docs"))
        a.removeUnassigned("docs")
        check(a.unassigned().isEmpty() && a.trees() == listOf("camera"))
    }

    private fun bindingsRemainSeparate() {
        val store = MemoryStore()
        val a = WatchRegistry(store, "A")
        val b = WatchRegistry(store, "B")
        a.add("camera"); b.add("docs")
        check(a.trees() == listOf("camera") && b.trees() == listOf("docs"))
        // An in-flight worker's registry stays bound to A when UI selects B.
        val ui = WatchRegistry(store, "B")
        ui.add("camera"); ui.remove("camera")
        check(a.trees() == listOf("camera") && b.trees() == listOf("docs"))
        check(WatchRegistry(store, "new-pairing-of-A").trees().isEmpty())
    }

    private fun receiptsBelongToServerAndRepository() {
        val store = MemoryStore()
        val a = WatchRegistry(store, "A")
        val b = WatchRegistry(store, "B")
        a.markSeen("repo-1", "source")
        check(a.seen("repo-1", "source"))
        check(!a.seen("repo-2", "source") && !b.seen("repo-1", "source"))
        check(WatchRegistry(store.reopen(), "A").seen("repo-1", "source"))
        // Separators in source/ID components cannot alias another tuple.
        a.markSeen("x:y", "z")
        check(!a.seen("x", "y:z"))
    }

    private fun oldReceiptsAreNotAdopted() {
        val store = MemoryStore().apply { flags["seen_source"] = true }
        val a = WatchRegistry(store, "A")
        check(!a.seen("repo", "source"))
        check(store.flags["seen_source"] == true) // preserve unknown provenance, do not reinterpret it
    }

    private fun missingServerFailsClosed() {
        for (id in listOf(null, "")) {
            val registry = WatchRegistry(MemoryStore(), id)
            check(registry.trees().isEmpty())
            check(runCatching { registry.add("camera") }.isFailure)
            check(runCatching { registry.markSeen("repo", "source") }.isFailure)
        }
        check(runCatching { WatchRegistry(MemoryStore(), "A").markSeen("", "source") }.isFailure)
    }

    private fun failedWriteLeavesMigrationUnfinished() {
        val store = MemoryStore().apply { lists["trees"] = listOf("camera"); rejectWrites = true }
        val a = WatchRegistry(store, "A")
        check(runCatching { a.add("camera") }.isFailure)
        check(a.trees().isEmpty() && a.unassigned() == listOf("camera"))
        store.rejectWrites = false
        a.add("camera")
        check(a.trees() == listOf("camera") && a.unassigned().isEmpty())
    }

    private fun concurrentAddsDoNotLoseBindings() {
        val store = MemoryStore()
        val errors = java.util.concurrent.ConcurrentLinkedQueue<Throwable>()
        val workers = (1..16).map { n -> Thread {
            try { WatchRegistry(store, "A").add("tree-$n") }
            catch (e: Throwable) { errors.add(e) }
        } }
        workers.forEach { it.start() }; workers.forEach { it.join(5000) }
        check(workers.none { it.isAlive } && errors.isEmpty())
        check(WatchRegistry(store, "A").trees().toSet() == (1..16).map { "tree-$it" }.toSet())
    }
}
