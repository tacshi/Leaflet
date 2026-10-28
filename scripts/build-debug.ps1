<#
.DESCRIPTION
Builds a debug Leaflet on Windows that runs beside a release copy: it keeps
MyGo's development features (such as the web inspector), and is named
"Leaflet Debug" with the identifier app.capyvibe.leaflet.debug, so it has a
data folder, credential entries and single-instance lock of its own. Its
pages and settings start empty; LEAFLET_DATA_DIR still points it elsewhere.
It never updates itself, so a release never replaces it.

  scripts\build-debug.ps1               # for this PC's architecture
  scripts\build-debug.ps1 -Open         # build, then launch it
  scripts\build-debug.ps1 -Arch arm64   # arm64 or x64

Builds go to build\win-debug\windows-<arch>: Leaflet Debug.exe with its
files, and "leaflet-debug-setup-<version>-<arch>.exe", made with NSIS, which
MyGo downloads when it is not installed. Neither is signed. On macOS,
scripts/build-debug.sh builds this and the macOS app.
#>
#Requires -Version 7
[CmdletBinding()]
param(
	[ValidateSet('arm64', 'x64', 'amd64')]
	[string]$Arch,
	[switch]$Open
)
. "$PSScriptRoot\lib\release.ps1"
Set-Location $RootDir

if (-not $IsWindows) { Fail 'this builds on Windows; elsewhere use scripts/build-debug.sh --win' }
Need go
if (-not $Arch) { $Arch = Get-HostArch }
$GoArch = Get-GoArch $Arch

$Name = 'Leaflet Debug'
$Out = 'build\win-debug'
$Dir = Join-Path $Out "windows-$GoArch"
try {
	# MyGo names the data folder after the app and keeps secrets under its
	# identifier, both from mygo.json, which this build alone sees changed.
	# Without updates, the app has no updater.
	Set-Config "name=$Name", "identifier=$(Get-ConfigValue identifier).debug", '-updates'
	Info "building $Name for windows/$GoArch"
	Invoke-Mygo build, -debug, -platform, "windows/$GoArch", -o, $Out
} finally {
	Restore-Config
}
$Exe = Join-Path $Dir "$Name.exe"
if (-not (Test-Path -PathType Leaf $Exe)) { Fail "expected $Exe after the build" }
Info "built $(Join-Path $RootDir $Dir)"
if ($Open) {
	# arm64 Windows runs x64 apps too, but x64 Windows can't run arm64 ones.
	if ($GoArch -eq 'arm64' -and (Get-HostArch) -ne 'arm64') { Fail '-Open cannot launch an arm64 build on an x64 PC' }
	Start-Process -FilePath (Join-Path $RootDir $Exe)
}
