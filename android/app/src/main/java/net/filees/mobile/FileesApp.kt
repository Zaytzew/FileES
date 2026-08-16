package net.filees.mobile

import android.app.Application

class FileesApp : Application() {
    override fun onCreate() {
        super.onCreate()
        FileesWatchScheduler.ensure(this)
    }
}
