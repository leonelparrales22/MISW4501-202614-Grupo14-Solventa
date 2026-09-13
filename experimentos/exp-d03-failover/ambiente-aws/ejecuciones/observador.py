"""Observador del staging durante una ejecución: escribe timeline.csv con cada
transición de salud de los targets del ALB, cada evento del servicio ECS y cada
evento de RDS, usando la marca de tiempo del evento cuando AWS la provee."""

import csv
import datetime as dt
import re
import threading
import time

from staging import ahora, iso, eventos_rds, eventos_servicio, salud_targets, tareas_del_servicio

PATRONES_ECS = [
    (re.compile(r"has stopped (\d+) running tasks: \(task ([0-9a-f]+)\)"), "tarea_detenida"),
    (re.compile(r"has started (\d+) tasks: \(task ([0-9a-f]+)\)"), "tarea_nueva"),
    (re.compile(r"\(task ([0-9a-f]+)\) failed ELB health checks"), "ecs_tarea_no_sana"),
    (re.compile(r"\(task ([0-9a-f]+)\) failed container health checks"), "ecs_tarea_no_sana"),
    (re.compile(r"registered (\d+) targets"), "ecs_target_registrado"),
    (re.compile(r"deregistered (\d+) targets"), "ecs_target_deregistrado"),
    (re.compile(r"has reached a steady state"), "servicio_estable"),
]

PATRONES_RDS = [
    (re.compile(r"Multi-AZ instance failover started", re.I), "base_failover_inicio"),
    (re.compile(r"Multi-AZ instance failover completed", re.I), "base_failover_fin"),
    (re.compile(r"Multi-AZ failover to standby complete", re.I), "base_failover_dns"),
    (re.compile(r"Abandoning user requested failover", re.I), "base_failover_rechazado"),
    (re.compile(r"DB instance restarted", re.I), "base_reiniciada"),
    (re.compile(r"DB instance shutdown", re.I), "base_apagada"),
]


class Observador(threading.Thread):
    def __init__(self, cl, cluster, servicio, tg_arn, rds_id, ruta_timeline, intervalo_targets=1.0, intervalo_eventos=5.0):
        super().__init__(daemon=True)
        self.cl = cl
        self.cluster = cluster
        self.servicio = servicio
        self.tg_arn = tg_arn
        self.rds_id = rds_id
        self.ruta = ruta_timeline
        self.intervalo_targets = intervalo_targets
        self.intervalo_eventos = intervalo_eventos
        self.inicio = ahora()
        self.detener_evento = threading.Event()
        self.candado = threading.Lock()
        self.estado_targets = {}
        self.ip_a_tarea = {}
        self.tareas = {}
        self.ecs_vistos = set()
        self.rds_vistos = set()
        self.eventos = []
        with open(self.ruta, "w", encoding="utf-8", newline="") as f:
            csv.writer(f).writerow(["ts", "evento", "detalle"])

    def marcar(self, evento, detalle="", momento=None):
        momento = momento or ahora()
        with self.candado:
            self.eventos.append((momento, evento, detalle))
            with open(self.ruta, "a", encoding="utf-8", newline="") as f:
                csv.writer(f).writerow([iso(momento), evento, detalle])
        print(f"[{iso(momento)}] {evento} {detalle}", flush=True)

    def visto(self, evento):
        return any(e[1] == evento for e in self.eventos)

    def instancia_de(self, ip):
        return self.ip_a_tarea.get(ip, ip)

    def refrescar_tareas(self):
        for t in tareas_del_servicio(self.cl, self.cluster, self.servicio):
            self.tareas[t["id"]] = t
            if t["ip_privada"]:
                self.ip_a_tarea[t["ip_privada"]] = t["id"]

    def sondear_targets(self, inicial=False):
        actual = salud_targets(self.cl, self.tg_arn)
        for ip, (estado, razon) in actual.items():
            previo = self.estado_targets.get(ip)
            if previo != estado:
                evento = "target_estado_inicial" if inicial or previo is None else f"target_{estado}"
                self.marcar(evento, f"instancia={self.instancia_de(ip)};ip={ip};estado={estado};razon={razon}")
                self.estado_targets[ip] = estado
        for ip in list(self.estado_targets):
            if ip not in actual:
                self.marcar("target_retirado", f"instancia={self.instancia_de(ip)};ip={ip}")
                del self.estado_targets[ip]

    def sondear_ecs(self):
        for e in eventos_servicio(self.cl, self.cluster, self.servicio):
            if e["id"] in self.ecs_vistos or e["ts"] < self.inicio - dt.timedelta(seconds=30):
                continue
            self.ecs_vistos.add(e["id"])
            nombre, detalle = "ecs_evento", ""
            for patron, etiqueta in PATRONES_ECS:
                m = patron.search(e["mensaje"])
                if m:
                    nombre = etiqueta
                    grupos = [g for g in m.groups() if g and re.fullmatch(r"[0-9a-f]{20,}", g)]
                    if grupos:
                        detalle = f"instancia={grupos[0]}"
                    break
            self.marcar(nombre, detalle or f"mensaje={e['mensaje'][:120]}", e["ts"])

    def sondear_rds(self):
        for e in eventos_rds(self.cl, self.rds_id, self.inicio - dt.timedelta(minutes=2)):
            clave = (e["ts"].isoformat(), e["mensaje"])
            if clave in self.rds_vistos:
                continue
            self.rds_vistos.add(clave)
            nombre = "rds_evento"
            for patron, etiqueta in PATRONES_RDS:
                if patron.search(e["mensaje"]):
                    nombre = etiqueta
                    break
            self.marcar(nombre, f"mensaje={e['mensaje'][:120]}", e["ts"])

    def run(self):
        self.refrescar_tareas()
        self.sondear_targets(inicial=True)
        ultimo_eventos = 0.0
        while not self.detener_evento.is_set():
            try:
                self.sondear_targets()
                if time.monotonic() - ultimo_eventos >= self.intervalo_eventos:
                    self.refrescar_tareas()
                    self.sondear_ecs()
                    self.sondear_rds()
                    ultimo_eventos = time.monotonic()
            except Exception as error:  # noqa: BLE001 - el observador no debe morir por un fallo transitorio de API
                self.marcar("observador_error", f"detalle={type(error).__name__}: {str(error)[:100]}")
                time.sleep(2)
            self.detener_evento.wait(self.intervalo_targets)

    def detener(self):
        self.detener_evento.set()
        self.join(timeout=30)
