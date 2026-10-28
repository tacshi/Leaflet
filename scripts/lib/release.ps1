# Shared by the Windows build scripts; dot-source it. The PowerShell
# counterpart of release.sh, without python3.

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# The script that dot-sourced this one, which names its errors.
$ScriptName = if ($MyInvocation.PSCommandPath) { Split-Path -Leaf $MyInvocation.PSCommandPath } else { 'release.ps1' }

function Fail([string]$Message) {
	[Console]::Error.WriteLine("${ScriptName}: $Message")
	exit 1
}
function Info([string]$Message) { Write-Host "==> $Message" }
function Warn([string]$Message) { [Console]::Error.WriteLine("warning: $Message") }

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$ConfigPath = Join-Path $RootDir 'mygo.json'
$ConfigBackup = $null

# Need fails unless each command is on PATH.
function Need([string[]]$Tools) {
	foreach ($tool in $Tools) {
		if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { Fail "needs $tool" }
	}
}

# Check-Version fails unless $Version looks like 1.2.3.
function Check-Version([string]$Version) {
	if ($Version -notmatch '^\d+\.\d+\.\d+$') { Fail "the version must look like 1.2.3, not $Version" }
}

# Get-ConfigValue returns the value in mygo.json at the dotted keys $Key,
# as in "version" or "updates.github".
function Get-ConfigValue([string]$Key) {
	$value = Get-Content -Raw $ConfigPath | ConvertFrom-Json
	foreach ($part in $Key.Split('.')) { $value = $value.$part }
	$value
}

# Set-Config changes mygo.json for this run only: each argument is
# <dotted key>=<value>, which sets the value, or -<dotted key>, which
# removes the key. The first call keeps the committed file, which
# Restore-Config puts back; call it from a finally block.
function Set-Config([string[]]$Pairs) {
	if (-not $script:ConfigBackup) {
		$script:ConfigBackup = [IO.File]::ReadAllBytes($ConfigPath)
	}
	$config = Get-Content -Raw $ConfigPath | ConvertFrom-Json
	foreach ($pair in $Pairs) {
		$remove = $pair.StartsWith('-')
		$keys, $value = $pair.TrimStart('-').Split('=', 2)
		$keys = $keys.Split('.')
		$node = $config
		foreach ($key in $keys | Select-Object -SkipLast 1) {
			if (-not $node.PSObject.Properties[$key]) {
				$node | Add-Member -NotePropertyName $key -NotePropertyValue ([pscustomobject]@{})
			}
			$node = $node.$key
		}
		$last = $keys[-1]
		if ($remove) {
			$node.PSObject.Properties.Remove($last)
		} elseif ($node.PSObject.Properties[$last]) {
			$node.$last = $value
		} else {
			$node | Add-Member -NotePropertyName $last -NotePropertyValue $value
		}
	}
	$json = ($config | ConvertTo-Json -Depth 32) + "`n"
	[IO.File]::WriteAllText($ConfigPath, $json, [Text.UTF8Encoding]::new($false))
}

# Restore-Config puts back the mygo.json that Set-Config changed.
function Restore-Config {
	if ($script:ConfigBackup) {
		[IO.File]::WriteAllBytes($ConfigPath, $script:ConfigBackup)
		$script:ConfigBackup = $null
	}
}

# Get-GoArch maps arm64 and x64 to Go's names.
function Get-GoArch([string]$Arch) {
	switch ($Arch) {
		'arm64' { 'arm64' }
		{ $_ -in 'x64', 'amd64' } { 'amd64' }
		default { Fail "unknown -Arch ${Arch}: arm64 or x64" }
	}
}

# Get-HostArch returns this machine's architecture, arm64 or x64.
function Get-HostArch {
	switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
		'Arm64' { 'arm64' }
		'X64' { 'x64' }
		default { Fail "unsupported architecture: $_" }
	}
}

# Find-Signtool returns signtool.exe as MyGo finds it: on PATH, else in the
# newest Windows SDK; or nothing.
function Find-Signtool {
	$found = Get-Command signtool -ErrorAction SilentlyContinue
	if ($found) { return $found.Source }
	$arch = if ((Get-HostArch) -eq 'arm64') { 'arm64' } else { 'x64' }
	$kits = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
	Get-ChildItem -Path "$kits\10.*\$arch\signtool.exe" -ErrorAction SilentlyContinue |
		Sort-Object FullName | Select-Object -Last 1 -ExpandProperty FullName
}

# Get-WindowsCertificate returns LEAFLET_WINDOWS_CERTIFICATE, the .pfx that
# signs Windows builds, as a full path, or nothing when it is not set, and
# fails when it is set but cannot sign: the file is missing, or signtool is.
function Get-WindowsCertificate {
	$cert = $env:LEAFLET_WINDOWS_CERTIFICATE
	if (-not $cert) { return }
	if (-not (Test-Path -PathType Leaf $cert)) { Fail "no Windows certificate at $cert (LEAFLET_WINDOWS_CERTIFICATE)" }
	if (-not (Find-Signtool)) { Fail 'signing Windows builds needs signtool from the Windows SDK' }
	if (-not $env:MYGO_WINDOWS_CERTIFICATE_PASSWORD) {
		Warn 'MYGO_WINDOWS_CERTIFICATE_PASSWORD is not set; the certificate must have no password'
	}
	(Resolve-Path $cert).Path
}

# Import-UpdateKey sets MYGO_UPDATER_PRIVATE_KEY, which signs updates, from
# the file LEAFLET_UPDATE_KEY names (by default the one
# `go tool mygo keygen -o $env:APPDATA\mygo\update-keys\Leaflet` wrote)
# unless it is set.
function Import-UpdateKey {
	if ($env:MYGO_UPDATER_PRIVATE_KEY) { return }
	$file = $env:LEAFLET_UPDATE_KEY
	if (-not $file) { $file = Join-Path $env:APPDATA 'mygo\update-keys\Leaflet\mygo-update.key' }
	if (-not (Test-Path -PathType Leaf $file)) {
		Fail "no update signing key at $file; set LEAFLET_UPDATE_KEY or MYGO_UPDATER_PRIVATE_KEY"
	}
	$env:MYGO_UPDATER_PRIVATE_KEY = (Get-Content -Raw $file).TrimEnd("`r", "`n")
}

# Test-Gh fails unless the GitHub CLI is there to upload with, signed in.
function Test-Gh {
	Need gh
	& gh auth status *> $null
	if ($LASTEXITCODE -ne 0 -and -not ($env:GH_TOKEN -or $env:GITHUB_TOKEN)) {
		Fail 'uploads need gh auth login, or GH_TOKEN'
	}
}

# Invoke-Mygo runs go tool mygo with $Arguments and fails when it does.
function Invoke-Mygo([string[]]$Arguments) {
	& go tool mygo @Arguments
	if ($LASTEXITCODE -ne 0) { Fail "go tool mygo $($Arguments[0]) failed" }
}
