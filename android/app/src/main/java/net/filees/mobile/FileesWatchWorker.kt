package net.filees.mobile

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

class FileesWatchWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        try {
            FileesWatchTick.run(applicationContext)
        } catch (_: Exception) {
            // The next tick is still scheduled; a hard retry loop would pile work.
        }
        FileesWatchScheduler.scheduleNext(applicationContext)
        return Result.success()
    }
}

object FileesWatchScheduler {
    const val TICK_MINUTES = 5L
    private const val UNIQUE = "filees-watch-tick"

    fun ensure(context: Context) {
        WorkManager.getInstance(context).cancelUniqueWork("filees-watch-once")
        enqueue(context, delayMinutes = TICK_MINUTES, replace = false)
    }

    fun runSoon(context: Context) {
        enqueue(context, delayMinutes = 0, replace = true)
    }

    fun scheduleNext(context: Context) {
        enqueue(context, delayMinutes = TICK_MINUTES, replace = true)
    }

    private fun enqueue(context: Context, delayMinutes: Long, replace: Boolean) {
        val request = OneTimeWorkRequestBuilder<FileesWatchWorker>()
            .setInitialDelay(delayMinutes, TimeUnit.MINUTES)
            .setConstraints(
                Constraints.Builder()
                    .setRequiredNetworkType(NetworkType.CONNECTED)
                    .build(),
            )
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(
            UNIQUE,
            if (replace) ExistingWorkPolicy.REPLACE else ExistingWorkPolicy.KEEP,
            request,
        )
    }
}
