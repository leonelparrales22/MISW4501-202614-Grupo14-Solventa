# Ejecución local del experimento EXP-D03 (Docker Compose).
#
# Levanta HAProxy + 2 instancias del stub + PostgreSQL, genera carga con k6,
# inyecta la falla de cómputo en el segundo TFallaComputo y la falla de base
# en el segundo TFallaBase, recoge la evidencia y ejecuta el análisis.
#
# Ejemplo:
#   .\ejecucion.ps1 -Config M1-P1 -HcInter 5s -HcFall 2 -HcTimeout 3s
#   .\ejecucion.ps1 -Config smoke -Duracion 120 -TFallaComputo 30 -TFallaBase 75

param(
    [string]$RunId = ("local-" + (Get-Date -Format "yyyyMMdd-HHmmss")),
    [string]$Config = "M2-P1",
    [string]$HcInter = "10s",
    [int]$HcFall = 2,
    [int]$HcRise = 2,
    [string]$HcTimeout = "5s",
    [string]$PoolMaxLifetime = "0",
    [string]$PoolMaxIdleTime = "0",
    [string]$HealthMode = "superficial",
    [int]$Duracion = 600,
    [int]$TFallaComputo = 120,
    [int]$TFallaBase = 360,
    [int]$Rps = 50,
    [ValidateSet("hang", "crash")][string]$TipoFalla = "hang",
    [ValidateSet("a", "b")][string]$Instancia = "a",
    [int]$BaseCaidaSegundos = 45,
    [int]$RetrasoReemplazo = 20,
    [switch]$SinAnalisis
)

$ErrorActionPreference = "Stop"
$raiz = Split-Path -Parent $PSScriptRoot
$resultados = Join-Path $raiz "resultados\$RunId"
New-Item -ItemType Directory -Force $resultados | Out-Null

$env:HC_INTER = $HcInter
$env:HC_FALL = "$HcFall"
$env:HC_RISE = "$HcRise"
$env:HC_TIMEOUT = $HcTimeout
$env:POOL_MAX_LIFETIME = $PoolMaxLifetime
$env:POOL_MAX_IDLE_TIME = $PoolMaxIdleTime
$env:HEALTH_MODE = $HealthMode
$env:RUN_ID = $RunId
$env:CONFIG = $Config
$env:RPS = "$Rps"
$env:DURACION = "${Duracion}s"
$token = if ($env:ADMIN_TOKEN) { $env:ADMIN_TOKEN } else { "local-token" }

$timeline = Join-Path $resultados "timeline.csv"
"ts,evento,detalle" | Out-File -Encoding utf8 $timeline

function Marcar([string]$evento, [string]$detalle = "") {
    $ts = (Get-Date).ToUniversalTime().ToString("o")
    "$ts,$evento,$detalle" | Out-File -Encoding utf8 -Append $timeline
    Write-Host "[$ts] $evento $detalle"
}

function Esperar-Salud([int]$puerto) {
    for ($i = 0; $i -lt 120; $i++) {
        try {
            $r = Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:$puerto/health" -TimeoutSec 2
            if ($r.StatusCode -eq 200) { return }
        } catch {}
        Start-Sleep -Seconds 1
    }
    throw "La instancia en el puerto $puerto no respondió al health check"
}

Push-Location $PSScriptRoot
try {
    Write-Host "== Configuración ${Config}: health $HcInter x $HcFall (timeout $HcTimeout), pool lifetime=$PoolMaxLifetime idle=$PoolMaxIdleTime, health $HealthMode =="
    docker compose up -d --build --force-recreate db stub-a stub-b haproxy
    Esperar-Salud 18081
    Esperar-Salud 18082
    $segundosInter = [double]($HcInter.TrimEnd("s"))
    Start-Sleep -Seconds ([int]($segundosInter * $HcRise) + 3)

    $k6 = (docker compose --profile carga run -d k6).Trim()
    $t0 = Get-Date
    $desde = $t0.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    Marcar "k6_inicio" "config=$Config;rps=$Rps;duracion=$Duracion"

    $inyectada = $false
    $detectada = $null
    $reiniciada = $false
    $baseCaida = $null
    $baseArriba = $false
    $puertoFalla = if ($Instancia -eq "a") { 18081 } else { 18082 }

    while ($true) {
        $t = ((Get-Date) - $t0).TotalSeconds

        if (-not $inyectada -and $t -ge $TFallaComputo) {
            $cuerpo = (@{ tipo = $TipoFalla } | ConvertTo-Json -Compress)
            Invoke-RestMethod -Method Post -Uri "http://localhost:$puertoFalla/admin/falla" -Headers @{ "X-Admin-Token" = $token } -ContentType "application/json" -Body $cuerpo -TimeoutSec 5 | Out-Null
            Marcar "falla_computo" "tipo=$TipoFalla;instancia=$Instancia"
            $inyectada = $true
        }

        if ($inyectada -and -not $detectada) {
            $logs = (docker compose logs --no-log-prefix --since $desde haproxy 2>$null) -join "`n"
            if ($logs -match "Server originacion/$Instancia is DOWN") {
                $detectada = Get-Date
                Write-Host "[$($detectada.ToUniversalTime().ToString('o'))] HAProxy marcó $Instancia como DOWN"
            }
        }

        if ($TipoFalla -eq "hang" -and $detectada -and -not $reiniciada -and ((Get-Date) - $detectada).TotalSeconds -ge $RetrasoReemplazo) {
            docker compose restart "stub-$Instancia" | Out-Null
            Marcar "reemplazo_iniciado" "instancia=$Instancia;metodo=docker_restart"
            $reiniciada = $true
        }

        if (-not $baseCaida -and $t -ge $TFallaBase) {
            docker compose kill db | Out-Null
            $baseCaida = Get-Date
            Marcar "falla_base" "metodo=docker_kill"
        }

        if ($baseCaida -and -not $baseArriba -and ((Get-Date) - $baseCaida).TotalSeconds -ge $BaseCaidaSegundos) {
            docker compose start db | Out-Null
            Marcar "base_restablecida" "metodo=docker_start"
            $baseArriba = $true
        }

        if ($t -ge ($Duracion + 8)) { break }
        Start-Sleep -Seconds 1
    }

    docker wait $k6 | Out-Null
    Marcar "k6_fin"

    cmd /c "docker logs $k6 > `"$resultados\k6_resumen.txt`" 2>&1"
    cmd /c "docker compose logs -t --no-log-prefix --since $desde haproxy > `"$resultados\haproxy.log`" 2>nul"
    cmd /c "docker compose logs -t --no-log-prefix --since $desde stub-a stub-b > `"$resultados\stubs.log`" 2>nul"
    docker rm $k6 | Out-Null

    $puertoLectura = if ($Instancia -eq "a") { 18082 } else { 18081 }
    $respuesta = Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:$puertoLectura/admin/ofertas?run_id=$RunId" -Headers @{ "X-Admin-Token" = $token } -TimeoutSec 180
    [System.IO.File]::WriteAllText((Join-Path $resultados "ids.json"), $respuesta.Content)

    if (-not $SinAnalisis) {
        docker compose --profile analisis run --rm --build analizador
    }
    Write-Host "== Evidencia en $resultados =="
} finally {
    Pop-Location
}
