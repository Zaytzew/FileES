' Launches the FileES supervisor with no window at all.
'
' Compatibility for shortcuts created before the native Go launcher.
' It directly starts that launcher; no interpreter chain or PowerShell.
'
' The third argument is False: do not wait. The supervisor loops for the life of
' the session and this script has nothing left to do once it has started it.
Option Explicit
Dim shell, here, command
Set shell = CreateObject("WScript.Shell")
here = Left(WScript.ScriptFullName, InStrRev(WScript.ScriptFullName, "\") - 1)
command = """" & here & "\filees-launch.exe"""
If WScript.Arguments.Count > 1 Then WScript.Quit 2
If WScript.Arguments.Count = 1 Then
    If WScript.Arguments(0) <> "--show" Then WScript.Quit 2
    command = command & " --show"
End If
shell.Run command, 0, False
