param(
    [string]$Version = "0.1.0"
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

$goCommand = Get-Command go -ErrorAction SilentlyContinue
if ($goCommand) {
    $goExe = $goCommand.Source
} else {
    $goExe = Join-Path $env:USERPROFILE ".local\go-toolchain\go\bin\go.exe"
}

if (-not (Test-Path $goExe)) {
    throw "Go not found. Install Go or place the portable toolchain at $env:USERPROFILE\.local\go-toolchain\go."
}

Push-Location $root
try {
    Write-Host "Using $goExe"
    & $goExe test ./...
    if ($LASTEXITCODE -ne 0) {
        throw "go test failed with exit code $LASTEXITCODE"
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $root "bin") | Out-Null
    & $goExe build -trimpath -ldflags "-s -w -X main.version=$Version" -o "bin\deepseek-search-mcp.exe" .
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }

    $installDir = Join-Path $env:USERPROFILE ".local\bin"
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item "bin\deepseek-search-mcp.exe" (Join-Path $installDir "deepseek-search-mcp.exe") -Force

    Write-Host "Built:     $root\bin\deepseek-search-mcp.exe"
    Write-Host "Installed: $installDir\deepseek-search-mcp.exe"
    Write-Host "Version:   $Version"
    Write-Host "Restart ZCode after changing the binary or environment variables."
} finally {
    Pop-Location
}
