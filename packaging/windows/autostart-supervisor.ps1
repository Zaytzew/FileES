# FileES autostart and daemon supervisor.
#
# Runs at logon. Keeps the daemon alive; starts the interface once and then
# leaves it alone - the daemon is a service and has to be running, but closing
# the window is the owner's decision and reopening it would be the interface
# arguing with him.
#
# Everything runs from this directory on purpose: config.json here is the
# production configuration, and the daemon reads it from the working directory.

param([switch]$ShowGUI)

$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here

$daemon = Join-Path $here 'filees.exe'
$gui = Join-Path $here 'filees-gui-wails.exe'
$logDir = Join-Path $here 'logs'
[System.IO.Directory]::CreateDirectory($logDir) | Out-Null

function Write-Supervisor($text) {
    $line = "{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $text
    Add-Content -Path (Join-Path $logDir 'supervisor.log') -Value $line -Encoding utf8
}

function Initialize-Configuration {
    # A fresh install has no config.json, and the daemon refuses to start
    # without one - config.Load treats a missing file as fatal, and the
    # transport paths must be absolute. So the first run writes the minimum
    # that lets the daemon come up and the interface offer activation.
    #
    # It is written here rather than by the installer because the content
    # depends on who is logged in, and an MSI cannot expand a user's home
    # directory into the body of a file. Written once and never again: after
    # this it is the owner's production configuration and nothing of ours
    # touches it.
    $config = Join-Path $here 'config.json'
    if (Test-Path $config) { return }
    $state = Join-Path $env:USERPROFILE '.local\share\filees'
    $seed = [ordered]@{
        transport = [ordered]@{
            identity_file = (Join-Path $state 'identity\id_ed25519')
            known_hosts   = (Join-Path $state 'known_hosts')
        }
        repositories = @()
    }
    $json = $seed | ConvertTo-Json -Depth 4
    [System.IO.File]::WriteAllText($config, $json, (New-Object System.Text.UTF8Encoding($false)))
    Write-Supervisor "wrote the initial configuration"
}

function Find-Daemon {
    # A process in another user's session must never satisfy our startup gate.
    $session = [System.Diagnostics.Process]::GetCurrentProcess().SessionId
    $procs = Get-CimInstance Win32_Process -Filter "Name LIKE 'filees%.exe'" -ErrorAction Stop
    foreach ($p in $procs) {
        if ($p.SessionId -ne $session -or -not $p.CommandLine -or $p.CommandLine -notmatch '\bdaemon\b') { continue }
        $process = Get-Process -Id $p.ProcessId -ErrorAction SilentlyContinue
        if ($process) {
            # Open the handle now so ExitCode remains available after exit,
            # including when adopting a daemon started by an earlier launcher.
            $null = $process.Handle
            return $process
        }
    }
    return $null
}

function Show-Interface {
    # Wails' existing single-instance handler raises the running window.
    Start-Process -FilePath $gui -WorkingDirectory $here
    Write-Supervisor "interface requested"
}

function Start-Daemon {
    # One log per start, never reused. The daemon writes its whole diagnosis to
    # stderr and a desktop install sends that nowhere - which is how a lock
    # release once shipped and never executed with nobody the wiser.
    #
    # Named by the start time and launcher PID rather than the day because -RedirectStandardError
    # truncates: a supervisor restart after a crash would overwrite the log of
    # the crash it was restarting from, which is the one file anybody would
    # want afterwards.
    $stamp = (Get-Date -Format "yyyy-MM-dd_HHmmss_fff") + "-$PID"
    $err = Join-Path $logDir "daemon-$stamp.stderr.log"
    $out = Join-Path $logDir "daemon-$stamp.stdout.log"
    $process = Start-Process -FilePath $daemon -ArgumentList 'daemon' -WorkingDirectory $here `
        -RedirectStandardError $err -RedirectStandardOutput $out -WindowStyle Hidden -PassThru
    $null = $process.Handle
    Write-Supervisor "daemon started"
    return $process
}

# One supervisor per interactive user session, shared across repeated clicks.
# The mutex is held for the full lifetime of the supervisor, including sleeps.
$mutex = New-Object System.Threading.Mutex($false, 'Local\FileESDesktopMSISupervisor')
$ownsMutex = $false
try {
    try { $ownsMutex = $mutex.WaitOne(0) } catch [System.Threading.AbandonedMutexException] { $ownsMutex = $true }
    if (-not $ownsMutex) {
        if (-not $ShowGUI) { return }
        $running = Find-Daemon
        if ($running) {
            $running.Dispose()
            Show-Interface
            return
        }
        # A click just after Quit may meet the old supervisor during its
        # final polling interval. Wait for it to release ownership, then start
        # the whole pair; showing GUI alone would reproduce the original bug.
        try { $ownsMutex = $mutex.WaitOne(20000) } catch [System.Threading.AbandonedMutexException] { $ownsMutex = $true }
        if (-not $ownsMutex) { throw 'FileES supervisor did not release a stopped daemon; see supervisor.log' }
    }
    Initialize-Configuration
    $tracked = Find-Daemon
    if (-not $tracked) { $tracked = Start-Daemon } else { Write-Supervisor "daemon adopted" }
    Start-Sleep -Seconds 5
    if ($ShowGUI -or -not (Get-Process -Name 'filees-gui-wails' -ErrorAction SilentlyContinue)) { Show-Interface }

    while ($true) {
        Start-Sleep -Seconds 15
        if (-not $tracked.HasExited) { continue }
        # A daemon restart launches its replacement before the old process
        # exits. Adopt that replacement before interpreting the exit code.
        $replacement = Find-Daemon
        if ($replacement) {
            $tracked.Dispose()
            $tracked = $replacement
            Write-Supervisor "replacement daemon adopted"
            continue
        }
        if ($tracked.ExitCode -eq 0) {
            Write-Supervisor "daemon stopped normally - supervisor exits"
            break
        }
        Write-Supervisor "daemon failed - restarting"
        try {
            $next = Start-Daemon
            $tracked.Dispose()
            $tracked = $next
        } catch { Write-Supervisor "restart failed: $_" }
    }
} finally {
    if ($tracked) { $tracked.Dispose() }
    if ($ownsMutex) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}
