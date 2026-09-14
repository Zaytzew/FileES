package net.filees.mobile

import org.json.JSONObject

data class RealmShare(
    val repoId: String,
    val displayName: String,
    val access: String,
    val state: String,
    // Mirrors clientview.Repository.Purpose ("" | "upload_shelf" |
    // "upload_trash"); empty means an ordinary repository.
    val purpose: String = "",
) {
    val selectable: Boolean
        get() = (state == "active" || state == "initializing") && (access == "r" || access == "rw")

    val isUploadShelf: Boolean
        get() = purpose == "upload_shelf"

    val isUploadTrash: Boolean
        get() = purpose == "upload_trash"

    // Capture (Dodaj / śledzone foldery) writes mobile-uploads/. The realm
    // trash is a reject waiting room, not a camera dump. Shelves stay
    // writable: the owner may still drop photos into the same delivery repo.
    val canCapture: Boolean
        get() = selectable && access == "rw" && !isUploadTrash
}

data class RealmProjection(
    val realmId: String,
    val realmAlias: String,
    val serverDisplayName: String,
    val generatedAt: String,
    val leadingColor: String = "",
    val shares: List<RealmShare>,
) {
    companion object {
        fun fromJson(json: String): RealmProjection {
            if (json.isBlank()) return RealmProjection("", "", "", "", "", emptyList())
            val root = JSONObject(json)
            val entries = root.optJSONArray("repositories")
            val shares = mutableListOf<RealmShare>()
            if (entries != null) {
                for (i in 0 until entries.length()) {
                    val item = entries.getJSONObject(i)
                    shares.add(
                        RealmShare(
                            repoId = item.optString("repo_id"),
                            displayName = item.optString("display_name"),
                            access = item.optString("access"),
                            state = item.optString("state"),
                            purpose = item.optString("purpose"),
                        )
                    )
                }
            }
            return RealmProjection(
                realmId = root.optString("realm_id"),
                realmAlias = root.optString("realm_alias"),
                serverDisplayName = root.optString("server_display_name"),
                generatedAt = root.optString("generated_at"),
                leadingColor = root.optString("leading_color"),
                shares = shares,
            )
        }
    }
}

data class ManifestEntry(
    val path: String,
    val kind: String,
    val size: Long,
)

data class BrowseRow(
    val name: String,
    val path: String,
    val directory: Boolean,
    val size: Long,
    val repoId: String = "",
    val share: Boolean = false,
    val sectionHeader: String? = null,
    val kind: Kind = Kind.ITEM,
    val metricServers: String = "",
    val metricRepos: String = "",
    val metricPending: String = "",
    val factServer: String = "",
    val factRevision: String = "",
    val factAccess: String = "",
    val factFolder: String = "",
    val journalScope: String = "",
    val journalTime: String = "",
    val journalEntry: String = "",
    val heroCopy: String = "",
    val serverMeta: String = "",
    val switchServerId: String = "",
    val accentColor: String = "",
    val panel: Panel = Panel.NONE,
) {
    enum class Kind {
        ITEM, HEADER, HERO, METRICS, SERVER, FACTS, JOURNAL_HEAD, JOURNAL, ADD_SERVER
    }

    // Where a row sits in a panel that spans several list items. Rows used to
    // be nested inside one item and inflated by hand, which switched recycling
    // off; a segment position lets every row be its own item and still draw
    // as one card. NONE is a row outside any panel, as when browsing a folder.
    enum class Panel {
        NONE, TOP, MIDDLE, BOTTOM, SINGLE
    }
}

object ManifestBrowse {
    fun entriesFrom(manifestJson: String): List<ManifestEntry> {
        if (manifestJson.isBlank()) return emptyList()
        val root = JSONObject(manifestJson)
        val array = root.optJSONArray("entries") ?: return emptyList()
        val out = ArrayList<ManifestEntry>(array.length())
        for (i in 0 until array.length()) {
            val item = array.getJSONObject(i)
            out.add(
                ManifestEntry(
                    path = item.optString("path"),
                    kind = item.optString("kind"),
                    size = item.optLong("size"),
                )
            )
        }
        return out
    }

    fun children(entries: List<ManifestEntry>, prefix: String): List<BrowseRow> {
        val pfx = if (prefix.isEmpty()) "" else "$prefix/"
        val seen = LinkedHashSet<String>()
        val rows = mutableListOf<BrowseRow>()
        for (entry in entries) {
            if (prefix.isNotEmpty() && entry.path == prefix) continue
            if (prefix.isNotEmpty() && !entry.path.startsWith(pfx)) continue
            val rest = if (prefix.isEmpty()) entry.path else entry.path.removePrefix(pfx)
            if (rest.isEmpty()) continue
            val name = rest.substringBefore('/')
            if (!seen.add(name)) continue
            val nested = rest.contains('/')
            val directory = nested || entry.kind == "directory"
            val path = if (prefix.isEmpty()) name else "$prefix/$name"
            rows.add(BrowseRow(name, path, directory, if (directory) 0 else entry.size))
        }
        return rows.sortedWith(compareBy({ !it.directory }, { it.name.lowercase() }))
    }

    fun filesUnder(entries: List<ManifestEntry>, prefix: String): List<ManifestEntry> {
        val pfx = if (prefix.isEmpty()) "" else "$prefix/"
        return entries.filter { entry ->
            entry.kind == "file" && (
                if (prefix.isEmpty()) true
                else entry.path.startsWith(pfx)
                )
        }
    }

    fun shoutsFrom(manifestJson: String): List<Pair<Long, String>> {
        if (manifestJson.isBlank()) return emptyList()
        val array = JSONObject(manifestJson).optJSONArray("shouts") ?: return emptyList()
        val out = ArrayList<Pair<Long, String>>(array.length())
        for (i in 0 until array.length()) {
            val item = array.getJSONObject(i)
            val comment = item.optString("comment")
            val revision = item.optLong("revision")
            if (comment.isNotBlank() && revision > 0) {
                out.add(revision to comment)
            }
        }
        return out
    }
}
