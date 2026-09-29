package net.filees.mobile

/** Only durable completion of ALL source markers permits receipt retention. */
internal object ReceiptAcknowledgement {
    fun record(sources: List<String>, markSeen: (String) -> Unit, acknowledge: () -> Unit) {
        sources.forEach(markSeen)
        acknowledge()
    }
}
