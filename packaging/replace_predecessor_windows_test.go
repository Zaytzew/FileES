//go:build windows

package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The real Resolve-StorePredecessor from the MSI supervisor, with the Store
// package and the GUI replaced by doubles. PowerShell resolves a function
// before a cmdlet of the same name, so the double stands in for
// Get-AppxPackage without touching the machine's packages.
func TestWindowsSupervisorResolvesStorePredecessor(t *testing.T) {
	source, err := filepath.Abs("windows/autostart-supervisor.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := `param($Source, $Root)
$ErrorActionPreference = 'Stop'
$here = $Root
$gui = Join-Path $Root 'filees-gui-wails.exe'
$script:installed = $false
$script:answer = 'decline'
$script:asked = 0
function Write-Supervisor($text) {}
function Get-AppxPackage {
  param($Name, $ErrorAction)
  if ($Name -ne 'FileES.FileESDesktop') { throw "wrong package $Name" }
  if ($script:installed) { return [pscustomobject]@{Name=$Name} }
}
function Start-Process {
  param($FilePath, $ArgumentList, $WorkingDirectory, [switch]$Wait, [switch]$PassThru)
  $expected = @('--replace-predecessor', 'store', '--config', ('"' + (Join-Path $Root 'config.json') + '"'))
  if ($FilePath -ne $gui -or (Compare-Object $ArgumentList $expected -SyncWindow 0)) { throw 'wrong command' }
  $script:asked++
  switch ($script:answer) {
    'decline' { return [pscustomobject]@{ExitCode=1} }
    'remove'  { $script:installed = $false; return [pscustomobject]@{ExitCode=0} }
    'claim'   { return [pscustomobject]@{ExitCode=0} }
  }
}
$tokens=$null; $parseErrors=$null
$ast=[System.Management.Automation.Language.Parser]::ParseFile($Source,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count) { throw 'parse error' }
$fn=$ast.Find({param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Resolve-StorePredecessor'}, $true)
Invoke-Expression $fn.Extent.Text

# No Store package: start the MSI pair, ask nothing.
$ShowGUI = $true
if (-not (Resolve-StorePredecessor) -or $script:asked -ne 0) { throw 'asked without a Store package' }

# Logon autostart with the Store package present: yield, ask nothing.
$script:installed = $true
$ShowGUI = $false
if ((Resolve-StorePredecessor) -or $script:asked -ne 0) { throw 'autostart did not yield silently' }

# Opened by the user and declined: keep the Store version, start nothing.
$ShowGUI = $true
if ((Resolve-StorePredecessor) -or $script:asked -ne 1 -or -not $script:installed) { throw 'declined removal was not respected' }

# A reported success while the package is still there must not start a second pair.
$script:answer = 'claim'
$failed = $false
try { Resolve-StorePredecessor | Out-Null } catch { $failed = $true }
if (-not $failed) { throw 'package left behind was accepted' }

# Confirmed and removed: the MSI pair may start.
$script:answer = 'remove'
if (-not (Resolve-StorePredecessor) -or $script:installed) { throw 'removal not followed through' }
`
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "test.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path, source, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
