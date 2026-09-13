#!/usr/bin/env python3
"""Ejecuta una ejecución del experimento EXP-D03 sobre el staging en AWS.

Precondiciones: staging aplicado con Terraform (terraform/), imagen publicada
en ECR, k6 instalado en el equipo y boto3 disponible.

Ejemplos:
  python ejecucion.py --config M2-P1
  python ejecucion.py --config M2-P1 --tipo-falla stop-task --run-id C1-retiro-planeado
  python ejecucion.py --config C3-profundo --run-id C3-health-profundo
"""

import argparse
import datetime as dt
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

from botocore.exceptions import ClientError

from staging import (DIR_ANALISIS, DIR_K6, DIR_RESULTADOS, ahora, clientes, despliegue_estable, estado_rds,
                       eventos_rds, iso, salidas_terraform, salud_targets, tareas_del_servicio)
from observador import Observador

ENFRIAMIENTO_FAILOVER_S = 300


class PrecondicionNoCumplida(Exception):
    pass


def esperar_estable(cl, salidas, tiempo_max_s=1200):
    """Espera a que el servicio esté estable, con 2 targets healthy y RDS disponible con standby."""
    limite = time.monotonic() + tiempo_max_s
    ultimo = ""
    while time.monotonic() < limite:
        estable, motivo = despliegue_estable(cl, salidas["cluster"], salidas["servicio"])
        healthy = [ip for ip, (estado, _) in salud_targets(cl, salidas["target_group_arn"]).items() if estado == "healthy"]
        rds = estado_rds(cl, salidas["rds_identificador"])
        listo = estable and len(healthy) >= 2 and rds["estado"] == "available" and rds["zona_secundaria"]
        descripcion = f"ecs={motivo} targets_healthy={len(healthy)} rds={rds['estado']} standby={rds['zona_secundaria']}"
        if descripcion != ultimo:
            print(f"[{iso(ahora())}] esperando: {descripcion}", flush=True)
            ultimo = descripcion
        if listo:
            return
        time.sleep(10)
    raise PrecondicionNoCumplida(f"el staging no se estabilizó en {tiempo_max_s} s: {ultimo}")


def esperar_enfriamiento_rds(cl, identificador):
    """RDS rechaza un failover forzado si hubo otro reciente (RDS-EVENT-0034)."""
    desde = ahora() - dt.timedelta(minutes=30)
    recientes = [e for e in eventos_rds(cl, identificador, desde, 31) if "failover" in e["mensaje"].lower()]
    if not recientes:
        return
    ultimo = max(e["ts"] for e in recientes)
    restante = ENFRIAMIENTO_FAILOVER_S - (ahora() - ultimo).total_seconds()
    if restante > 0:
        print(f"[{iso(ahora())}] enfriamiento tras el failover anterior: {restante:.0f} s", flush=True)
        time.sleep(restante)


def asegurar_acceso_operador(cl, prefijo="expd03"):
    """La IP pública del operador puede cambiar durante una sesión (proveedor
    inestable). Antes de hablar con las tareas se agrega la IP actual al security
    group de las tareas si aún no está autorizada en el puerto 8080."""
    try:
        with urllib.request.urlopen("https://checkip.amazonaws.com", timeout=10) as r:
            ip = r.read().decode().strip()
    except (urllib.error.URLError, OSError):
        return None
    grupos = cl["ec2"].describe_security_groups(Filters=[{"Name": "group-name", "Values": [f"{prefijo}-sg-tarea"]}])["SecurityGroups"]
    if not grupos:
        return ip
    sg = grupos[0]
    cidr = f"{ip}/32"
    for permiso in sg.get("IpPermissions", []):
        if permiso.get("FromPort") == 8080 and any(r.get("CidrIp") == cidr for r in permiso.get("IpRanges", [])):
            return ip
    cl["ec2"].authorize_security_group_ingress(GroupId=sg["GroupId"], IpPermissions=[{
        "IpProtocol": "tcp", "FromPort": 8080, "ToPort": 8080,
        "IpRanges": [{"CidrIp": cidr, "Description": "operador (IP actualizada durante la ejecucion)"}],
    }])
    print(f"[{iso(ahora())}] security group actualizado con la IP actual del operador", flush=True)
    return ip


def peticion_http(url, metodo="GET", cuerpo=None, cabeceras=None, tiempo_max=5):
    datos = json.dumps(cuerpo).encode() if cuerpo is not None else None
    solicitud = urllib.request.Request(url, data=datos, method=metodo, headers=cabeceras or {})
    if datos is not None:
        solicitud.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(solicitud, timeout=tiempo_max) as respuesta:
        return respuesta.status, respuesta.read()


def inyectar_falla_computo(cl, salidas, tipo, victima, observador):
    if tipo == "stop-task":
        cl["ecs"].stop_task(cluster=salidas["cluster"], task=victima["arn"], reason="EXP-D03 retiro planeado del servicio")
        observador.marcar("retiro_planeado", f"tipo=stop-task;instancia={victima['id']};ip={victima['ip_privada']}")
        return
    asegurar_acceso_operador(cl)
    url = f"http://{victima['ip_publica']}:8080/admin/falla"
    estado, _ = peticion_http(url, "POST", {"tipo": tipo}, {"X-Admin-Token": salidas["admin_token"]})
    if estado != 202:
        raise RuntimeError(f"la inyección respondió {estado}")
    observador.marcar("falla_computo", f"tipo={tipo};instancia={victima['id']};ip={victima['ip_privada']}")


def forzar_failover(cl, salidas, observador):
    cl["rds"].reboot_db_instance(DBInstanceIdentifier=salidas["rds_identificador"], ForceFailover=True)
    observador.marcar("falla_base", "metodo=reboot_force_failover")


def descargar_ids(cl, salidas, tareas, victima_id, run_id, ruta):
    candidatas = [t for t in tareas if t["id"] != victima_id and t["ip_publica"] and t["estado"] == "RUNNING"]
    for intento in range(3):
        asegurar_acceso_operador(cl)
        for t in candidatas:
            try:
                _, cuerpo = peticion_http(f"http://{t['ip_publica']}:8080/admin/ofertas?run_id={run_id}",
                                          cabeceras={"X-Admin-Token": salidas["admin_token"]}, tiempo_max=240)
            except (urllib.error.URLError, OSError) as error:
                print(f"no se pudo leer la evidencia desde {t['id']}: {str(error)[:80]}", flush=True)
                continue
            with open(ruta, "wb") as f:
                f.write(cuerpo)
            return t["id"]
        time.sleep(20)
    raise RuntimeError("ninguna tarea respondió la lectura de evidencia")


def ejecutar(args):
    salidas = salidas_terraform()
    cl = clientes(salidas["region"])
    run_id = args.run_id or args.config
    directorio = os.path.join(DIR_RESULTADOS, run_id)
    os.makedirs(directorio, exist_ok=True)

    esperar_estable(cl, salidas)
    if not args.sin_failover:
        esperar_enfriamiento_rds(cl, salidas["rds_identificador"])

    tareas = sorted(tareas_del_servicio(cl, salidas["cluster"], salidas["servicio"]), key=lambda t: t["id"])
    corriendo = [t for t in tareas if t["estado"] == "RUNNING"]
    if len(corriendo) < 2:
        raise PrecondicionNoCumplida(f"solo hay {len(corriendo)} tareas corriendo")
    victima = corriendo[0]
    if args.tipo_falla != "stop-task" and not victima["ip_publica"]:
        raise PrecondicionNoCumplida("la tarea víctima no tiene IP pública para inyectar la falla")

    observador = Observador(cl, salidas["cluster"], salidas["servicio"], salidas["target_group_arn"],
                            salidas["rds_identificador"], os.path.join(directorio, "timeline.csv"))
    observador.start()
    time.sleep(3)

    meta = {
        "run_id": run_id, "config": args.config, "tipo_falla": args.tipo_falla, "rps": args.rps,
        "perfil": args.perfil, "rps_max": args.rps_max,
        "duracion_s": args.duracion, "t_falla_computo_s": args.t_falla_computo, "t_falla_base_s": args.t_falla_base,
        "sin_failover": args.sin_failover, "sin_falla_computo": args.sin_falla_computo,
        "parametros_staging": salidas.get("configuracion"), "victima": victima, "tareas_iniciales": corriendo,
        "api_url": salidas["api_url"], "inicio": iso(ahora()),
    }

    entorno = dict(os.environ, K6_CSV_TIME_FORMAT="rfc3339_nano")
    comando = [args.k6, "run", "--quiet", "--out", f"csv={os.path.join(directorio, 'k6.csv')}",
               "-e", f"BASE_URL={salidas['api_url']}", "-e", f"API_KEY={salidas['api_key']}",
               "-e", f"RUN_ID={run_id}", "-e", f"CONFIG={args.config}", "-e", f"RPS={args.rps}",
               "-e", f"DURACION={args.duracion}s", "-e", f"PERFIL={args.perfil}", "-e", f"RPS_MAX={args.rps_max}",
               os.path.join(DIR_K6, "oferta.js")]
    with open(os.path.join(directorio, "k6_resumen.txt"), "w", encoding="utf-8") as salida_k6:
        proceso = subprocess.Popen(comando, env=entorno, stdout=salida_k6, stderr=subprocess.STDOUT)
        t0 = time.monotonic()
        observador.marcar("k6_inicio", f"config={args.config};rps={args.rps};duracion={args.duracion};tipo_falla={args.tipo_falla}")
        inyectada = args.sin_falla_computo
        failover = args.sin_failover
        try:
            while proceso.poll() is None:
                t = time.monotonic() - t0
                if not inyectada and t >= args.t_falla_computo:
                    inyectar_falla_computo(cl, salidas, args.tipo_falla, victima, observador)
                    inyectada = True
                if not failover and t >= args.t_falla_base:
                    forzar_failover(cl, salidas, observador)
                    failover = True
                time.sleep(0.5)
        finally:
            if proceso.poll() is None:
                proceso.terminate()
    observador.marcar("k6_fin", f"codigo_salida={proceso.returncode}")

    # Espera final: recoger los eventos de RDS (llegan con retraso) y la reintegración.
    limite = time.monotonic() + args.estabilizacion
    while time.monotonic() < limite:
        healthy = [ip for ip, (estado, _) in salud_targets(cl, salidas["target_group_arn"]).items() if estado == "healthy"]
        failover_cerrado = args.sin_failover or observador.visto("base_failover_fin")
        if len(healthy) >= 2 and failover_cerrado and time.monotonic() - t0 > args.duracion + 90:
            break
        time.sleep(5)
    observador.detener()

    tareas_finales = tareas_del_servicio(cl, salidas["cluster"], salidas["servicio"])
    meta["lector_evidencia"] = descargar_ids(cl, salidas, tareas_finales, victima["id"], run_id, os.path.join(directorio, "ids.json"))
    meta["tareas_finales"] = tareas_finales
    meta["fin"] = iso(ahora())
    with open(os.path.join(directorio, "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2, default=str)

    if not args.sin_analisis:
        subprocess.run([sys.executable, os.path.join(DIR_ANALISIS, "analizar.py"), directorio], check=False)
    print(f"== Evidencia en {directorio} ==", flush=True)


def main():
    parser = argparse.ArgumentParser(description="Ejecución del experimento EXP-D03 en AWS")
    parser.add_argument("--config", required=True, help="etiqueta de la configuración (M1-P1, ..., C3-profundo)")
    parser.add_argument("--run-id", default=None)
    parser.add_argument("--tipo-falla", choices=["hang", "crash", "stop-task"], default="hang")
    parser.add_argument("--duracion", type=int, default=600)
    parser.add_argument("--t-falla-computo", type=int, default=120)
    parser.add_argument("--t-falla-base", type=int, default=360)
    parser.add_argument("--rps", type=int, default=50)
    parser.add_argument("--perfil", choices=["constante", "rampa"], default="constante",
                        help="constante: RPS fijos durante --duracion; rampa: escalones hasta --rps-max, sin fallas")
    parser.add_argument("--rps-max", type=int, default=450)
    parser.add_argument("--sin-failover", action="store_true")
    parser.add_argument("--sin-falla-computo", action="store_true")
    parser.add_argument("--estabilizacion", type=int, default=420, help="segundos máximos de espera tras k6 para eventos tardíos")
    parser.add_argument("--k6", default="k6")
    parser.add_argument("--sin-analisis", action="store_true")
    args = parser.parse_args()
    try:
        ejecutar(args)
        return 0
    except (PrecondicionNoCumplida, RuntimeError) as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    except ClientError as error:
        print(f"Error de AWS: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
