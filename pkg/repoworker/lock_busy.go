package repoworker

import "errors"

var ErrFileLockBusy = errors.New("repository worker lock is busy; retry next scheduled pass")
