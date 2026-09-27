package net.filees.mobile

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import androidx.work.workDataOf
import java.util.concurrent.TimeUnit

class FileesWatchWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    private val cancellation = CaptureCancellation()
    override fun onStopped() { cancellation.cancel(); super.onStopped() }
    override fun doWork(): Result {
        try {
            if (isStopped) return Result.retry()
            FileesWatchTick.run(applicationContext, cancellation)
        } catch (_: Exception) {
            if (isStopped) return Result.retry()
        }
        if (!isStopped && inputData.getBoolean("reschedule", true)) FileesWatchScheduler.scheduleNext(applicationContext)
        return Result.success()
    }
}

object FileesWatchScheduler {
    const val TICK_MINUTES = 5L
    private const val UNIQUE = "filees-watch-tick"
    fun ensure(context: Context) {
        WorkManager.getInstance(context).cancelUniqueWork("filees-watch-once")
        enqueue(context, TICK_MINUTES, UNIQUE, ExistingWorkPolicy.KEEP, true)
    }
    fun runSoon(context: Context) {
        // A nudge cannot replace/cancel the live periodic chain.
        enqueue(context, 0, "$UNIQUE-soon", ExistingWorkPolicy.KEEP, false)
    }
    fun scheduleNext(context: Context) {
        // Append after this worker; REPLACE used to cancel the very worker
        // scheduling its successor while its synchronous I/O kept running.
        enqueue(context, TICK_MINUTES, UNIQUE, ExistingWorkPolicy.APPEND_OR_REPLACE, true)
    }
    private fun enqueue(context: Context, delayMinutes: Long, name: String, policy: ExistingWorkPolicy, reschedule: Boolean) {
        val request = OneTimeWorkRequestBuilder<FileesWatchWorker>()
            .setInitialDelay(delayMinutes, TimeUnit.MINUTES)
            .setInputData(workDataOf("reschedule" to reschedule))
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(name, policy, request)
    }
}
