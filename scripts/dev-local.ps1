<#
.SYNOPSIS
    AegisVision Windows local development launcher.
.DESCRIPTION
    Starts local Go backend services (using backend/config.dev.yaml) and Vite dev server
    in separate titled PowerShell windows. Infrastructure (Postgres/Redis/RabbitMQ/MinIO/MediaMTX)
    is expected to be running on your VM or local Docker Compose.
.EXAMPLE
    .\scripts\dev-local.ps1              # Minimal: api + scheduler + ws + web
    .\scripts\dev-local.ps1 -Full        # Full: api + scheduler + ws + ingest + dispatcher + agent + web
    .\scripts\dev-local.ps1 -SkipWeb     # Backend only
#>
param(
    [switch]$Full,
    [switch]$SkipWeb,
    [string]$ConfigPath = "config.dev.yaml",
    [string]$AgentConfigPath = "agent.dev.yaml"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$backendDir = Join-Path $repoRoot "backend"
$webDir = Join-Path $repoRoot "web"
$resolvedConfig = Join-Path $backendDir $ConfigPath
$resolvedAgentConfig = Join-Path $backendDir $AgentConfigPath

if (-not (Test-Path $resolvedConfig)) {
    Write-Error "Backend config not found: $resolvedConfig`nCopy deploy/baremetal/config.yaml.example to backend/$ConfigPath first."
    exit 1
}

function Start-DevWindow {
    param(
        [string]$Title,
        [string]$WorkingDir,
        [string]$Command
    )
    $fullCmd = "`$Host.UI.RawUI.WindowTitle = '$Title'; Write-Host '=== $Title ===' -ForegroundColor Cyan; $Command"
    Start-Process -FilePath "powershell.exe" `
        -WorkingDirectory $WorkingDir `
        -ArgumentList @("-NoExit", "-Command", $fullCmd) | Out-Null
    Write-Host "[STARTED] $Title" -ForegroundColor Green
}

$services = @(
    @{ Name = "aegis-api (:8081)";       Pkg = "./cmd/aegis-api";       Cfg = $ConfigPath },
    @{ Name = "aegis-scheduler (:8083)"; Pkg = "./cmd/aegis-scheduler"; Cfg = $ConfigPath },
    @{ Name = "aegis-ws (:8082)";        Pkg = "./cmd/aegis-ws";        Cfg = $ConfigPath }
)

if ($Full) {
    $services += @(
        @{ Name = "aegis-ingest (:9090)";     Pkg = "./cmd/aegis-ingest";     Cfg = $ConfigPath },
        @{ Name = "aegis-dispatcher (:8084)"; Pkg = "./cmd/aegis-dispatcher"; Cfg = $ConfigPath }
    )
    if (Test-Path $resolvedAgentConfig) {
        $services += @{ Name = "aegis-agent"; Pkg = "./cmd/aegis-agent"; Cfg = $AgentConfigPath }
    } else {
        Write-Warning "Skipping aegis-agent: $resolvedAgentConfig not found."
    }
}

foreach ($svc in $services) {
    Start-DevWindow -Title $svc.Name -WorkingDir $backendDir -Command "go run $($svc.Pkg) -config ./$($svc.Cfg)"
}

if (-not $SkipWeb) {
    Start-DevWindow -Title "aegis-web (:8848)" -WorkingDir $repoRoot -Command "pnpm.cmd --dir web dev"
}

Write-Host ""
Write-Host "All local dev windows launched. Open http://127.0.0.1:8848 once ready." -ForegroundColor Yellow
