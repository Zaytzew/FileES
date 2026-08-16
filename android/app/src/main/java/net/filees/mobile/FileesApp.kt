package net.filees.mobile

import android.app.Application

class FileesApp : Application() {
    override fun onCreate() {
        super.onCreate()
        TreeZip.sweep(cacheDir)
        FileesWatchScheduler.ensure(this)
    }
}
