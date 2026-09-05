param([string]$Config = "config/workload.local.json")

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$Binary = Join-Path $ProjectRoot "bin/search-workload.exe"
if (-not [System.IO.Path]::IsPathRooted($Config)) {
    $Config = Join-Path $ProjectRoot $Config
}

if (-not (Test-Path -LiteralPath $Binary)) {
    throw "workload binary not found: $Binary"
}
if (-not (Test-Path -LiteralPath $Config)) {
    throw "config not found: $Config"
}

& $Binary -config $Config -drop init
& $Binary -config $Config load
& $Binary -config $Config indexes
& $Binary -config $Config -wait-timeout 30m wait
& $Binary -config $Config -scope all partition-elastic
& $Binary -config $Config check
& $Binary -config $Config partitions
