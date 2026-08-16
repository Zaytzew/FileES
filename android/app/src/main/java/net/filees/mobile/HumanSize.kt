package net.filees.mobile

import java.util.Locale

object HumanSize {
    fun format(bytes: Long): String {
        if (bytes < 1024) return "$bytes B"
        val units = arrayOf("KB", "MB", "GB", "TB")
        var value = bytes.toDouble() / 1024.0
        var i = 0
        while (value >= 1024.0 && i < units.lastIndex) {
            value /= 1024.0
            i++
        }
        val pattern = if (value >= 10) "%.0f %s" else "%.1f %s"
        return String.format(Locale("pl", "PL"), pattern, value, units[i])
    }
}
