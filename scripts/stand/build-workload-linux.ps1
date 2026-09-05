param(
    [string]$Output = "bin/search-workload-linux-amd64"
)

$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$OutputPath = Join-Path $Root $Output
$OutputDirectory = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$PreviousGoos = $env:GOOS
$PreviousGoarch = $env:GOARCH
$PreviousCgo = $env:CGO_ENABLED
$PreviousGoCache = $env:GOCACHE
$PreviousGoModCache = $env:GOMODCACHE

try {
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    $env:GOCACHE = Join-Path $Root ".gocache"
    $env:GOMODCACHE = Join-Path $Root ".gomodcache"

    Push-Location (Join-Path $Root "workload")
    try {
        go build -trimpath -o $OutputPath ./cmd/search-workload
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $PreviousGoos
    $env:GOARCH = $PreviousGoarch
    $env:CGO_ENABLED = $PreviousCgo
    $env:GOCACHE = $PreviousGoCache
    $env:GOMODCACHE = $PreviousGoModCache
}

Write-Host "built $OutputPath"
