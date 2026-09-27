#Requires -Version 5.1
<#
.SYNOPSIS
    Start the local embedding and rerank services with llama.cpp server.

.DESCRIPTION
    Launches two llama.cpp server processes, one per GGUF model found under -ModelsDir:

        embedding : qwen3-embedding-4b-q4_k_m_2.gguf  ->  http://127.0.0.1:8080
        rerank    : bge-reranker-v2-m3-q8_0.gguf      ->  http://127.0.0.1:8081

    8080 is the llama.cpp default port and is assigned to the embedding service.
    Two servers cannot share one port, so the rerank service uses 8081.
    Override either port with -EmbeddingPort / -RerankPort.

    Only logs/ and logs/local-llm.pids.json are written inside the repository
    (logs/ is git-ignored).

.PARAMETER Stop
    Stop the services recorded by a previous run, then exit.

.PARAMETER Status
    Report the recorded services and exit; nothing is started.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\scripts\start-local-llm.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\scripts\start-local-llm.ps1 -Status

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\scripts\start-local-llm.ps1 -Stop
#>
[CmdletBinding()]
param(
    [string] $ModelsDir       = 'C:\APP\Models',
    [string] $LlamaExe        = '',
    [int]    $EmbeddingPort   = 8080,
    [int]    $RerankPort      = 8081,
    [int]    $ContextSize     = 8192,
    [string] $HostAddress     = '127.0.0.1',
    [int]    $ReadyTimeoutSec = 240,
    [switch] $Stop,
    [switch] $Status
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$LogDir      = Join-Path $ProjectRoot 'logs'
$StateFile   = Join-Path $LogDir 'local-llm.pids.json'

$EmbeddingModelName = 'qwen3-embedding-4b-q4_k_m_2.gguf'
$RerankModelName    = 'bge-reranker-v2-m3-q8_0.gguf'

function Write-Step { param([string]$Message) Write-Host "[local-llm] $Message" -ForegroundColor Cyan }
function Write-Ok   { param([string]$Message) Write-Host "[local-llm] $Message" -ForegroundColor Green }
function Write-Note { param([string]$Message) Write-Host "[local-llm] $Message" -ForegroundColor Yellow }
function Write-Fail { param([string]$Message) Write-Host "[local-llm] $Message" -ForegroundColor Red }

function Resolve-LlamaExecutable {
    param([string]$Explicit)
    if ($Explicit) {
        if (Test-Path -LiteralPath $Explicit) { return (Resolve-Path -LiteralPath $Explicit).Path }
        throw "llama.cpp executable not found: $Explicit"
    }
    $cmd = Get-Command llama -ErrorAction SilentlyContinue
    if ($cmd -and $cmd.Source) { return $cmd.Source }
    $candidates = @(
        (Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\llama.exe'),
        (Join-Path $env:LOCALAPPDATA 'Programs\llama.cpp\llama-server.exe'),
        'C:\llama.cpp\llama-server.exe'
    )
    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { return $candidate }
    }
    throw 'llama.cpp not found. Install it or pass -LlamaExe <path to llama.exe / llama-server.exe>.'
}

function Resolve-ModelFile {
    param([string]$Directory, [string]$PreferredName, [string]$FallbackPattern)
    if (-not (Test-Path -LiteralPath $Directory)) { throw "model directory not found: $Directory" }
    $preferred = Join-Path $Directory $PreferredName
    if (Test-Path -LiteralPath $preferred) { return $preferred }
    $hit = Get-ChildItem -LiteralPath $Directory -Filter $FallbackPattern -File -ErrorAction SilentlyContinue |
        Sort-Object Length -Descending | Select-Object -First 1
    if ($hit) { return $hit.FullName }
    throw "model not found in $Directory (expected $PreferredName)"
}

function Get-ServeArguments {
    param(
        [string]   $Executable,
        [string]   $ModelPath,
        [int]      $Port,
        [string]   $Alias,
        [string[]] $ExtraArgs,
        [int]      $Ctx
    )
    $args = @('-m', $ModelPath, '--host', $HostAddress, '--port', "$Port", '-c', "$Ctx")
    if ($Alias) { $args += @('-a', $Alias) }
    $args += $ExtraArgs
    # Unified llama.cpp CLI needs the "serve" sub-command; the legacy
    # llama-server.exe binary is already the server.
    if ((Split-Path -Leaf $Executable) -like 'llama-server*') { return $args }
    return @('serve') + $args
}

function Get-PortOwner {
    param([int]$Port)
    $conn = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($conn) { return [int]$conn.OwningProcess }
    return 0
}

function Wait-EndpointReady {
    param([int]$Port, [int]$TimeoutSec)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $uri = "http://$HostAddress`:$Port/health"
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $uri -UseBasicParsing -TimeoutSec 3
            if ($resp.StatusCode -eq 200) { return $true }
        } catch { }
        Start-Sleep -Milliseconds 800
    }
    return $false
}

function Get-EndpointModelInfo {
    param([int]$Port)
    try {
        $raw = (Invoke-WebRequest -Uri "http://$HostAddress`:$Port/v1/models" -UseBasicParsing -TimeoutSec 10).Content
        return ($raw | ConvertFrom-Json).data[0]
    } catch { return $null }
}

function Read-State {
    if (-not (Test-Path -LiteralPath $StateFile)) { return $null }
    try { return (Get-Content -LiteralPath $StateFile -Raw | ConvertFrom-Json) } catch { return $null }
}

function Stop-RecordedServices {
    $state = Read-State
    if (-not $state) { Write-Note 'no recorded services (logs/local-llm.pids.json not found)'; return }
    foreach ($entry in @($state.embedding, $state.rerank)) {
        if (-not $entry -or -not $entry.pid) { continue }
        $proc = Get-Process -Id ([int]$entry.pid) -ErrorAction SilentlyContinue
        if ($proc) {
            Stop-Process -Id ([int]$entry.pid) -Force
            Write-Ok "stopped $($entry.name) (pid $($entry.pid), port $($entry.port))"
        } else {
            Write-Note "$($entry.name) (pid $($entry.pid)) is not running"
        }
    }
    Remove-Item -LiteralPath $StateFile -Force -ErrorAction SilentlyContinue
}

function Show-RecordedServices {
    $state = Read-State
    if (-not $state) { Write-Note 'no recorded services (logs/local-llm.pids.json not found)'; return }
    foreach ($entry in @($state.embedding, $state.rerank)) {
        if (-not $entry) { continue }
        $alive   = [bool](Get-Process -Id ([int]$entry.pid) -ErrorAction SilentlyContinue)
        $healthy = $false
        if ($alive) {
            try {
                $resp = Invoke-WebRequest -Uri "http://$HostAddress`:$($entry.port)/health" -UseBasicParsing -TimeoutSec 3
                $healthy = ($resp.StatusCode -eq 200)
            } catch { }
        }
        $stateText = if (-not $alive) { 'stopped' } elseif ($healthy) { 'healthy' } else { 'starting/unhealthy' }
        Write-Host ("{0,-10} {1,-10} pid={2,-7} http://{3}:{4}" -f $entry.name, $stateText, $entry.pid, $HostAddress, $entry.port)
    }
}

function Start-LlamaServer {
    param([string]$Executable, [string[]]$Arguments, [string]$LogPath)
    $stderrPath = "$LogPath.err"
    if (Test-Path -LiteralPath $LogPath) { Remove-Item -LiteralPath $LogPath -Force }
    if (Test-Path -LiteralPath $stderrPath) { Remove-Item -LiteralPath $stderrPath -Force }
    return Start-Process -FilePath $Executable -ArgumentList $Arguments -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $LogPath -RedirectStandardError $stderrPath
}

New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

if ($Stop)   { Stop-RecordedServices; return }
if ($Status) { Show-RecordedServices; return }

if ($EmbeddingPort -eq $RerankPort) {
    throw "EmbeddingPort and RerankPort must differ (both were $EmbeddingPort)."
}
if (-not $HostAddress) { throw 'HostAddress must not be empty.' }
if ($ContextSize -lt 512) { throw "ContextSize must be >= 512 (got $ContextSize)." }

$exe = Resolve-LlamaExecutable -Explicit $LlamaExe
Write-Step "llama.cpp: $exe"

$embeddingModel = Resolve-ModelFile -Directory $ModelsDir -PreferredName $EmbeddingModelName -FallbackPattern '*embedding*.gguf'
$rerankModel    = Resolve-ModelFile -Directory $ModelsDir -PreferredName $RerankModelName    -FallbackPattern '*rerank*.gguf'
Write-Step "embedding model: $embeddingModel"
Write-Step "rerank model   : $rerankModel"

foreach ($port in @($EmbeddingPort, $RerankPort)) {
    $owner = Get-PortOwner -Port $port
    if ($owner) {
        throw "port $port is already in use by pid $owner. Run with -Stop, or pass -EmbeddingPort/-RerankPort."
    }
}

$embeddingAlias = [System.IO.Path]::GetFileNameWithoutExtension($embeddingModel)
$rerankAlias    = [System.IO.Path]::GetFileNameWithoutExtension($rerankModel)

$embeddingLog = Join-Path $LogDir "embedding-$EmbeddingPort.log"
$rerankLog    = Join-Path $LogDir "rerank-$RerankPort.log"

Write-Step "starting embedding on port $EmbeddingPort ..."
$embeddingArgs = Get-ServeArguments -Executable $exe -ModelPath $embeddingModel -Port $EmbeddingPort `
    -Alias $embeddingAlias -ExtraArgs @('--embedding') -Ctx $ContextSize
$embeddingProc = Start-LlamaServer -Executable $exe -Arguments $embeddingArgs -LogPath $embeddingLog

Write-Step "starting rerank on port $RerankPort ..."
$rerankArgs = Get-ServeArguments -Executable $exe -ModelPath $rerankModel -Port $RerankPort `
    -Alias $rerankAlias -ExtraArgs @('--rerank') -Ctx $ContextSize
$rerankProc = Start-LlamaServer -Executable $exe -Arguments $rerankArgs -LogPath $rerankLog

$embeddingReady = Wait-EndpointReady -Port $EmbeddingPort -TimeoutSec $ReadyTimeoutSec
$rerankReady    = Wait-EndpointReady -Port $RerankPort    -TimeoutSec $ReadyTimeoutSec

if (-not $embeddingReady) { Write-Fail "embedding service did not become ready; see $embeddingLog.err" }
if (-not $rerankReady)    { Write-Fail "rerank service did not become ready; see $rerankLog.err" }

if (-not ($embeddingReady -and $rerankReady)) {
    foreach ($proc in @($embeddingProc, $rerankProc)) {
        if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
    }
    Write-Fail 'startup failed, started processes were stopped.'
    exit 1
}

$embeddingInfo = Get-EndpointModelInfo -Port $EmbeddingPort
$rerankInfo    = Get-EndpointModelInfo -Port $RerankPort
$embeddingDims = if ($embeddingInfo -and $embeddingInfo.meta) { $embeddingInfo.meta.n_embd } else { 'unknown' }

$state = [ordered]@{
    startedAt = (Get-Date).ToString('s')
    embedding = [ordered]@{ name = 'embedding'; pid = $embeddingProc.Id; port = $EmbeddingPort; model = $embeddingModel; alias = $embeddingAlias; log = $embeddingLog }
    rerank    = [ordered]@{ name = 'rerank';    pid = $rerankProc.Id;    port = $RerankPort;    model = $rerankModel;    alias = $rerankAlias;    log = $rerankLog }
}
$state | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $StateFile -Encoding UTF8

Write-Host ''
Write-Ok 'both services are up'
Write-Host ("  embedding  POST http://{0}:{1}/v1/embeddings   model={2}  dims={3}" -f $HostAddress, $EmbeddingPort, $embeddingAlias, $embeddingDims)
Write-Host ("             GET  http://{0}:{1}/v1/models       (model list / metadata)" -f $HostAddress, $EmbeddingPort)
Write-Host ("  rerank     POST http://{0}:{1}/v1/rerank       model={2}" -f $HostAddress, $RerankPort, $rerankAlias)
Write-Host ("             GET  http://{0}:{1}/v1/models" -f $HostAddress, $RerankPort)
Write-Host ''
Write-Note "state file : $StateFile"
Write-Note "logs       : $LogDir"
Write-Note 'stop with  : powershell -ExecutionPolicy Bypass -File .\scripts\start-local-llm.ps1 -Stop'
