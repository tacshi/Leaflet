<#
.DESCRIPTION
Builds Leaflet's Windows release installers on Windows, with the signed
update archives, deltas and manifests that installed copies update from.

  scripts\build-release.ps1                     # the version in mygo.json
  scripts\build-release.ps1 0.2.0               # another version
  scripts\build-release.ps1 -Arch arm64         # only some: arm64, x64
  scripts\build-release.ps1 -Out out            # another output directory
  scripts\build-release.ps1 -Upload             # and upload to a draft GitHub release
  scripts\build-release.ps1 -Changelog f.md     # notes from f.md, not CHANGELOG.md
  scripts\build-release.ps1 -Upload -AllowUnsigned  # upload unsigned builds too

The installers go to build\release: Leaflet-<version>-{x64,arm64}-setup.exe,
beside a folder per platform with the app and its updates. NSIS makes them,
which MyGo downloads when it is not installed. -Upload puts them all in the
draft release v<version> of tacshi/Leaflet, with gh; installed copies see
the update once the draft is published.

Signs updates with MYGO_UPDATER_PRIVATE_KEY, else the key in the file
LEAFLET_UPDATE_KEY, by default
  %APPDATA%\mygo\update-keys\Leaflet\mygo-update.key
made with
  go tool mygo keygen -o $env:APPDATA\mygo\update-keys\Leaflet
Its public half is updates.publicKey in mygo.json.

Signs with the .pfx LEAFLET_WINDOWS_CERTIFICATE names, whose password is
MYGO_WINDOWS_CERTIFICATE_PASSWORD, using signtool from the Windows SDK;
without it the builds are unsigned and SmartScreen warns about them, and
-Upload refuses them unless -AllowUnsigned, or LEAFLET_ALLOW_UNSIGNED=1,
says to upload them anyway.

macOS releases are built on macOS with scripts/build-release.sh. To build a
Leaflet server in, set MYGO_LDFLAGS as the README says.
#>
#Requires -Version 7
[CmdletBinding()]
param(
	[Parameter(Position = 0)]
	[string]$Version,
	[ValidateSet('arm64', 'x64', 'amd64')]
	[string[]]$Arch = @('x64', 'arm64'),
	[string]$Out = 'build\release',
	[switch]$Upload,
	[switch]$AllowUnsigned,
	[string]$Changelog
)
. "$PSScriptRoot\lib\release.ps1"
Set-Location $RootDir

if (-not $IsWindows) { Fail 'this builds on Windows; elsewhere use scripts/build-release.sh --win' }
Need go
if (-not $Version) { $Version = Get-ConfigValue version }
$Version = $Version.TrimStart('v')
Check-Version $Version
$Out = [IO.Path]::GetFullPath($Out, $RootDir)
if ($Changelog) {
	if (-not (Test-Path -PathType Leaf $Changelog)) { Fail "no changelog $Changelog" }
	$Changelog = (Resolve-Path $Changelog).Path
}
$Arches = @($Arch | ForEach-Object { if ($_ -eq 'amd64') { 'x64' } else { $_ } } | Select-Object -Unique)
$Platforms = @($Arches | ForEach-Object { "windows/$(Get-GoArch $_)" })

$Config = @("version=$Version")
if ($Changelog) { $Config += "updates.changelog=$Changelog" }
$Certificate = Get-WindowsCertificate
if ($Certificate) {
	$Config += "windows.certificate=$Certificate"
} elseif ($Upload -and -not $AllowUnsigned -and $env:LEAFLET_ALLOW_UNSIGNED -ne '1') {
	Fail 'LEAFLET_WINDOWS_CERTIFICATE is not set, so -Upload would publish unsigned Windows builds; set it, or add -AllowUnsigned (or LEAFLET_ALLOW_UNSIGNED=1) to upload them anyway'
} else {
	Warn 'LEAFLET_WINDOWS_CERTIFICATE is not set, so the Windows builds are unsigned'
}
Import-UpdateKey
$BuildArgs = @('build', '-platform', ($Platforms -join ','), '-o', $Out)
if ($Upload) {
	Test-Gh
	$BuildArgs += '-upload'
}

try {
	# The version, the changelog and the certificate go into mygo.json for
	# this build only.
	Set-Config $Config
	Info "building Leaflet $Version for $($Platforms -join ' ')"
	Invoke-Mygo $BuildArgs
} finally {
	Restore-Config
}

# MyGo names installers "leaflet-setup-<version>-<arch>.exe", in a folder
# per platform; they are moved out beside those folders under the usual names.
foreach ($a in $Arches) {
	$goArch = Get-GoArch $a
	$setup = Join-Path $Out "windows-$goArch\leaflet-setup-$Version-$goArch.exe"
	if (-not (Test-Path -PathType Leaf $setup)) { Fail "expected $setup after the build" }
	$dest = Join-Path $Out "Leaflet-$Version-$a-setup.exe"
	Move-Item -Force $setup $dest
	Info $dest
}
