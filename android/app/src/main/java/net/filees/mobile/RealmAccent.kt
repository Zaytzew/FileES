package net.filees.mobile

import android.graphics.Color

object RealmAccent {
    const val DEFAULT = "#FF6A00"

    fun parse(raw: String?): Int {
        val value = raw?.trim().orEmpty()
        return try {
            if (value.matches(Regex("^#[0-9A-Fa-f]{6}$"))) Color.parseColor(value)
            else Color.parseColor(DEFAULT)
        } catch (_: Exception) {
            Color.parseColor(DEFAULT)
        }
    }
}
