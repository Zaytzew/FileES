package net.filees.mobile

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/** Host-runnable policy tests; not a replacement for Activity/device acceptance. */
internal object UiRequestChecks {
    @JvmStatic fun main(args: Array<String>) {
        run()
        println("UiRequestChecks: 7 cases PASS")
    }

    fun run() {
        newerRequestRetiresOldSuccessAndError()
        navigationDoesNotResurrectOldRequest()
        independentLanesPreserveBusyOwner()
        oldProgressCannotTakeBackBusy()
        destructionRetiresAllLanes()
        sessionChangeRetiresCallbackBeforeResume()
        queuedCaptureKeepsOriginalDestination()
    }

    private fun newerRequestRetiresOldSuccessAndError() {
        val requests = UiRequests()
        val old = requests.begin("browse")
        requests.ownBusy(old)
        val newer = requests.begin("browse")
        requests.ownBusy(newer)
        check(!requests.current(old)) // both success and error callbacks use this gate
        check(!requests.ownsBusy(old))
        check(requests.current(newer) && requests.ownsBusy(newer))
    }

    private fun navigationDoesNotResurrectOldRequest() {
        val requests = UiRequests()
        val firstA = requests.begin("browse")
        requests.invalidate() // A -> B
        val b = requests.begin("browse")
        requests.invalidate() // B -> A, same repo and path as before
        val secondA = requests.begin("browse")
        requests.ownBusy(secondA)
        check(!requests.current(firstA) && !requests.current(b))
        check(requests.current(secondA) && requests.ownsBusy(secondA))
    }

    private fun independentLanesPreserveBusyOwner() {
        val requests = UiRequests()
        val projection = requests.begin("projection")
        requests.ownBusy(projection)
        val decisions = requests.begin("decisions")
        check(requests.current(projection) && requests.current(decisions))
        check(requests.ownsBusy(projection) && !requests.ownsBusy(decisions))
        val capture = requests.begin("capture")
        requests.ownBusy(capture)
        // Projection may still render, but cannot dismiss capture's spinner/error.
        check(requests.current(projection) && !requests.ownsBusy(projection))
        check(requests.ownsBusy(capture))
    }

    private fun oldProgressCannotTakeBackBusy() {
        val requests = UiRequests()
        val capture = requests.begin("capture")
        requests.ownBusy(capture)
        requests.clearBusy() // another UI operation, even one without a ticket
        check(requests.current(capture) && !requests.ownsBusy(capture))
        // requestBusy must test ownsBusy before changing text or ownership.
        val browse = requests.begin("browse")
        requests.ownBusy(browse)
        check(!requests.ownsBusy(capture) && requests.ownsBusy(browse))
        requests.clearBusy()
        check(!requests.ownsBusy(browse))
    }

    private fun destructionRetiresAllLanes() {
        val requests = UiRequests()
        val tickets = listOf("activation", "projection", "browse", "capture", "decisions")
            .map { requests.begin(it) }
        requests.ownBusy(tickets.last())
        requests.invalidate()
        check(tickets.none { requests.current(it) || requests.ownsBusy(it) })
    }

    private fun queuedCaptureKeepsOriginalDestination() {
        val clientA = Any()
        val clientB = Any()
        var selection = CaptureDestination(clientA, "repo-A")
        val target = selection // UI click, before SAF enumeration / coordinator wait
        val started = CountDownLatch(1)
        val release = CountDownLatch(1)
        val sent = AtomicReference<CaptureDestination<Any>>()
        val failure = AtomicReference<Throwable>()
        val worker = Thread {
            try {
                started.countDown()
                check(release.await(5, TimeUnit.SECONDS))
                sent.set(target)
            } catch (t: Throwable) { failure.set(t) }
        }
        worker.start()
        try {
            check(started.await(5, TimeUnit.SECONDS))
            selection = CaptureDestination(clientB, "repo-B")
        } finally {
            release.countDown()
            worker.join(5000)
        }
        check(!worker.isAlive)
        failure.get()?.let { throw it }
        check(selection.client === clientB && selection.repoId == "repo-B")
        check(sent.get().client === clientA && sent.get().repoId == "repo-A")
    }

    private fun sessionChangeRetiresCallbackBeforeResume() {
        var session = Triple("id-A", "host-A", "key-A")
        val requests = UiRequests { session }
        val old = requests.begin("projection")
        requests.ownBusy(old)
        session = Triple("id-A", "host-A", "key-new")
        check(!requests.current(old) && !requests.ownsBusy(old))
        requests.invalidate() // onResume observes the different session
        session = Triple("id-A", "host-A", "key-A")
        check(!requests.current(old))
        val current = requests.begin("projection")
        session = Triple("id-B", "host-A", "key-A")
        check(!requests.current(current))
    }
}
