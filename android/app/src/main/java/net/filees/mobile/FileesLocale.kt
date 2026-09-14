package net.filees.mobile

import android.app.Activity
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import java.util.Locale

object FileesLocale {
    const val PREF_LANGUAGE = "language"
    const val SYSTEM = "system"
    val CODES = listOf("pl", "en", "fr", "de", "es")

    fun wrap(base: Context): Context {
        val tag = resolve(preference(base), systemLanguage())
        val locale = Locale.forLanguageTag(tag)
        Locale.setDefault(locale)
        val config = Configuration(base.resources.configuration)
        config.setLocale(locale)
        return base.createConfigurationContext(config)
    }

    fun preference(ctx: Context): String {
        val raw = ctx.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
            .getString(PREF_LANGUAGE, SYSTEM) ?: SYSTEM
        return if (raw in CODES) raw else SYSTEM
    }

    fun set(ctx: Context, code: String) {
        val value = if (code in CODES) code else SYSTEM
        ctx.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(PREF_LANGUAGE, value)
            .apply()
    }

    fun mismatch(ctx: Context): Boolean =
        resolve(preference(ctx), systemLanguage()) != applied(ctx)

    fun applyChoice(activity: Activity, code: String) {
        set(activity, code)
        activity.recreate()
    }

    fun displayName(ctx: Context, code: String): String = when (code) {
        SYSTEM -> {
            val native = nativeName(resolve(SYSTEM, systemLanguage()))
            ctx.getString(R.string.settings_language_system_current, native)
        }
        else -> nativeName(code)
    }

    fun pickerCodes(): List<String> = listOf(SYSTEM) + CODES

    fun pickerLabel(ctx: Context, code: String): String =
        if (code == SYSTEM) ctx.getString(R.string.settings_language_system) else nativeName(code)

    private fun nativeName(code: String): String = when (code) {
        "pl" -> "Polski"
        "en" -> "English"
        "fr" -> "Français"
        "de" -> "Deutsch"
        "es" -> "Español"
        else -> code
    }

    private fun resolve(pref: String, system: String): String {
        if (pref in CODES) return pref
        val primary = system.lowercase().substringBefore('-').substringBefore('_')
        return if (primary in CODES) primary else "en"
    }

    private fun systemLanguage(): String {
        val locales = Resources.getSystem().configuration.locales
        return if (locales.isEmpty) "en" else locales[0].language
    }

    private fun applied(ctx: Context): String {
        val locales = ctx.resources.configuration.locales
        return if (locales.isEmpty) "en" else locales[0].language
    }
}
