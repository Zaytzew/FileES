package net.filees.mobile

import android.content.Context

/** Device-wide budget for the source bytes staged before sharing a folder ZIP. */
internal object SharePackageLimit {
    const val DEFAULT_MIB = 40
    const val MIN_MIB = 1
    const val MAX_MIB = 4096
    private const val PREFS = "filees_share"
    private const val KEY = "folder_package_limit_mib"
    private const val MIB = 1024L * 1024L

    fun mib(context: Context): Int {
        val saved = runCatching {
            context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getInt(KEY, DEFAULT_MIB)
        }.getOrDefault(DEFAULT_MIB)
        return saved.takeIf { it in MIN_MIB..MAX_MIB } ?: DEFAULT_MIB
    }

    fun bytes(context: Context): Long = mib(context).toLong() * MIB

    fun parse(text: String): Int? = text.trim().toIntOrNull()?.takeIf { it in MIN_MIB..MAX_MIB }

    fun save(context: Context, value: Int) {
        require(value in MIN_MIB..MAX_MIB)
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putInt(KEY, value).apply()
    }
}
