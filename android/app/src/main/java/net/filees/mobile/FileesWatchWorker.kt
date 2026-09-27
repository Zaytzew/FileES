package net.filees.mobile

import android.content.Context
import android.os.Build
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkInfo
import androidx.work.Worker
import androidx.work.WorkerParameters
import androidx.work.workDataOf
import java.util.concurrent.TimeUnit

class FileesWatchWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    private val cancellation = CaptureCancellation()
    override fun onStopped() {
        val reason = if (Build.VERSION.SDK_INT >= 31) stopReason else WorkInfo.STOP_REASON_UNKNOWN
        android.util.Log.i("FileesWatch", "Worker stopped: reason=$reason")
        cancellation.cancel(reason)
        super.onStopped()
    }
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
    private val migrationExecutor = java.util.concurrent.Executors.newSingleThreadExecutor()
    private val migrating = java.util.concurrent.atomic.AtomicBoolean(false)
    fun ensure(context: Context) {
        WorkManager.getInstance(context).cancelUniqueWork("filees-watch-once")
        enqueue(context, TICK_MINUTES, UNIQUE, ExistingWorkPolicy.KEEP, true)
        migrateBackoff(context.applicationContext)
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
    internal fun request(delayMinutes: Long, reschedule: Boolean, id: java.util.UUID = java.util.UUID.randomUUID()) =
        OneTimeWorkRequestBuilder<FileesWatchWorker>()
            .setId(id)
            .setInitialDelay(delayMinutes, TimeUnit.MINUTES)
            .setBackoffCriteria(BackoffPolicy.LINEAR, 30, TimeUnit.SECONDS)
            .setInputData(workDataOf("reschedule" to reschedule))
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .build()

    private fun enqueue(context: Context, delayMinutes: Long, name: String, policy: ExistingWorkPolicy, reschedule: Boolean) {
        WorkManager.getInstance(context).enqueueUniqueWork(name, policy, request(delayMinutes,reschedule))
    }

    private fun migrateBackoff(context: Context) {
        val prefs = context.getSharedPreferences("filees_watch_scheduler", Context.MODE_PRIVATE)
        if (prefs.getBoolean("linear_backoff_v1",false) || !migrating.compareAndSet(false,true)) return
        migrationExecutor.execute {
            try {
                val manager = WorkManager.getInstance(context)
                for ((name,periodic) in listOf(UNIQUE to true, "$UNIQUE-soon" to false)) {
                    for (info in manager.getWorkInfosForUniqueWork(name).get().filter { !it.state.isFinished }) {
                        // updateWork preserves ID, attempts and enqueue time; a running
                        // generation finishes unchanged. Never cancel a live upload.
                        val reason = if (Build.VERSION.SDK_INT >= 31) info.stopReason else WorkInfo.STOP_REASON_UNKNOWN
                        val updated = manager.updateWork(request(if(periodic) TICK_MINUTES else 0,periodic,info.id)).get()
                        android.util.Log.i("FileesWatch", "Retry policy: id=${info.id} attempts=${info.runAttemptCount} stopReason=$reason previousEarliest=${info.nextScheduleTimeMillis} update=$updated")
                    }
                }
                prefs.edit().putBoolean("linear_backoff_v1",true).commit()
            } catch (e: Exception) {
                android.util.Log.w("FileesWatch", "Could not update retry policy",e)
            } finally { migrating.set(false) }
        }
    }
}
