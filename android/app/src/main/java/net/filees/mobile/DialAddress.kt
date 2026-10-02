package net.filees.mobile

import android.content.Context
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress

/**
 * Resolve host:port with Android's libc DNS (the same path JuiceSSH uses).
 * Go's net.Resolver on mobile data often returns "no such host" for names
 * that Android itself resolves fine.
 *
 * A paired server keeps the last address that actually completed a handshake.
 * The key is the server id plus the paired host:port, so a new pairing address
 * does not reuse the previous machine. Callers remember only after the pinned
 * host key has matched.
 */
object DialAddress {
    private const val CACHE_PREFS = "filees_dial_cache"

    fun cached(context: Context, serverId: String, pairedAddress: String): String? {
        if (serverId.isBlank() || pairedAddress.isBlank()) return null
        return context.getSharedPreferences(CACHE_PREFS, Context.MODE_PRIVATE)
            .getString(cacheKey(serverId, pairedAddress), null)
            ?.takeIf { it.isNotBlank() }
    }

    fun remember(context: Context, serverId: String, pairedAddress: String, dialAddress: String) {
        if (serverId.isBlank() || pairedAddress.isBlank() || dialAddress.isBlank()) return
        context.getSharedPreferences(CACHE_PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(cacheKey(serverId, pairedAddress), dialAddress)
            .apply()
    }

    private fun cacheKey(serverId: String, pairedAddress: String) = serverId + "\u0000" + pairedAddress

    fun resolve(address: String): String {
        val (host, port) = splitHostPort(address)
        if (host.isBlank() || port.isBlank()) {
            throw IllegalArgumentException("oczekiwane host:port, jest: $address")
        }
        val answers = InetAddress.getAllByName(host)
        val chosen = answers.firstOrNull { it is Inet4Address } ?: answers.firstOrNull()
            ?: throw java.net.UnknownHostException(host)
        val ip = chosen.hostAddress ?: host
        return if (chosen is Inet6Address) "[$ip]:$port" else "$ip:$port"
    }

    private fun splitHostPort(address: String): Pair<String, String> {
        if (address.startsWith("[")) {
            val end = address.indexOf(']')
            if (end > 1 && end + 1 < address.length && address[end + 1] == ':') {
                return address.substring(1, end) to address.substring(end + 2)
            }
        }
        val colon = address.lastIndexOf(':')
        if (colon <= 0 || colon == address.lastIndex) {
            return address to ""
        }
        return address.substring(0, colon) to address.substring(colon + 1)
    }
}
