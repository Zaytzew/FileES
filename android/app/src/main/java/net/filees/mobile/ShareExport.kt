package net.filees.mobile

import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import java.io.File
import java.io.IOException

/** A private download becomes readable to one chosen app through a temporary URI grant. */
internal object ShareExport {
    fun relativeFiles(prefix: String, files: List<ManifestEntry>): List<Pair<ManifestEntry, String>> {
        if (prefix.isNotEmpty() && !safePath(prefix)) throw IOException("Invalid folder path")
        val base = if (prefix.isEmpty()) "" else "$prefix/"
        val names = HashSet<String>()
        return files.map { entry ->
            if (entry.kind != "file" || !entry.path.startsWith(base)) throw IOException("Invalid folder entry")
            val relative = entry.path.removePrefix(base)
            if (!safePath(relative) || !names.add(relative)) throw IOException("Invalid ZIP entry")
            entry to relative
        }
    }

    fun sendIntent(context: Context, file: File, name: String, folder: Boolean): Intent {
        DownloadPublication.requireName(name)
        val uri: Uri = FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        val mime = if (folder) "application/zip" else
            MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase()) ?: "application/octet-stream"
        return Intent(Intent.ACTION_SEND).apply {
            type = mime
            putExtra(Intent.EXTRA_STREAM, uri)
            putExtra(Intent.EXTRA_TITLE, name)
            clipData = ClipData.newUri(context.contentResolver, name, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
    }

    private fun safePath(path: String): Boolean = path.isNotEmpty() &&
        path.split('/').all { segment ->
            segment.isNotEmpty() && segment != "." && segment != ".." &&
                segment.none { it == '\\' || it == ':' || it.code < 32 }
        }
}
