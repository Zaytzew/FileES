package net.filees.mobile

/** Uploads land under the picked share at mobile-uploads/<actual load>. */
object UploadPaths {
    const val ROOT = "mobile-uploads"

    fun parent(relativeDir: String): String {
        val tail = relativeDir.trim('/')
        return if (tail.isEmpty()) ROOT else "$ROOT/$tail"
    }
}
