<#
.DESCRIPTION
Signs the Windows program at Path, with a timestamp, using the .pfx
LEAFLET_WINDOWS_CERTIFICATE names, whose password is
MYGO_WINDOWS_CERTIFICATE_PASSWORD. It is the windows.signCommand that
scripts\build-release.ps1 gives MyGo, which runs it in the checkout for
each program it signs, the installer's uninstaller among them. The password
is read here, from the environment, so it is on no command line, as it
would be on signtool's.

  scripts\sign-windows.ps1 build\release\windows-amd64\Leaflet.exe
#>
#Requires -Version 7
[CmdletBinding()]
param(
	[Parameter(Mandatory, Position = 0)]
	[string]$Path
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$TimestampServer = 'http://timestamp.digicert.com'

if (-not $env:LEAFLET_WINDOWS_CERTIFICATE) {
	[Console]::Error.WriteLine('sign-windows.ps1: LEAFLET_WINDOWS_CERTIFICATE names no certificate')
	exit 1
}
$pfx = [IO.Path]::GetFullPath($env:LEAFLET_WINDOWS_CERTIFICATE, (Get-Location -PSProvider FileSystem).ProviderPath)
$certificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new(
	$pfx, [string]$env:MYGO_WINDOWS_CERTIFICATE_PASSWORD)
try {
	$signature = Set-AuthenticodeSignature -LiteralPath $Path -Certificate $certificate `
		-HashAlgorithm SHA256 -TimestampServer $TimestampServer
	# Signed with this certificate and timestamped, so the signature
	# outlives it, whether or not this machine trusts its issuer.
	if (-not $signature.SignerCertificate -or $signature.SignerCertificate.Thumbprint -ne $certificate.Thumbprint -or
		-not $signature.TimeStamperCertificate) {
		[Console]::Error.WriteLine("sign-windows.ps1: could not sign ${Path}: $($signature.StatusMessage)")
		exit 1
	}
} finally {
	$certificate.Dispose()
}
