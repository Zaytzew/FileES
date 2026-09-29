package net.filees.mobile

/** UI-thread only. Navigation and newer requests retire old callbacks, not I/O. */
internal class UiRequests(private val context: () -> Any? = { null }) {
    class Ticket internal constructor(internal val context: Any?)
    private val latest = mutableMapOf<String, Ticket>()
    private var busyOwner: Ticket? = null

    fun begin(lane: String): Ticket = Ticket(context()).also { latest[lane] = it }
    fun current(ticket: Ticket): Boolean = ticket.context == context() && latest.values.any { it === ticket }
    fun ownBusy(ticket: Ticket) { busyOwner = ticket }
    fun ownsBusy(ticket: Ticket): Boolean = current(ticket) && busyOwner === ticket
    fun clearBusy() { busyOwner = null }
    fun invalidate() { latest.clear(); clearBusy() }
}

/** Destination chosen on the UI thread; background work must not read selection. */
internal data class CaptureDestination<C>(val client: C, val repoId: String)
