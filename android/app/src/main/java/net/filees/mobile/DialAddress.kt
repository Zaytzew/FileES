package net.filees.mobile

import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress

/**
 * Resolve host:port with Android's libc DNS (the same path JuiceSSH uses).
 * Go's net.Resolver on mobile data often returns "no such host" for names
 * that Android itself resolves fine.
 */
object DialAddress {
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
