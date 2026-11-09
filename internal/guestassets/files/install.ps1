#requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot 'jetkvm-guest.exe'
$pairing = Join-Path $PSScriptRoot 'pairing.json'
$manifest = Get-Content (Join-Path $PSScriptRoot 'manifest.json') -Raw | ConvertFrom-Json
if ($manifest.version -ne 1 -or (Get-Item $source).Length -ne $manifest.bytes -or (Get-FileHash $source -Algorithm SHA256).Hash -ne $manifest.sha256) { throw 'Companion integrity check failed' }
$service = Get-Service JetKVMGuest -ErrorAction SilentlyContinue
if ($service) { Stop-Service JetKVMGuest; $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30)) }
$destination = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'JetKVMGuest'
if (Test-Path $destination) {
 if ((Get-Item -Force $destination).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unsafe installation directory' }
} else { New-Item -ItemType Directory $destination | Out-Null }
& icacls.exe $destination /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
if ($LASTEXITCODE) { throw 'Cannot protect installation directory' }
$exe = Join-Path $destination 'jetkvm-guest.exe'
if ((Test-Path $exe) -and ((Get-Item -Force $exe).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Unsafe executable path' }
Copy-Item -LiteralPath $source -Destination $exe -Force
& $exe --import-pairing $pairing
if ($LASTEXITCODE) { throw 'Pairing import failed' }
Remove-Item -LiteralPath $pairing -Force
& $exe --install-service
if ($LASTEXITCODE) { throw 'Service installation failed' }
Write-Host 'JetKVM guest integration installed. Delete the downloaded private ZIP after installation.'
