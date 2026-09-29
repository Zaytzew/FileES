package net.filees.mobile

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

/** Publication is successful only after the public entry is committed. */
internal object DownloadPublication {
    fun requireName(name: String) {
        require(name.isNotBlank() && name != "." && name != ".." &&
            name.none { it == '/' || it == '\\' || it == '\u0000' }) { "Invalid download name" }
    }

    fun <T : Any> pending(
        input: () -> InputStream,
        create: () -> T?,
        open: (T) -> OutputStream?,
        commit: (T) -> Int,
        remove: (T) -> Unit,
    ) {
        val entry = create() ?: throw IOException("Cannot create Downloads entry")
        try {
            val output = open(entry) ?: throw IOException("Cannot open Downloads entry")
            output.use { out -> input().use { it.copyTo(out) } }
            if (commit(entry) != 1) throw IOException("Cannot publish Downloads entry")
        } catch (e: Exception) {
            try { remove(entry) } catch (cleanup: Exception) { e.addSuppressed(cleanup) }
            throw e
        }
    }

    fun legacy(source: File, dir: File, name: String): File {
        requireName(name)
        if (!dir.isDirectory && !dir.mkdirs()) throw IOException("Cannot create Downloads directory")
        val dot = name.lastIndexOf('.')
        val stem = if (dot > 0) name.substring(0, dot) else name
        val ext = if (dot > 0) name.substring(dot) else ""
        for (n in 0..9999) {
            val target = File(dir, if (n == 0) name else "$stem ($n)$ext")
            // Atomic reservation: concurrent downloads never choose the same name.
            if (!target.createNewFile()) continue
            try {
                target.outputStream().use { out -> source.inputStream().use { it.copyTo(out) } }
                return target
            } catch (e: Exception) {
                if (!target.delete()) e.addSuppressed(IOException("Cannot remove incomplete download"))
                throw e
            }
        }
        throw IOException("Too many Downloads entries with this name")
    }
}
