"""Utilidades compartidas por los scripts de la fase en AWS del experimento EXP-D03."""

import datetime as dt
import json
import os
import subprocess

import boto3
from botocore.config import Config

DIR_EJECUCIONES = os.path.dirname(os.path.abspath(__file__))
RAIZ = os.path.dirname(os.path.dirname(DIR_EJECUCIONES))
DIR_TERRAFORM = os.path.join(RAIZ, "ambiente-aws", "infraestructura")
DIR_RESULTADOS = os.path.join(RAIZ, "resultados")
DIR_K6 = os.path.join(RAIZ, "carga")
DIR_ANALISIS = os.path.join(RAIZ, "analisis")


def ahora():
    return dt.datetime.now(dt.timezone.utc)


def iso(momento):
    return momento.astimezone(dt.timezone.utc).isoformat()


def salidas_terraform():
    """Lee `terraform output -json` del staging y devuelve un dict plano."""
    proceso = subprocess.run(
        ["terraform", "output", "-json"], cwd=DIR_TERRAFORM, capture_output=True, text=True, check=True
    )
    return {clave: valor["value"] for clave, valor in json.loads(proceso.stdout).items()}


def clientes(region):
    cfg = Config(retries={"total_max_attempts": 5, "mode": "adaptive"}, connect_timeout=5, read_timeout=20)
    sesion = boto3.Session(region_name=region)
    return {
        "ecs": sesion.client("ecs", config=cfg),
        "ec2": sesion.client("ec2", config=cfg),
        "elbv2": sesion.client("elbv2", config=cfg),
        "rds": sesion.client("rds", config=cfg),
    }


def tareas_del_servicio(cl, cluster, servicio):
    """Tareas del servicio con id, estado, zona, IP privada e IP pública."""
    paginador = cl["ecs"].get_paginator("list_tasks")
    arns = list(paginador.paginate(cluster=cluster, serviceName=servicio).search("taskArns[]"))
    tareas = []
    for i in range(0, len(arns), 100):
        respuesta = cl["ecs"].describe_tasks(cluster=cluster, tasks=arns[i:i + 100])
        for t in respuesta["tasks"]:
            detalles = {}
            for adjunto in t.get("attachments", []):
                if adjunto.get("type") == "ElasticNetworkInterface":
                    detalles = {d["name"]: d["value"] for d in adjunto.get("details", [])}
            tareas.append({
                "id": t["taskArn"].rsplit("/", 1)[-1],
                "arn": t["taskArn"],
                "estado": t.get("lastStatus"),
                "zona": t.get("availabilityZone"),
                "ip_privada": detalles.get("privateIPv4Address"),
                "eni": detalles.get("networkInterfaceId"),
                "ip_publica": None,
                "inicio": iso(t["startedAt"]) if t.get("startedAt") else None,
                "definicion": t.get("taskDefinitionArn", "").rsplit("/", 1)[-1],
            })
    enis = [t["eni"] for t in tareas if t["eni"]]
    if enis:
        try:
            respuesta = cl["ec2"].describe_network_interfaces(NetworkInterfaceIds=enis)
        except cl["ec2"].exceptions.ClientError:
            respuesta = {"NetworkInterfaces": []}
        publicas = {n["NetworkInterfaceId"]: n.get("Association", {}).get("PublicIp") for n in respuesta["NetworkInterfaces"]}
        for t in tareas:
            t["ip_publica"] = publicas.get(t["eni"])
    return tareas


def salud_targets(cl, tg_arn):
    """Estado de cada target: {ip: (estado, razon)}."""
    respuesta = cl["elbv2"].describe_target_health(TargetGroupArn=tg_arn)
    salida = {}
    for d in respuesta["TargetHealthDescriptions"]:
        salud = d.get("TargetHealth", {})
        salida[d["Target"]["Id"]] = (salud.get("State", "desconocido"), salud.get("Reason", ""))
    return salida


def despliegue_estable(cl, cluster, servicio):
    respuesta = cl["ecs"].describe_services(cluster=cluster, services=[servicio])
    if not respuesta["services"]:
        return False, "servicio no encontrado"
    s = respuesta["services"][0]
    despliegues = s.get("deployments", [])
    if len(despliegues) != 1 or despliegues[0].get("rolloutState") not in ("COMPLETED", None):
        return False, f"despliegue en curso ({len(despliegues)} despliegues)"
    if s["runningCount"] != s["desiredCount"]:
        return False, f"tareas {s['runningCount']}/{s['desiredCount']}"
    return True, "estable"


def eventos_servicio(cl, cluster, servicio):
    respuesta = cl["ecs"].describe_services(cluster=cluster, services=[servicio])
    if not respuesta["services"]:
        return []
    return [{"id": e["id"], "ts": e["createdAt"], "mensaje": e["message"]} for e in respuesta["services"][0].get("events", [])]


def estado_rds(cl, identificador):
    respuesta = cl["rds"].describe_db_instances(DBInstanceIdentifier=identificador)
    i = respuesta["DBInstances"][0]
    return {
        "estado": i["DBInstanceStatus"],
        "multi_az": i.get("MultiAZ"),
        "zona": i.get("AvailabilityZone"),
        "zona_secundaria": i.get("SecondaryAvailabilityZone"),
    }


def eventos_rds(cl, identificador, desde, duracion_min=60):
    respuesta = cl["rds"].describe_events(
        SourceIdentifier=identificador,
        SourceType="db-instance",
        StartTime=desde,
        EndTime=ahora() + dt.timedelta(minutes=1),
    )
    return [{"ts": e["Date"], "mensaje": e["Message"]} for e in respuesta.get("Events", [])]
