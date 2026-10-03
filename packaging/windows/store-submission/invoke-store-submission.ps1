# Local-only launcher. Credential is a user/machine-bound DPAPI CLIXML file.
param(
    [Parameter(Mandatory=$true)][string]$Python,
    [Parameter(ValueFromRemainingArguments=$true)][string[]]$ToolArgs
)
$ErrorActionPreference = 'Stop'
$privateDir = Join-Path $env:LOCALAPPDATA 'FileES/StoreSubmission'
$configPath = Join-Path $privateDir 'account.json'
$credentialPath = Join-Path $privateDir 'credential.clixml'
if (-not (Test-Path -LiteralPath $configPath) -or -not (Test-Path -LiteralPath $credentialPath)) {
    throw 'Local account configuration is missing; see README.md. No Partner Center request was made.'
}
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
if ($config.tenantId -notmatch '^[0-9a-fA-F-]{36}$') { throw 'Invalid local tenant ID' }
$credential = Import-Clixml -LiteralPath $credentialPath
if (-not ($credential -is [pscredential])) { throw 'Credential file has an unexpected format' }
$clientId = $credential.UserName
if ($clientId -notmatch '^[0-9a-fA-F-]{36}$') { throw 'Invalid local client ID' }
$secret = $credential.GetNetworkCredential().Password
try {
    $token = Invoke-RestMethod -Method Post -Uri "https://login.microsoftonline.com/$($config.tenantId)/oauth2/token" -Body @{
        grant_type = 'client_credentials'
        client_id = $clientId
        client_secret = $secret
        resource = 'https://manage.devcenter.microsoft.com'
    } -ContentType 'application/x-www-form-urlencoded'
    if (-not $token.access_token) { throw 'Microsoft did not return an access token' }
    $env:FILEES_STORE_ACCESS_TOKEN = $token.access_token
    & $Python (Join-Path $PSScriptRoot 'store_submission.py') @ToolArgs
    exit $LASTEXITCODE
} catch {
    # Never display the HTTP error body: it may include account information.
    throw 'Authentication or Store operation failed; inspect only the redacted tool output.'
} finally {
    Remove-Item Env:FILEES_STORE_ACCESS_TOKEN -ErrorAction SilentlyContinue
    $secret = $null
}
