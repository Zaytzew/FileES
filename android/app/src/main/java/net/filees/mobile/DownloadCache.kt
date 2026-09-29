package net.filees.mobile

import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.UUID

/** Disposable private copies, not the user's public Downloads or upload queue. */
internal object DownloadCache {
    private const val MAX_AGE = 24L * 60 * 60 * 1000
    private val forgotten = mutableSetOf<String>()
    private fun key(server: String) = MessageDigest.getInstance("SHA-256")
        .digest(server.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it) }
    private fun scope(root: File, server: String) = File(root, "dl/v2/${key(server)}")

    @Synchronized fun checkActive(root: File, server: String) {
        if (scope(root, server).absolutePath in forgotten) throw IOException("Download server was unpaired")
    }

    @Synchronized fun create(root: File, server: String): File {
        checkActive(root, server)
        val attempt = File(scope(root, server), UUID.randomUUID().toString())
        if (!attempt.mkdirs()) throw IOException("Cannot create private download directory")
        return attempt
    }

    @Synchronized fun forget(root: File, server: String) {
        val dir = scope(root, server)
        forgotten += dir.absolutePath // An in-flight old task cannot create a new attempt.
        runCatching { removeInside(File(root, "dl"), dir) }
    }

    // Run before activities/workers start. No in-process live attempt exists yet.
    @Synchronized fun sweep(root: File, now: Long = System.currentTimeMillis(), keepApkName: String? = null) {
        val dl = File(root, "dl")
        val cutoff = now - MAX_AGE
        dl.listFiles().orEmpty().filter { it.name != "v2" && it.lastModified() in 1 until cutoff }
            .forEach { removeInside(dl, it) }
        File(dl, "v2").listFiles().orEmpty().forEach { server ->
            if (server.canonicalFile != File(server.parentFile.canonicalFile, server.name)) return@forEach
            server.listFiles().orEmpty().filter { it.lastModified() in 1 until cutoff }
                .forEach { removeInside(dl, it) }
        }
        // Startup only; never the durable queue in filesDir. Keep the prepared
        // installer and give all other attempts a full day, not immediate GC.
        root.listFiles().orEmpty().filter { file ->
            val name = file.name
            file.isFile && file.canonicalFile == File(file.parentFile.canonicalFile, name) &&
                file.lastModified() in 1 until cutoff && name != keepApkName &&
                (Regex("filees-update-[a-fA-F0-9]{64}\\.apk").matches(name) ||
                    (name.startsWith("filees-update-") && name.endsWith(".part")) ||
                    (name.startsWith("capture-") && name.endsWith(".tmp")))
        }.forEach { it.delete() }
    }

    fun removeAttempt(root: File, attempt: File) { runCatching { removeInside(File(root, "dl"), attempt) } }

    private fun removeInside(root: File, file: File) {
        val base = root.canonicalFile
        if (file.canonicalFile != File(file.parentFile.canonicalFile, file.name) || !file.canonicalPath.startsWith(base.path + File.separator)) return
        // Walk without following symbolic links; only this private cache subtree.
        if (file.isDirectory) file.listFiles().orEmpty().forEach { removeInside(base, it) }
        file.delete()
    }
}
