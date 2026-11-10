<#
.DESCRIPTION
Builds Leaflet's Windows release installers on Windows, with the signed
update archives, deltas and manifests that installed copies update from.

  scripts\build-release.ps1                     # the version in mygo.json
  scripts\build-release.ps1 0.2.0               # another version
  scripts\build-release.ps1 -Arch arm64         # only some: arm64, x64
  scripts\build-release.ps1 -Out out            # another output directory
  scripts\build-release.ps1 0.2.0 -Upload       # and upload to a draft GitHub release
  scripts\build-release.ps1 -Changelog f.md     # notes from f.md, not CHANGELOG.md
  scripts\build-release.ps1 0.2.0 -Upload -AllowUnsigned  # upload unsigned builds too

The installers go to build\release: Leaflet-<version>-{x64,arm64}-setup.exe,
beside a folder per platform with the app and its updates. NSIS makes them,
which MyGo downloads when it is not installed. -Upload, once every installer
is built, puts the installers and updates in the draft release v<version> of
tacshi/Leaflet, with gh, before the installers are renamed: there they keep
MyGo's names, leaflet-setup-<version>-<arch>.exe, by which SHA256SUMS lists
them and install.ps1 finds its installer. Installed copies see the update
once the draft is published. A release that is published already is refused:
its files are what installed copies took. So is a draft of another commit,
which the draft records as it is made: a release tags one commit. So is a
working tree with changes, before the build and again once it is done: the
build would not be that commit's. It needs the version named, as mygo.json's
is no release's.

Signs updates with MYGO_UPDATER_PRIVATE_KEY, else the key in the file
LEAFLET_UPDATE_KEY, by default
  %APPDATA%\mygo\update-keys\Leaflet\mygo-update.key
made with
  go tool mygo keygen -o $env:APPDATA\mygo\update-keys\Leaflet
Its public half is updates.publicKey in mygo.json.

Signs with the .pfx LEAFLET_WINDOWS_CERTIFICATE names, whose password is
MYGO_WINDOWS_CERTIFICATE_PASSWORD, using signtool from the Windows SDK, and
fails if any program of the build comes out unsigned; without it the builds
are unsigned and SmartScreen warns about them, and -Upload refuses them
unless -AllowUnsigned, or LEAFLET_ALLOW_UNSIGNED=1, says to upload them
anyway.

Paths given, -Out, -Changelog and those in LEAFLET_WINDOWS_CERTIFICATE and
LEAFLET_UPDATE_KEY, are from where this is run.

macOS releases are built on macOS with scripts/build-release.sh. To build a
server in, set MYGO_LDFLAGS as README.md's Build and release shows.
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
if ($Upload -and -not $Version) { Fail '-Upload needs the version to release, as in scripts\build-release.ps1 0.2.0 -Upload' }
if (-not $Version) { $Version = Get-ConfigValue version }
$Version = $Version.TrimStart('v')
Check-Version $Version
$Out = if ($PSBoundParameters.ContainsKey('Out')) { Get-FullPath $Out } else { [IO.Path]::GetFullPath($Out, $RootDir) }
if ($Changelog) {
	$Changelog = Get-FullPath $Changelog
	if (-not (Test-Path -PathType Leaf $Changelog)) { Fail "no changelog $Changelog" }
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
$UpdateKey = Get-UpdateKeyFile
if ($UpdateKey) { $Config += "updates.privateKey=$UpdateKey" }
$BuildArgs = @('build', '-platform', ($Platforms -join ','), '-o', $Out)
if ($Upload) {
	Test-Gh
	Test-Draft $Version
	Need git
	$Commit = & git rev-parse HEAD
	if ($LASTEXITCODE -ne 0) { Fail 'git rev-parse HEAD failed' }
	Test-Pushed $Commit
	Test-DraftCommit $Version $Commit
}

try {
	if ($Upload) {
		# Held first: another build of this checkout has mygo.json changed until it ends.
		Hold-Config
		if (& git status --porcelain) {
			Fail '-Upload needs a clean working tree, as the draft release records the commit as what was built; commit or stash the changes first'
		}
	}
	Set-Config $Config
	foreach ($a in $Arches) { Build-Cli (Get-ConfigValue name) $Version (Get-GoArch $a) }
	Info "building Leaflet $Version for $($Platforms -join ' ')"
	Invoke-Mygo $BuildArgs
} finally {
	Restore-Config
}

foreach ($a in $Arches) {
	$goArch = Get-GoArch $a
	$folder = Join-Path $Out "windows-$goArch"
	$setup = Join-Path $folder "leaflet-setup-$Version-$goArch.exe"
	if (-not (Test-Path -PathType Leaf $setup)) { Fail "expected $setup after the build" }
	if ($Certificate) {
		Test-Signed @(Get-ChildItem -LiteralPath $folder, (Join-Path $folder 'bin') -Filter '*.exe' -File | ForEach-Object FullName)
	}
}

if ($Upload) {
	$NotesFile = [IO.Path]::GetTempFileName()
	try {
		$notesFrom = if ($Changelog) { $Changelog } else { Join-Path $RootDir 'CHANGELOG.md' }
		[IO.File]::WriteAllText($NotesFile, (Get-ChangelogNotes $notesFrom $Version), [Text.UTF8Encoding]::new($false))
		Test-Unchanged $Commit
		Send-Release $Version $Out $NotesFile $Commit @($Arches | ForEach-Object { "windows-$(Get-GoArch $_)" })
	} finally {
		Remove-Item -Force $NotesFile
	}
	Info "uploaded to the draft release v$Version of $(Get-ConfigValue updates.github); publish it once every platform is there"
}

foreach ($a in $Arches) {
	$goArch = Get-GoArch $a
	$setup = Join-Path $Out "windows-$goArch\leaflet-setup-$Version-$goArch.exe"
	$dest = Join-Path $Out "Leaflet-$Version-$a-setup.exe"
	Move-Item -Force $setup $dest
	Info $dest
}
