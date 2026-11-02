# Builds the siren agent for both target platforms into bin\.
# Usage: .\build.ps1 [-Version 0.1.0]
param(
    [string]$Version = "0.1.0-dev"
)

$ErrorActionPreference = "Stop"

$go = "C:\Program Files\Go\bin\go.exe"
if (-not (Test-Path $go)) {
    $go = "go"  # fallback to PATH
}

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

# Regenerate the resource object from the manifest so the release exe keeps
# requireAdministrator + comctl32 v6. Note: with the .syso present, the
# cmd/siren test binary also requires elevation ("go test ./cmd/siren/").
$rsrc = "$(& $go env GOPATH)\bin\rsrc.exe"
if (Test-Path $rsrc) {
    & $rsrc -manifest "cmd\siren\app.manifest" -arch amd64 -o "cmd\siren\rsrc_windows_amd64.syso"
    if ($LASTEXITCODE -ne 0) { throw "rsrc failed" }
} elseif (-not (Test-Path "cmd\siren\rsrc_windows_amd64.syso")) {
    throw "rsrc.exe not found (go install github.com/akavel/rsrc@latest) and no .syso present"
}

$ldflags = "-s -w -X main.version=$Version"

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
# -H=windowsgui: no console window on double-click; the agent attaches to a
# parent terminal programmatically when run from one (initConsole).
& $go build -trimpath -ldflags "-H=windowsgui $ldflags" -o "bin\siren-windows-amd64.exe" ".\cmd\siren"
if ($LASTEXITCODE -ne 0) { throw "windows build failed" }

$env:GOOS = "linux"
& $go build -trimpath -ldflags $ldflags -o "bin\siren-linux-amd64" ".\cmd\siren"
if ($LASTEXITCODE -ne 0) { throw "linux build failed" }

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

Get-ChildItem "bin" | Format-Table Name, Length
