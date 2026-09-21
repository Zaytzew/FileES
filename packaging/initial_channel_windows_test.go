//go:build windows

package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsInitialChannelBeforeDaemon(t *testing.T) {
	// Execute the actual initialization function, never the supervisor loop.
	// The GUI process is a test double; real Wails acceptance is separate.
	source, err := filepath.Abs("windows/autostart-supervisor.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := `param($Source, $Root)
$ErrorActionPreference = 'Stop'
$here = $Root
$gui = Join-Path $Root 'filees-gui-wails.exe'
$script:calls = 0
$script:mode = 'cancel'
function Write-Supervisor($text) {}
function Start-Process {
  param($FilePath, $ArgumentList, $WorkingDirectory, [switch]$Wait, [switch]$PassThru)
  if ($FilePath -ne $gui -or $ArgumentList[0] -ne '--choose-update-channel') { throw 'wrong command' }
  $script:calls++
  if ($script:mode -eq 'cancel') { return [pscustomobject]@{ExitCode=1} }
  if ($script:mode -eq 'empty') { return [pscustomobject]@{ExitCode=0} }
  $path = $ArgumentList[1].Trim('"')
  $data = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
  $data | Add-Member -MemberType NoteProperty -Name update -Value @{enabled=$true;channel=$script:mode}
  [IO.File]::WriteAllText($path, ($data | ConvertTo-Json -Depth 6))
  return [pscustomobject]@{ExitCode=0}
}
$tokens=$null; $parseErrors=$null
$ast=[System.Management.Automation.Language.Parser]::ParseFile($Source,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count) { throw 'parse error' }
$fn=$ast.Find({param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Initialize-Configuration'}, $true)
Invoke-Expression $fn.Extent.Text
$config=Join-Path $Root 'config.json'
foreach ($mode in @('cancel','empty')) {
  $script:mode=$mode
  $failed=$false
  try { Initialize-Configuration } catch { $failed=$true }
  if (-not $failed -or (Test-Path $config)) { throw 'unfinished setup accepted' }
  if (Get-ChildItem -LiteralPath $Root -Filter '.filees-setup-*') { throw 'temporary configuration leaked' }
}
$script:mode='beta'
Initialize-Configuration
$before=[IO.File]::ReadAllText($config)
if (($before | ConvertFrom-Json).update.channel -ne 'beta') { throw 'beta not saved' }
$script:mode='alpha'
Initialize-Configuration
if ([IO.File]::ReadAllText($config) -cne $before -or $script:calls -ne 3) { throw 'existing configuration was changed' }
`
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "test.ps1")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path, source, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
