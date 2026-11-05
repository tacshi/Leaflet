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
# The directory the script was run from, which the paths it is given are
# relative to, though it then works in $RootDir.
$CallerDir = (Get-Location -PSProvider FileSystem).ProviderPath
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

# Get-FullPath returns $Path in full: as it is when it is, else from the
# directory the script was run from.
function Get-FullPath([string]$Path) { [IO.Path]::GetFullPath($Path, $CallerDir) }

# Get-GoArch maps arm64 and x64 to Go's names.
function Get-GoArch([string]$Arch) {
	switch ($Arch) {
		'arm64' { 'arm64' }
		{ $_ -in 'x64', 'amd64' } { 'amd64' }
		default { Fail "unknown -Arch ${Arch}: arm64 or x64" }
	}
}

# Build-Cli builds the command line for the app named $Name, version
# $Version, on Go's architecture $GoArch (cmd/leaflet), into what the app
# ships as bin\<command>.exe: the app is a GUI program, which a terminal
# neither waits for nor shows the output of. Its name is the app's, in
# lower case with dashes, as cli.CommandName has it: leaflet, or
# leaflet-debug beside it. MyGo signs it with the app.
function Build-Cli([string]$Name, [string]$Version, [string]$GoArch) {
	$bin = Join-Path $RootDir "resources\windows-$GoArch\bin"
	$cmd = ($Name.Trim().ToLowerInvariant() -split '\s+') -join '-'
	# The other build's command must not ship in this one.
	if (Test-Path $bin) { Remove-Item -Recurse -Force $bin }
	Info "building $cmd.exe for windows/$GoArch"
	$saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
	$env:GOOS, $env:GOARCH, $env:CGO_ENABLED = 'windows', $GoArch, '0'
	try {
		& go build -trimpath -ldflags "-s -w -X 'main.appName=$Name' -X 'main.version=$Version' $env:MYGO_LDFLAGS" -o (Join-Path $bin "$cmd.exe") ./cmd/leaflet
		if ($LASTEXITCODE -ne 0) { Fail "building $cmd.exe failed" }
	} finally {
		foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key]) }
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
	$cert = Get-FullPath $cert
	if (-not (Test-Path -PathType Leaf $cert)) { Fail "no Windows certificate at $cert (LEAFLET_WINDOWS_CERTIFICATE)" }
	if (-not (Find-Signtool)) { Fail 'signing Windows builds needs signtool from the Windows SDK' }
	if (-not $env:MYGO_WINDOWS_CERTIFICATE_PASSWORD) {
		Warn 'MYGO_WINDOWS_CERTIFICATE_PASSWORD is not set; the certificate must have no password'
	}
	$cert
}

# Test-Signed fails unless each of the Windows programs $Paths carries a
# signature: a certificate that signed some of a build should leave none of
# it to go out unsigned.
function Test-Signed([string[]]$Paths) {
	foreach ($program in $Paths) {
		if (-not (Get-AuthenticodeSignature -LiteralPath $program).SignerCertificate) {
			Fail "$program is not signed, though a Windows certificate was given"
		}
	}
}

# Get-UpdateKeyFile returns the file of the key that signs updates, the one
# LEAFLET_UPDATE_KEY names (by default the one
# `go tool mygo keygen -o $env:APPDATA\mygo\update-keys\Leaflet` wrote), for
# updates.privateKey: read there, the key stays out of what every program
# the release runs inherits. It returns nothing when
# MYGO_UPDATER_PRIVATE_KEY, which MyGo takes first, is set, and fails when
# there is neither.
function Get-UpdateKeyFile {
	if ($env:MYGO_UPDATER_PRIVATE_KEY) { return }
	$file = $env:LEAFLET_UPDATE_KEY
	if (-not $file) { $file = Join-Path $env:APPDATA 'mygo\update-keys\Leaflet\mygo-update.key' }
	$file = Get-FullPath $file
	if (-not (Test-Path -PathType Leaf $file)) {
		Fail "no update signing key at $file; set LEAFLET_UPDATE_KEY or MYGO_UPDATER_PRIVATE_KEY"
	}
	$file
}

# Test-Gh fails unless the GitHub CLI is there to upload with, signed in.
function Test-Gh {
	Need gh
	& gh auth status *> $null
	if ($LASTEXITCODE -ne 0 -and -not ($env:GH_TOKEN -or $env:GITHUB_TOKEN)) {
		Fail 'uploads need gh auth login, or GH_TOKEN'
	}
}

# Test-Draft fails when the GitHub release v$Version of updates.github is
# published: an upload to it would replace, under the same version, the
# files installed copies have taken. A draft, or no release yet, passes.
function Test-Draft([string]$Version) {
	$repo = Get-ConfigValue updates.github
	$draft = & gh release view "v$Version" --repo $repo --json isDraft --jq .isDraft 2>$null
	if ($LASTEXITCODE -eq 0 -and "$draft".Trim() -ne 'true') {
		Fail "the release v$Version of $repo is already published; a new build needs a new version"
	}
}

# Test-DraftCommit fails when the draft release v$Version of updates.github
# holds a build of another commit than $Commit, which Send-Release records
# on it as it makes it (--target): a release tags one commit, and a build of
# another would go out under it. No release yet passes.
function Test-DraftCommit([string]$Version, [string]$Commit) {
	$repo = Get-ConfigValue updates.github
	$target = & gh release view "v$Version" --repo $repo --json targetCommitish --jq .targetCommitish 2>$null
	if ($LASTEXITCODE -eq 0 -and "$target".Trim() -ne $Commit) {
		Fail "the draft release v$Version of $repo holds a build of $("$target".Trim()), not of $Commit; delete the draft and run again"
	}
}

# Test-Pushed fails unless updates.github has the commit $Commit, which a
# draft can record only then: found out before the long build, not after it.
function Test-Pushed([string]$Commit) {
	$repo = Get-ConfigValue updates.github
	& gh api "repos/$repo/commits/$Commit" --silent *> $null
	if ($LASTEXITCODE -ne 0) {
		Fail "$repo does not have the commit $Commit, which its draft release would record; push it first"
	}
}

# Invoke-Mygo runs go tool mygo with $Arguments and fails when it does.
function Invoke-Mygo([string[]]$Arguments) {
	& go tool mygo @Arguments
	if ($LASTEXITCODE -ne 0) { Fail "go tool mygo $($Arguments[0]) failed" }
}

# Invoke-Gh runs gh with its arguments and fails when it does.
function Invoke-Gh {
	& gh @args
	if ($LASTEXITCODE -ne 0) { Fail "gh $($args[0]) $($args[1]) failed" }
}

# Get-ChangelogNotes returns the section of the changelog $Path under
# "## <version>" (or "## [<version>]") for $Version, as MyGo reads it, or
# nothing.
function Get-ChangelogNotes([string]$Path, [string]$Version) {
	if (-not (Test-Path -PathType Leaf $Path)) { return '' }
	$section = @()
	$inside = $false
	foreach ($line in [IO.File]::ReadAllLines($Path)) {
		if ($line.StartsWith('## ')) {
			if ($inside) { break }
			$fields = @(-split $line.Substring(3))
			$heading = if ($fields.Count) { $fields[0] } else { '' }
			if ($heading.StartsWith('[')) { $heading = $heading.Substring(1).Split(']', 2)[0] }
			$inside = ($heading -creplace '^v', '') -eq $Version
			continue
		}
		if ($inside) { $section += $line }
	}
	($section -join "`n").Trim()
}

# Read-Checksums returns the lines of the SHA256SUMS $Path, each file's sum
# by its name.
function Read-Checksums([string]$Path) {
	$sums = @{}
	foreach ($line in [IO.File]::ReadAllLines($Path)) {
		if ($line -match '^([0-9a-f]{64}) [ *](.+)$') { $sums[$Matches[2]] = $Matches[1] }
	}
	$sums
}

# Merge-Checksums adds to the SHA256SUMS $Path the lines of the SHA256SUMS
# $Earlier, an earlier upload's to the same release, for the files $Path
# doesn't list, keeping the lines sorted by name, as MyGo writes them.
function Merge-Checksums([string]$Path, [string]$Earlier) {
	$sums = Read-Checksums $Path
	$before = Read-Checksums $Earlier
	foreach ($name in $before.Keys) {
		if (-not $sums.ContainsKey($name)) { $sums[$name] = $before[$name] }
	}
	$names = [string[]]$sums.Keys
	[Array]::Sort($names, [StringComparer]::Ordinal)
	$text = ($names | ForEach-Object { "$($sums[$_])  $_`n" }) -join ''
	[IO.File]::WriteAllText($Path, $text, [Text.UTF8Encoding]::new($false))
}

# Send-Release puts what go tool mygo build wrote in the output directory
# $Out, built from the commit $Commit, for the platform folders $Dirs, on the
# GitHub release v$Version of updates.github, with gh, as mygo build -upload
# does: the files the build's SHA256SUMS lists, which MyGo writes for the
# files it would upload (the installers, update archives, deltas and install
# scripts), and SHA256SUMS, first, then the update manifests, so that none
# ever names a file not there yet. They keep MyGo's names, by which
# SHA256SUMS lists them and install.ps1 finds its installer. A release there
# already must be a draft of that commit, and keeps the checksums of the
# files other builds put on it; else it is made, as a draft of it, with the
# notes in the file $Notes.
function Send-Release([string]$Version, [string]$Out, [string]$Notes, [string]$Commit, [string[]]$Dirs) {
	$repo = Get-ConfigValue updates.github
	$tag = "v$Version"
	$sumsFile = Join-Path $Out 'SHA256SUMS'
	if (-not (Test-Path -PathType Leaf $sumsFile)) {
		Fail "no SHA256SUMS in $Out, which lists what the build made to upload"
	}
	# By MyGo's own list, a kind of file it starts to make goes up too,
	# rather than being left out. One install script serves every
	# architecture of a system, so the first folder's goes.
	$files = @()
	foreach ($name in (Read-Checksums $sumsFile).Keys) {
		$found = $null
		foreach ($dir in $Dirs) {
			$candidate = Join-Path $Out "$dir\$name"
			if (Test-Path -PathType Leaf $candidate) {
				$found = $candidate
				break
			}
		}
		if (-not $found) { Fail "SHA256SUMS lists $name, which is in none of the build's folders ($($Dirs -join ', '))" }
		$files += $found
	}
	$manifests = @()
	foreach ($dir in $Dirs) {
		$manifests += @(Get-ChildItem -LiteralPath (Join-Path $Out $dir) -Filter 'update-*.json' -File | ForEach-Object FullName)
	}
	if ($files.Count + $manifests.Count -eq 0) { Fail "nothing to upload in $Out" }
	Test-Draft $Version
	Test-DraftCommit $Version $Commit
	$assets = & gh release view $tag --repo $repo --json assets --jq '.assets[].name' 2>$null
	if ($LASTEXITCODE -eq 0) {
		if (@($assets) -contains 'SHA256SUMS') {
			$earlier = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
			New-Item -ItemType Directory $earlier | Out-Null
			try {
				Invoke-Gh release download $tag --repo $repo --pattern SHA256SUMS --dir $earlier
				Merge-Checksums $sumsFile (Join-Path $earlier 'SHA256SUMS')
			} finally {
				Remove-Item -Recurse -Force $earlier
			}
		}
	} else {
		Info "creating the draft release $tag of $repo"
		Invoke-Gh release create $tag --repo $repo --draft --target $Commit --title $tag --notes-file $Notes | Out-Null
	}
	$files += $sumsFile
	Info "uploading $($files.Count) files, then $($manifests.Count) manifests, to $tag of $repo"
	Invoke-Gh release upload $tag --repo $repo --clobber @files
	if ($manifests.Count -gt 0) { Invoke-Gh release upload $tag --repo $repo --clobber @manifests }
}
