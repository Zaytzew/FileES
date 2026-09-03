' Launches the FileES supervisor with no window at all.
'
' Task Scheduler running powershell.exe -WindowStyle Hidden still leaves a
' console on screen: the console host is created before the style is applied,
' so the flag hides nothing. WScript.Shell.Run with intWindowStyle 0 never
' creates one, which is why this shim exists rather than another flag.
'
' The third argument is False: do not wait. The supervisor loops for the life of
' the session and this script has nothing left to do once it has started it.
Option Explicit
Dim shell, here, command
Set shell = CreateObject("WScript.Shell")
here = Left(WScript.ScriptFullName, InStrRev(WScript.ScriptFullName, "\") - 1)
command = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File """ & here & "\start-filees.ps1"""
shell.Run command, 0, False
