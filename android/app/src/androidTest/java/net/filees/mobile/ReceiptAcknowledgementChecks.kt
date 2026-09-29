package net.filees.mobile

internal object ReceiptAcknowledgementChecks {
    fun run() {
        val seen = linkedSetOf<String>()
        var ack = false
        check(runCatching {
            ReceiptAcknowledgement.record(listOf("a", "b"), {
                if (it == "b") error("storage full")
                seen += it
            }) { ack = true }
        }.isFailure)
        check(!ack && seen == setOf("a"))
        // Restart/retry repeats harmless markers, acknowledges only after all.
        ReceiptAcknowledgement.record(listOf("a", "b"), { seen += it }) {
            check(seen == setOf("a", "b")); ack = true
        }
        check(ack)
        check(runCatching {
            ReceiptAcknowledgement.record(listOf("a", "b"), { seen += it }) { error("ACK write failed") }
        }.isFailure)
        check(seen == setOf("a", "b"))
        println("ReceiptAcknowledgement: partial marker failure, restart order, ACK failure PASS")
    }
}

fun main() = ReceiptAcknowledgementChecks.run()
