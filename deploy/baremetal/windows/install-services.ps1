<#
.SYNOPSIS
    AegisVision Windows 服务安装/卸载脚本（裸机部署，支持 NSSM）。
.DESCRIPTION
    使用 NSSM 将 AegisVision 平台 5 个无 CGO 二进制或算力机 aegis-agent 注册为 Windows 服务，
    配置自动重启与日志轮转。运行前请确保以管理员身份打开 PowerShell，并已安装 nssm.exe。
.EXAMPLE
    .\install-services.ps1 -Role platform -InstallDir "C:\AegisVision" -ConfigPath "C:\AegisVision\config.yaml"
.EXAMPLE
    .\install-services.ps1 -Role agent -InstallDir "C:\AegisVision" -ConfigPath "C:\AegisVision\agent.yaml"
.EXAMPLE
    .\install-services.ps1 -Role platform -Uninstall
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("platform", "agent")]
    [string]$Role,

    [string]$InstallDir = "C:\AegisVision",
    [string]$ConfigPath = "",
    [string]$NssmPath = "nssm.exe",
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

$nssmCmd = Get-Command $NssmPath -ErrorAction SilentlyContinue
if (-not $nssmCmd) {
    throw "未找到 NSSM ($NssmPath)。请先下载并将 nssm.exe 加入 PATH，或通过 -NssmPath 指定路径。"
}

$services = if ($Role -eq "platform") {
    @("aegis-api", "aegis-scheduler", "aegis-ingest", "aegis-ws", "aegis-dispatcher")
} else {
    @("aegis-agent")
}

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = if ($Role -eq "platform") {
        Join-Path $InstallDir "config.yaml"
    } else {
        Join-Path $InstallDir "agent.yaml"
    }
}

$logDir = Join-Path $InstallDir "logs"
if (-not $Uninstall) {
    if (-not (Test-Path $ConfigPath)) {
        throw "配置文件不存在: $ConfigPath"
    }
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
}

foreach ($svc in $services) {
    if ($Uninstall) {
        Write-Host "[uninstall] Stopping and removing $svc..."
        & $NssmPath stop $svc 2>$null | Out-Null
        & $NssmPath remove $svc confirm 2>$null | Out-Null
        continue
    }

    $binPath = Join-Path (Join-Path $InstallDir "bin") "$svc.exe"
    if (-not (Test-Path $binPath)) {
        throw "未找到可执行文件: $binPath"
    }

    if ($svc -eq "aegis-api") {
        Write-Host "[migrate] Running database migration via aegis-api migrate up..."
        & $binPath migrate up -config $ConfigPath
        if ($LASTEXITCODE -ne 0) {
            throw "数据库迁移失败 (exit code $LASTEXITCODE)"
        }
    }

    Write-Host "[install] Registering Windows service $svc..."
    & $NssmPath install $svc $binPath "-config `"$ConfigPath`""
    & $NssmPath set $svc AppDirectory $InstallDir
    & $NssmPath set $svc DisplayName "AegisVision $svc"
    & $NssmPath set $svc Start SERVICE_AUTO_START
    & $NssmPath set $svc AppStdout (Join-Path $logDir "$svc.stdout.log")
    & $NssmPath set $svc AppStderr (Join-Path $logDir "$svc.stderr.log")
    & $NssmPath set $svc AppRotateFiles 1
    & $NssmPath set $svc AppRotateBytes 52428800
    & $NssmPath set $svc AppExit Default Restart
    & $NssmPath set $svc AppRestartDelay 3000

    Write-Host "[start] Starting $svc..."
    & $NssmPath start $svc
}

Write-Host "Done."
