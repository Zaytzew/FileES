# FileES autostart and daemon supervisor.
#
# Runs at logon. Keeps the daemon alive; starts the interface once and then
# leaves it alone - the daemon is a service and has to be running, but closing
# the window is the owner's decision and reopening it would be the interface
# arguing with him.
#
# Everything runs from this directory on purpose: config.json here is the
# production configuration, and the daemon reads it from the working directory.

$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here

$daemon = Join-Path $here 'filees.exe'
$gui = Join-Path $here 'filees-gui-wails.exe'
$logDir = Join-Path $here 'logs'
if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir | Out-Null }

function Write-Supervisor($text) {
    $line = "{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $text
    Add-Content -Path (Join-Path $logDir 'supervisor.log') -Value $line -Encoding utf8
}

function Test-DaemonRunning {
    # By image path rather than by name: a hand-built filees-rNNN.exe left over
    # from a session is still a daemon holding the socket, and starting a second
    # one would fail on the bind and look like a broken install.
    $procs = Get-CimInstance Win32_Process -Filter "Name LIKE 'filees%.exe'" -ErrorAction SilentlyContinue
    foreach ($p in $procs) {
        if ($p.CommandLine -and $p.CommandLine -match '\bdaemon\b') { return $true }
    }
    return $false
}

function Start-Daemon {
    # One log per start, never reused. The daemon writes its whole diagnosis to
    # stderr and a desktop install sends that nowhere - which is how a lock
    # release once shipped and never executed with nobody the wiser.
    #
    # Named by the minute rather than the day because -RedirectStandardError
    # truncates: a supervisor restart after a crash would overwrite the log of
    # the crash it was restarting from, which is the one file anybody would
    # want afterwards.
    $stamp = Get-Date -Format "yyyy-MM-dd_HHmmss"
    $err = Join-Path $logDir "daemon-$stamp.stderr.log"
    $out = Join-Path $logDir "daemon-$stamp.stdout.log"
    Start-Process -FilePath $daemon -ArgumentList 'daemon' -WorkingDirectory $here `
        -RedirectStandardError $err -RedirectStandardOutput $out -WindowStyle Hidden
    Write-Supervisor "daemon started"
}

if (-not (Test-DaemonRunning)) { Start-Daemon } else { Write-Supervisor "daemon already running" }

Start-Sleep -Seconds 5

# The interface, once. It attaches to the daemon socket, so it is started after
# the daemon rather than beside it.
$guiRunning = Get-Process -Name 'filees-gui-wails' -ErrorAction SilentlyContinue
if (-not $guiRunning) {
    Start-Process -FilePath $gui -WorkingDirectory $here
    Write-Supervisor "interface started"
}

# Supervision from here on. Fifteen seconds is a compromise: long enough that a
# daemon which cannot start does not spin, short enough that a crash during the
# owner's working day is invisible to him.
#
# Deliberately no restart cap. This machine is the live test, and a daemon that
# gave up after N attempts would leave his work unguarded silently - which is
# the failure shape this whole product exists to avoid.
while ($true) {
    Start-Sleep -Seconds 15
    if (-not (Test-DaemonRunning)) {
        Write-Supervisor "daemon is gone - restarting"
        try { Start-Daemon } catch { Write-Supervisor "restart failed: $_" }
    }
}
