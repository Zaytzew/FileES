@echo off
rem Update the working copy, then deploy filees.space (tools\deploy-site.sh)
rem through Git Bash. Works from any directory, cmd or PowerShell:
rem   E:\!!!_COOPERATE\FILEES\tools\deploy-site.cmd [-y] [--dry-run]
setlocal DisableDelayedExpansion
set "here=%~dp0"
svn update "%here%.." || exit /b 1
rem Git Bash needs forward slashes to find the repository from the script path.
set "script=%here:\=/%deploy-site.sh"
"C:\Program Files\Git\bin\bash.exe" "%script%" %*
exit /b %ERRORLEVEL%
