package net.filees.mobile

import android.content.ContentResolver
import java.io.File
import java.util.zip.CRC32
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/** Zip-on-wire packer. Lives only in cacheDir; sweep leftovers on launch. */
object TreeZip {
    // Must match pkg/mobile/v1.TreePackComment. The worker unpacks only this.
    const val COMMENT = "filees.tree/v1"

    private val storeExt = setOf(
        "jpg", "jpeg", "png", "gif", "webp", "heic",
        "mp4", "mov", "m4v", "avi",
        "mp3", "m4a", "ogg", "wav", "flac",
        "zip", "gz", "bz2", "xz", "7z", "rar",
        "pdf",
    )

    fun pack(resolver: ContentResolver, files: List<WalkedFile>, cacheDir: File): File {
        cacheDir.mkdirs()
        val out = File(cacheDir, "pack-${System.currentTimeMillis()}.zip")
        try {
            ZipOutputStream(out.outputStream().buffered()).use { zip ->
                zip.setComment(COMMENT)
                for (file in files) {
                    val name = listOf(file.relativeDir.trim('/'), file.filename)
                        .filter { it.isNotBlank() }
                        .joinToString("/")
                    val bytes = resolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
                    val entry = ZipEntry(name)
                    if (stored(file.filename)) {
                        val crc = CRC32()
                        crc.update(bytes)
                        entry.method = ZipEntry.STORED
                        entry.size = bytes.size.toLong()
                        entry.compressedSize = bytes.size.toLong()
                        entry.crc = crc.value
                    } else {
                        entry.method = ZipEntry.DEFLATED
                    }
                    zip.putNextEntry(entry)
                    zip.write(bytes)
                    zip.closeEntry()
                }
            }
        } catch (e: Exception) {
            out.delete()
            throw e
        }
        return out
    }

    fun sweep(cacheDir: File) {
        cacheDir.listFiles { f ->
            f.isFile && f.name.startsWith("pack-") && f.name.endsWith(".zip")
        }?.forEach { it.delete() }
    }

    private fun stored(filename: String): Boolean {
        val ext = filename.substringAfterLast('.', "").lowercase()
        return ext in storeExt
    }
}
