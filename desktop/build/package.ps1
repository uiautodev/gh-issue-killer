# Assemble the Windows desktop bundle: the shell and the engine in one zip.
# Build the SvelteKit dashboard first (`npm --prefix frontend run build`); the
# engine embeds cmd/issuekiller/web/dist.
# Run from PowerShell on a Windows machine (the release workflow does this).
param(
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$DesktopDir = Split-Path -Parent $PSScriptRoot
$Root = Split-Path -Parent $DesktopDir
$Goos = & go env GOOS
$Goarch = & go env GOARCH
$Dist = Join-Path $DesktopDir "build\dist"
$Name = "issuekiller-desktop_${Version}_${Goos}_${Goarch}"
$Work = Join-Path $Dist $Name
$Zip = Join-Path $Dist "$Name.zip"

if ($Goos -ne "windows") {
    throw "package.ps1 is for Windows runners; use build/package.sh"
}

if (Test-Path $Work) { Remove-Item -Recurse -Force $Work }
if (Test-Path $Zip) { Remove-Item -Force $Zip }
New-Item -ItemType Directory -Path $Work | Out-Null

Write-Host "building engine $Goos/$Goarch"
Push-Location $Root
try {
    & go build -trimpath -ldflags "-s -w -X main.version=$Version" `
        -o (Join-Path $Work "issuekiller.exe") ./cmd/issuekiller
    if ($LASTEXITCODE -ne 0) { throw "engine build failed" }
} finally { Pop-Location }

Write-Host "building desktop shell"
Push-Location $DesktopDir
try {
    & go build -trimpath -ldflags "-s -w" `
        -o (Join-Path $Work "issuekiller-desktop.exe") .
    if ($LASTEXITCODE -ne 0) { throw "desktop build failed" }
} finally { Pop-Location }

Compress-Archive -Path (Join-Path $Work "*") -DestinationPath $Zip -Force
Remove-Item -Recurse -Force $Work
Get-Item $Zip | Format-List Name, Length
