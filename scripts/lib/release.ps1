
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ScriptName = if ($MyInvocation.PSCommandPath) { Split-Path -Leaf $MyInvocation.PSCommandPath } else { 'release.ps1' }

function Fail([string]$Message) {
	[Console]::Error.WriteLine("${ScriptName}: $Message")
	exit 1
}
function Info([string]$Message) { Write-Host "==> $Message" }
function Warn([string]$Message) { [Console]::Error.WriteLine("warning: $Message") }

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$CallerDir = (Get-Location -PSProvider FileSystem).ProviderPath
$ConfigPath = Join-Path $RootDir 'mygo.json'
$ConfigLock = [IO.Path]::Combine($RootDir, 'build', 'mygo.json.lock')
$ConfigKept = [IO.Path]::Combine($RootDir, 'build', 'mygo.json.committed')
$ConfigRecover = [IO.Path]::Combine($RootDir, 'build', 'mygo.json.recover')
$ConfigHeld = $false

function Need([string[]]$Tools) {
	foreach ($tool in $Tools) {
		if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { Fail "needs $tool" }
	}
}

function Check-Version([string]$Version) {
	if ($Version -notmatch '^\d+\.\d+\.\d+$') { Fail "the version must look like 1.2.3, not $Version" }
}

function Read-Config([string]$Path, [string]$Key) {
	$value = Get-Content -Raw $Path | ConvertFrom-Json
	foreach ($part in $Key.Split('.')) { $value = $value.$part }
	$value
}

function Get-ConfigValue([string]$Key) {
	if (-not $script:ConfigHeld -and (Test-Path -PathType Leaf $ConfigKept)) {
		try { return Read-Config $ConfigKept $Key } catch { }
	}
	Read-Config $ConfigPath $Key
}

function New-Exclusive([string]$Path, [string]$Text) {
	try {
		$stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
	} catch [IO.IOException] {
		return $false
	}
	try {
		$bytes = [Text.Encoding]::ASCII.GetBytes($Text)
		$stream.Write($bytes, 0, $bytes.Length)
	} finally {
		$stream.Dispose()
	}
	$true
}

function Read-Holder([string]$Path) {
	try { "$(Get-Content -Raw -ErrorAction Stop $Path)".Trim() } catch { '' }
}

function Hold-Config {
	New-Item -ItemType Directory -Force (Split-Path $ConfigLock) | Out-Null
	$said = $false
	while (-not (New-Exclusive $ConfigLock "$PID`n")) {
		$holder = Read-Holder $ConfigLock
		$gone = $holder -match '^\d+$' -and -not (Get-Process -Id ([int]$holder) -ErrorAction SilentlyContinue)
		if ($gone -and (Repair-Config $holder)) { continue }
		if (-not $said) {
			Info "waiting for another build of this checkout (process $holder), which has mygo.json changed; $ConfigLock names it"
			$said = $true
		}
		Start-Sleep -Seconds 1
	}
	$script:ConfigHeld = $true
	Copy-Item $ConfigPath "$ConfigKept.$PID"
	Move-Item -Force "$ConfigKept.$PID" $ConfigKept
}

function Repair-Config([string]$Holder) {
	if (-not (New-Exclusive $ConfigRecover '')) { return $false }
	try {
		if ((Read-Holder $ConfigLock) -eq $Holder) {
			if (Test-Path -PathType Leaf $ConfigKept) {
				Warn 'a build that ended early left mygo.json changed; putting the committed one back'
				Move-Item -Force $ConfigKept $ConfigPath
			}
			Remove-Item -Force $ConfigLock
		}
	} finally {
		Remove-Item -Force -ErrorAction SilentlyContinue $ConfigRecover
	}
	$true
}

function Set-Config([string[]]$Pairs) {
	if (-not $script:ConfigHeld) { Hold-Config }
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

function Restore-Config {
	if ($script:ConfigHeld) {
		if (Test-Path -PathType Leaf $ConfigKept) { Move-Item -Force $ConfigKept $ConfigPath }
		Remove-Item -Force -ErrorAction SilentlyContinue $ConfigLock
		$script:ConfigHeld = $false
	}
}

function Get-FullPath([string]$Path) { [IO.Path]::GetFullPath($Path, $CallerDir) }

function Get-GoArch([string]$Arch) {
	switch ($Arch) {
		'arm64' { 'arm64' }
		{ $_ -in 'x64', 'amd64' } { 'amd64' }
		default { Fail "unknown -Arch ${Arch}: arm64 or x64" }
	}
}

function Build-Cli([string]$Name, [string]$Version, [string]$GoArch) {
	$bin = Join-Path $RootDir "resources\windows-$GoArch\bin"
	$cmd = ($Name.Trim().ToLowerInvariant() -split '\s+') -join '-'
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

function Get-HostArch {
	switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
		'Arm64' { 'arm64' }
		'X64' { 'x64' }
		default { Fail "unsupported architecture: $_" }
	}
}

function Find-Signtool {
	$found = Get-Command signtool -ErrorAction SilentlyContinue
	if ($found) { return $found.Source }
	$arch = if ((Get-HostArch) -eq 'arm64') { 'arm64' } else { 'x64' }
	$kits = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
	Get-ChildItem -Path "$kits\10.*\$arch\signtool.exe" -ErrorAction SilentlyContinue |
		Sort-Object FullName | Select-Object -Last 1 -ExpandProperty FullName
}

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

function Test-Signed([string[]]$Paths) {
	foreach ($program in $Paths) {
		if (-not (Get-AuthenticodeSignature -LiteralPath $program).SignerCertificate) {
			Fail "$program is not signed, though a Windows certificate was given"
		}
	}
}

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

function Test-Gh {
	Need gh
	& gh auth status *> $null
	if ($LASTEXITCODE -ne 0 -and -not ($env:GH_TOKEN -or $env:GITHUB_TOKEN)) {
		Fail 'uploads need gh auth login, or GH_TOKEN'
	}
}

# Asks gh for the release v$Version, with $Options, into $script:Release. No
# such release is $false; any other failure fails, as none of the guards that
# ask may pass for want of an answer.
function Find-Release([string]$Version, [string[]]$Options) {
	$repo = Get-ConfigValue updates.github
	$err = [IO.Path]::GetTempFileName()
	try {
		$script:Release = & gh release view "v$Version" --repo $repo @Options 2>$err
		$status = $LASTEXITCODE
		$why = "$(Get-Content -Raw $err)".Trim()
	} finally {
		Remove-Item -Force $err
	}
	if ($status -eq 0) { return $true }
	if ($why -match 'release not found') { return $false }
	if (-not $why) { $why = 'gh release view failed' }
	Fail "could not ask GitHub for the release v$Version of ${repo}: $why"
}

function Test-Draft([string]$Version) {
	$repo = Get-ConfigValue updates.github
	if ((Find-Release $Version @('--json', 'isDraft', '--jq', '.isDraft')) -and "$script:Release".Trim() -ne 'true') {
		Fail "the release v$Version of $repo is already published; a new build needs a new version"
	}
}

function Test-DraftCommit([string]$Version, [string]$Commit) {
	$repo = Get-ConfigValue updates.github
	if ((Find-Release $Version @('--json', 'targetCommitish', '--jq', '.targetCommitish')) -and "$script:Release".Trim() -ne $Commit) {
		Fail "the draft release v$Version of $repo holds a build of $("$script:Release".Trim()), not of $Commit; delete the draft and run again"
	}
}

function Test-Unchanged([string]$Commit) {
	$head = & git rev-parse HEAD
	$changed = & git status --porcelain --untracked-files=no
	if ($LASTEXITCODE -ne 0 -or "$head".Trim() -ne $Commit -or $changed) {
		Fail "the checkout changed while $Commit was built, so the build is not of the commit its draft release would record; nothing was uploaded"
	}
}

function Test-Pushed([string]$Commit) {
	$repo = Get-ConfigValue updates.github
	& gh api "repos/$repo/commits/$Commit" --silent *> $null
	if ($LASTEXITCODE -ne 0) {
		Fail "$repo does not have the commit $Commit, which its draft release would record; push it first"
	}
}

function Invoke-Mygo([string[]]$Arguments) {
	& go tool mygo @Arguments
	if ($LASTEXITCODE -ne 0) { Fail "go tool mygo $($Arguments[0]) failed" }
}

function Invoke-Gh {
	& gh @args
	if ($LASTEXITCODE -ne 0) { Fail "gh $($args[0]) $($args[1]) failed" }
}

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

function Read-Checksums([string]$Path) {
	$sums = @{}
	foreach ($line in [IO.File]::ReadAllLines($Path)) {
		if ($line -match '^([0-9a-f]{64}) [ *](.+)$') { $sums[$Matches[2]] = $Matches[1] }
	}
	$sums
}

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

function Send-Release([string]$Version, [string]$Out, [string]$Notes, [string]$Commit, [string[]]$Dirs) {
	$repo = Get-ConfigValue updates.github
	$tag = "v$Version"
	$sumsFile = Join-Path $Out 'SHA256SUMS'
	if (-not (Test-Path -PathType Leaf $sumsFile)) {
		Fail "no SHA256SUMS in $Out, which lists what the build made to upload"
	}
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
	if (Find-Release $Version @('--json', 'assets', '--jq', '.assets[].name')) {
		if (@($script:Release) -contains 'SHA256SUMS') {
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
