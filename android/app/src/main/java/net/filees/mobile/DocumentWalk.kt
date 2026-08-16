package net.filees.mobile

import android.content.ContentResolver
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.OpenableColumns

data class WalkedFile(
    val uri: Uri,
    val relativeDir: String,
    val filename: String,
    val contentType: String,
)

object DocumentWalk {
    fun single(resolver: ContentResolver, uri: Uri): WalkedFile {
        var name = uri.lastPathSegment ?: "upload.bin"
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
            val index = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            if (index >= 0 && cursor.moveToFirst()) {
                name = cursor.getString(index) ?: name
            }
        }
        return WalkedFile(uri, "", name, resolver.getType(uri).orEmpty())
    }

    fun tree(resolver: ContentResolver, treeUri: Uri): List<WalkedFile> {
        val rootId = DocumentsContract.getTreeDocumentId(treeUri)
        val rootName = queryName(resolver, DocumentsContract.buildDocumentUriUsingTree(treeUri, rootId))
            ?: "folder"
        return walk(resolver, treeUri, rootId, rootName)
    }

    private fun walk(resolver: ContentResolver, treeUri: Uri, parentId: String, relDir: String): List<WalkedFile> {
        val children = DocumentsContract.buildChildDocumentsUriUsingTree(treeUri, parentId)
        val out = mutableListOf<WalkedFile>()
        val projection = arrayOf(
            DocumentsContract.Document.COLUMN_DOCUMENT_ID,
            DocumentsContract.Document.COLUMN_DISPLAY_NAME,
            DocumentsContract.Document.COLUMN_MIME_TYPE,
        )
        resolver.query(children, projection, null, null, null)?.use { cursor ->
            val idCol = cursor.getColumnIndex(DocumentsContract.Document.COLUMN_DOCUMENT_ID)
            val nameCol = cursor.getColumnIndex(DocumentsContract.Document.COLUMN_DISPLAY_NAME)
            val mimeCol = cursor.getColumnIndex(DocumentsContract.Document.COLUMN_MIME_TYPE)
            while (cursor.moveToNext()) {
                val id = cursor.getString(idCol) ?: continue
                val name = cursor.getString(nameCol) ?: continue
                if (name.startsWith(".")) continue
                val mime = cursor.getString(mimeCol).orEmpty()
                if (mime == DocumentsContract.Document.MIME_TYPE_DIR) {
                    out += walk(resolver, treeUri, id, "$relDir/$name")
                } else {
                    out += WalkedFile(
                        uri = DocumentsContract.buildDocumentUriUsingTree(treeUri, id),
                        relativeDir = relDir,
                        filename = name,
                        contentType = mime,
                    )
                }
            }
        }
        return out
    }

    private fun queryName(resolver: ContentResolver, uri: Uri): String? {
        resolver.query(uri, arrayOf(DocumentsContract.Document.COLUMN_DISPLAY_NAME), null, null, null)?.use { cursor ->
            if (cursor.moveToFirst()) {
                return cursor.getString(0)
            }
        }
        return null
    }
}
