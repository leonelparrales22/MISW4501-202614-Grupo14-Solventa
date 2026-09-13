#!/usr/bin/env python3
"""Análisis determinista de una ejecución del experimento EXP-D03.

Entradas (en el directorio de la ejecución):
  k6.csv         salida CSV de k6; se usa la métrica `oferta_ms` con sus etiquetas
  timeline.csv   marcas de tiempo de los eventos (ts,evento,detalle)
  haproxy.log    opcional, fase local: `docker compose logs -t` de HAProxy
  ids.json       opcional: identificadores persistidos en la base para la ejecución

Salidas (en el mismo directorio):
  resumen.json, resumen.md, serie_por_segundo.csv, grafica_errores.png, grafica_p95.png

Uso:
  python analizar.py <directorio_ejecución>
  python analizar.py --consolidar <dir1> <dir2> ... > matriz.md
"""

import argparse
import csv
import json
import os
import re
import sys

import pandas as pd
import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

UMBRAL_VENTANA_COMPUTO_S = 30.0
UMBRAL_RECUPERACION_BASE_S = 120.0
HUECO_MAX_S = 2
# Una solicitud que estaba en vuelo cuando ocurrió la falla empezó hasta
# TIMEOUT_CLIENTE_S antes del evento; se atribuye a ese evento.
TIMEOUT_CLIENTE_S = 5.0
# Un intervalo de error se atribuye a un evento solo si empieza dentro de esta
# ventana posterior al evento; los demás se reportan como errores esporádicos.
ATRIBUCION_COMPUTO_S = 90.0
ATRIBUCION_BASE_S = 150.0
MINUTOS_MES = 30 * 24 * 60

EVENTOS_NO_SANO = {"monitor_unhealthy", "target_unhealthy"}
EVENTOS_SANO = {"monitor_healthy", "target_healthy"}


def parsear_ts(valor):
    s = str(valor).strip()
    if re.fullmatch(r"\d+(\.\d+)?", s):
        return float(s)
    return pd.Timestamp(s).timestamp()


def parsear_detalle(detalle):
    campos = {}
    for parte in str(detalle or "").split(";"):
        if "=" in parte:
            k, v = parte.split("=", 1)
            campos[k.strip()] = v.strip()
    return campos


def cargar_k6(ruta):
    columnas = ["metric_name", "timestamp", "metric_value", "status", "error_code", "extra_tags"]
    partes = []
    for trozo in pd.read_csv(ruta, usecols=columnas, chunksize=200_000, dtype=str, encoding="utf-8-sig"):
        partes.append(trozo[trozo["metric_name"] == "oferta_ms"])
    df = pd.concat(partes, ignore_index=True)
    if df.empty:
        raise SystemExit("k6.csv no contiene muestras de oferta_ms")
    df["fin"] = df["timestamp"].map(parsear_ts)
    df["ms"] = df["metric_value"].astype(float)
    df["inicio"] = df["fin"] - df["ms"] / 1000.0

    def etiquetas(cadena):
        salida = {}
        for kv in str(cadena or "").split("&"):
            if "=" in kv:
                k, v = kv.split("=", 1)
                salida[k] = v
        return salida

    tags = df["extra_tags"].map(etiquetas)
    for clave in ("rid", "instancia", "ok"):
        df[clave] = tags.map(lambda d, c=clave: d.get(c, ""))
    # k6 guarda `status` y `error_code` en columnas propias del CSV, no en extra_tags.
    df["status"] = df["status"].fillna("").astype(str).str.replace(r"\.0$", "", regex=True)
    df["error"] = df["error_code"].fillna("").astype(str).str.replace(r"\.0$", "", regex=True)
    df["ok"] = df["ok"] == "1"
    return df.sort_values("inicio").reset_index(drop=True)


def cargar_timeline(ruta):
    eventos = []
    if not os.path.exists(ruta):
        return eventos
    with open(ruta, encoding="utf-8-sig", newline="") as f:
        for fila in csv.DictReader(f):
            if not fila.get("ts"):
                continue
            eventos.append({
                "ts": parsear_ts(fila["ts"]),
                "evento": fila["evento"].strip(),
                "detalle": parsear_detalle(fila.get("detalle", "")),
            })
    return eventos


def eventos_haproxy(ruta):
    """Convierte las líneas de cambio de estado de HAProxy en eventos del monitor."""
    eventos = []
    if not os.path.exists(ruta):
        return eventos
    patron = re.compile(r"^(\S+)\s+.*Server \S+/(\w+) is (DOWN|UP)")
    with open(ruta, encoding="utf-8-sig", errors="replace") as f:
        for linea in f:
            m = patron.match(linea)
            if not m:
                continue
            evento = "monitor_unhealthy" if m.group(3) == "DOWN" else "monitor_healthy"
            eventos.append({
                "ts": parsear_ts(m.group(1)),
                "evento": evento,
                "detalle": {"instancia": m.group(2), "fuente": "haproxy"},
            })
    return eventos


def cargar_ids(ruta):
    if not os.path.exists(ruta):
        return None
    with open(ruta, encoding="utf-8-sig") as f:
        datos = json.load(f)
    if isinstance(datos, dict):
        datos = datos.get("ids", [])
    return set(datos)


def deduplicar(eventos):
    """HAProxy emite cada cambio de estado por dos canales; se conserva uno por segundo."""
    salida = []
    for e in eventos:
        repetido = any(
            p["evento"] == e["evento"]
            and p["detalle"].get("instancia") == e["detalle"].get("instancia")
            and abs(p["ts"] - e["ts"]) < 1.0
            for p in salida[-4:]
        )
        if not repetido:
            salida.append(e)
    return salida


def intervalos_de_error(serie):
    segundos = sorted(serie.index[serie["fallidas"] > 0].tolist())
    intervalos = []
    for s in segundos:
        if intervalos and s - intervalos[-1][1] <= HUECO_MAX_S:
            intervalos[-1][1] = s + 1
        else:
            intervalos.append([s, s + 1])
    return intervalos


def primero(eventos, nombres, desde=None, instancia=None):
    for e in sorted(eventos, key=lambda x: x["ts"]):
        if e["evento"] not in nombres:
            continue
        if desde is not None and e["ts"] < desde:
            continue
        if instancia is not None and e["detalle"].get("instancia", "") not in ("", instancia):
            continue
        return e
    return None


def p95(valores):
    return float(valores.quantile(0.95)) if len(valores) else None


def analizar(directorio):
    df = cargar_k6(os.path.join(directorio, "k6.csv"))
    eventos = cargar_timeline(os.path.join(directorio, "timeline.csv"))
    eventos += eventos_haproxy(os.path.join(directorio, "haproxy.log"))
    eventos.sort(key=lambda e: e["ts"])
    eventos = deduplicar(eventos)
    ids_base = cargar_ids(os.path.join(directorio, "ids.json"))

    t0 = float(df["inicio"].min())
    # Un target nuevo que el observador ve por primera vez ya en estado healthy
    # (pasó el chequeo inicial entre dos sondeos) cuenta como reintegración.
    for e in eventos:
        if e["evento"] == "target_estado_inicial" and e["detalle"].get("estado") == "healthy" and e["ts"] > t0:
            e["evento"] = "target_healthy"
    df["seg"] = ((df["inicio"] - t0) // 1).astype(int)
    df["bucket"] = (df["seg"] // 10) * 10

    serie = df.groupby("seg").agg(total=("ok", "size"), ok=("ok", "sum"))
    serie["fallidas"] = serie["total"] - serie["ok"]
    serie = serie.reindex(range(0, int(serie.index.max()) + 1), fill_value=0)
    p95_buckets = df.groupby("bucket")["ms"].quantile(0.95)

    ev_falla_computo = primero(eventos, {"falla_computo", "retiro_planeado"})
    ev_falla_base = primero(eventos, {"falla_base"})
    t_fc = ev_falla_computo["ts"] - t0 if ev_falla_computo else None
    t_fb = ev_falla_base["ts"] - t0 if ev_falla_base else None
    instancia_fallada = ev_falla_computo["detalle"].get("instancia") if ev_falla_computo else None

    fallidas_df = df[~df["ok"]]
    intervalos = []
    for ini, fin in intervalos_de_error(serie):
        if t_fb is not None and t_fb - TIMEOUT_CLIENTE_S - 1 <= ini <= t_fb + ATRIBUCION_BASE_S:
            causa = "base"
        elif t_fc is not None and t_fc - TIMEOUT_CLIENTE_S - 1 <= ini <= t_fc + ATRIBUCION_COMPUTO_S:
            causa = "computo"
        else:
            causa = "esporadico"
        en_intervalo = fallidas_df[(fallidas_df["seg"] >= ini) & (fallidas_df["seg"] < fin)]
        inicio_real = float(en_intervalo["inicio"].min() - t0)
        fin_real = float(en_intervalo["fin"].max() - t0)
        intervalos.append({
            "inicio_s": round(inicio_real, 3),
            "fin_s": round(fin_real, 3),
            "duracion_s": round(fin_real - inicio_real, 3),
            "fallidas": int(len(en_intervalo)),
            "causa": causa,
        })

    # Un intervalo aislado de una o dos fallas dentro de la ventana de atribución
    # es un error esporádico si esa causa ya tiene su intervalo principal.
    for causa in ("computo", "base"):
        principales = [i for i in intervalos if i["causa"] == causa and i["fallidas"] >= 3]
        if principales:
            for i in intervalos:
                if i["causa"] == causa and i["fallidas"] < 3:
                    i["causa"] = "esporadico"

    ventana_computo = sum(i["duracion_s"] for i in intervalos if i["causa"] == "computo")
    fin_computo = max((i["fin_s"] for i in intervalos if i["causa"] == "computo"), default=None)
    esporadicos = sum(i["fallidas"] for i in intervalos if i["causa"] == "esporadico")
    ventana_base = sum(i["duracion_s"] for i in intervalos if i["causa"] == "base")
    fin_base = max((i["fin_s"] for i in intervalos if i["causa"] == "base"), default=None)
    recuperacion_base = (fin_base - t_fb) if (fin_base is not None and t_fb is not None) else (0.0 if t_fb is not None else None)

    deteccion = None
    reintegracion = None
    if ev_falla_computo:
        e_unhealthy = primero(eventos, EVENTOS_NO_SANO, desde=ev_falla_computo["ts"], instancia=instancia_fallada)
        if e_unhealthy:
            deteccion = e_unhealthy["ts"] - ev_falla_computo["ts"]
        # Reintegración: primer target healthy distinto del retirado, posterior al
        # retiro por el monitor o, en el retiro planeado y el crash, al evento mismo.
        desde_reintegracion = e_unhealthy["ts"] if e_unhealthy else ev_falla_computo["ts"]
        for e in sorted(eventos, key=lambda x: x["ts"]):
            if e["evento"] in EVENTOS_SANO and e["ts"] >= desde_reintegracion and e["detalle"].get("instancia", "") != instancia_fallada:
                reintegracion = e["ts"] - ev_falla_computo["ts"]
                break

    falsos_positivos = 0
    for e in eventos:
        if e["evento"] not in EVENTOS_NO_SANO:
            continue
        inst = e["detalle"].get("instancia", "")
        antes_de_la_falla = ev_falla_computo is None or e["ts"] < ev_falla_computo["ts"]
        if inst and instancia_fallada and inst != instancia_fallada:
            falsos_positivos += 1
        elif antes_de_la_falla:
            falsos_positivos += 1

    # Marca de "base disponible de nuevo". En AWS el evento "DB instance restarted"
    # marca la nueva primaria en servicio; "failover completed" se registra minutos
    # después, cuando la standby ya se reconstruyó, y no sirve para medir el pool.
    desde_base = ev_falla_base["ts"] if ev_falla_base else None
    e_fo_ini = primero(eventos, {"base_failover_inicio"}, desde=desde_base)
    e_base_ok = (primero(eventos, {"base_reiniciada", "base_restablecida"}, desde=desde_base)
                 or primero(eventos, {"base_failover_fin"}, desde=desde_base))
    duracion_failover = (e_base_ok["ts"] - (e_fo_ini or ev_falla_base)["ts"]) if (e_base_ok and (e_fo_ini or ev_falla_base)) else None
    retardo_reconexion_pool = (recuperacion_base - (e_base_ok["ts"] - ev_falla_base["ts"])) if (recuperacion_base is not None and e_base_ok and ev_falla_base) else None
    t_base_disponible = (e_base_ok["ts"] - t0) if e_base_ok else None

    ids_201 = set(df.loc[df["ok"], "rid"])
    ids_fallidas = set(df.loc[~df["ok"], "rid"])
    perdidas = ambiguas = None
    if ids_base is not None:
        perdidas = len(ids_201 - ids_base)
        ambiguas = len(ids_base & ids_fallidas)

    fases = {}
    limites = [("linea_base", 0, t_fc if t_fc is not None else None),
               ("una_instancia", t_fc, t_fb),
               ("failover_base", t_fb, None)]
    for nombre, a, b in limites:
        if a is None:
            continue
        sel = df["inicio"] - t0 >= a
        if b is not None:
            sel &= df["inicio"] - t0 < b
        fases[nombre] = {"solicitudes": int(sel.sum()), "p95_ms": p95(df.loc[sel, "ms"]),
                         "p95_exitosas_ms": p95(df.loc[sel & df["ok"], "ms"]),
                         "fallidas": int((~df.loc[sel, "ok"]).sum())}

    # Resumen por minuto: útil en las ejecuciones de carga en rampa, donde cada
    # escalón de carga dura dos minutos y no hay fallas inyectadas.
    df["minuto"] = (df["seg"] // 60).astype(int)
    por_minuto = []
    for minuto, grupo in df.groupby("minuto"):
        exitosas = grupo[grupo["ok"]]
        por_minuto.append({
            "minuto": int(minuto),
            "solicitudes": int(len(grupo)),
            "rps": round(len(grupo) / 60.0, 1),
            "fallidas": int((~grupo["ok"]).sum()),
            "p95_exitosas_ms": p95(exitosas["ms"]),
            "unhealthy_en_el_minuto": int(sum(1 for e in eventos if e["evento"] in EVENTOS_NO_SANO and minuto * 60 <= e["ts"] - t0 < (minuto + 1) * 60)),
        })

    total = int(len(df))
    fallidas_total = int((~df["ok"]).sum())
    disponibilidad = 1 - fallidas_total / total

    veredicto = {
        "a1_ventana_computo_ok": (ventana_computo <= UMBRAL_VENTANA_COMPUTO_S) if ev_falla_computo else None,
        "a2_sin_falsos_positivos": falsos_positivos == 0,
        "a3_sin_aceptadas_perdidas": (perdidas == 0) if perdidas is not None else None,
        "b_recuperacion_base_ok": (recuperacion_base is not None and recuperacion_base <= UMBRAL_RECUPERACION_BASE_S) if ev_falla_base else None,
    }
    veredicto["cumple"] = all(v is True for v in veredicto.values() if v is not None) and any(v is not None for v in veredicto.values())

    resumen = {
        "ejecución": os.path.basename(os.path.normpath(directorio)),
        "config": df["extra_tags"].iloc[0] if False else None,
        "solicitudes": total,
        "fallidas": fallidas_total,
        "disponibilidad": disponibilidad,
        "minutos_indisponibles_equivalentes_mes": (1 - disponibilidad) * MINUTOS_MES,
        "duracion_s": float(df["fin"].max() - t0),
        "instancia_fallada": instancia_fallada,
        "t_falla_computo_s": t_fc,
        "t_falla_base_s": t_fb,
        "ventana_error_computo_s": ventana_computo,
        "fin_ventana_computo_s": fin_computo,
        "deteccion_s": deteccion,
        "reintegracion_s": reintegracion,
        "falsos_positivos": falsos_positivos,
        "ventana_error_base_s": ventana_base,
        "recuperacion_base_s": recuperacion_base,
        "duracion_failover_base_s": duracion_failover,
        "t_base_disponible_s": t_base_disponible,
        "retardo_reconexion_pool_s": retardo_reconexion_pool,
        "errores_esporadicos": esporadicos,
        "aceptadas_perdidas": perdidas,
        "resultados_ambiguos": ambiguas,
        "por_instancia": df.groupby("instancia")["ok"].agg(["size", "sum"]).rename(columns={"size": "solicitudes", "sum": "ok"}).astype(int).to_dict("index"),
        "por_status": df["status"].value_counts().to_dict(),
        "fases": fases,
        "por_minuto": por_minuto,
        "intervalos": intervalos,
        "eventos": [{"t_s": round(e["ts"] - t0, 3), "evento": e["evento"], "detalle": e["detalle"]} for e in eventos],
        "veredicto": veredicto,
    }
    del resumen["config"]

    serie.to_csv(os.path.join(directorio, "serie_por_segundo.csv"), index_label="segundo")
    with open(os.path.join(directorio, "resumen.json"), "w", encoding="utf-8") as f:
        json.dump(resumen, f, ensure_ascii=False, indent=2, default=str)
    graficar(directorio, serie, p95_buckets, resumen)
    with open(os.path.join(directorio, "resumen.md"), "w", encoding="utf-8") as f:
        f.write(resumen_markdown(resumen))
    return resumen


def graficar(directorio, serie, p95_buckets, resumen):
    marcas = [(e["t_s"], e["evento"]) for e in resumen["eventos"]
              if e["evento"] in {"falla_computo", "retiro_planeado", "falla_base", "base_restablecida",
                                 "base_failover_fin", "reemplazo_iniciado"} | EVENTOS_NO_SANO | EVENTOS_SANO]

    fig, ax = plt.subplots(figsize=(12, 4.5))
    ax.bar(serie.index, serie["ok"], width=1.0, color="#4c9a2a", label="exitosas")
    ax.bar(serie.index, serie["fallidas"], bottom=serie["ok"], width=1.0, color="#c0392b", label="fallidas")
    for t, nombre in marcas:
        ax.axvline(t, color="#333333", linestyle="--", linewidth=0.8)
        ax.text(t + 1, ax.get_ylim()[1] * 0.95, nombre, rotation=90, va="top", fontsize=7)
    ax.set_xlabel("segundo de la ejecución")
    ax.set_ylabel("solicitudes por segundo")
    ax.set_title(f"{resumen['ejecución']}: éxitos y errores por segundo")
    ax.legend(loc="lower left")
    fig.tight_layout()
    fig.savefig(os.path.join(directorio, "grafica_errores.png"), dpi=130)
    plt.close(fig)

    fig, ax = plt.subplots(figsize=(12, 3.5))
    ax.plot(p95_buckets.index, p95_buckets.values, marker="o", markersize=3, color="#1f4e79")
    for t, nombre in marcas:
        ax.axvline(t, color="#333333", linestyle="--", linewidth=0.8)
    ax.set_xlabel("segundo de la ejecución (ventanas de 10 s)")
    ax.set_ylabel("p95 (ms)")
    ax.set_title(f"{resumen['ejecución']}: p95 por ventana de 10 s")
    fig.tight_layout()
    fig.savefig(os.path.join(directorio, "grafica_p95.png"), dpi=130)
    plt.close(fig)


def fmt(v, dec=1):
    if v is None:
        return "n/d"
    if isinstance(v, bool):
        return "sí" if v else "no"
    if isinstance(v, float):
        return f"{v:.{dec}f}"
    return str(v)


def resumen_markdown(r):
    v = r["veredicto"]
    filas = [
        ("Solicitudes", r["solicitudes"]),
        ("Fallidas", r["fallidas"]),
        ("Disponibilidad de la ejecución", f"{r['disponibilidad'] * 100:.3f} %"),
        ("Minutos/mes equivalentes", fmt(r["minutos_indisponibles_equivalentes_mes"])),
        ("Ventana de error por falla de cómputo (s)", fmt(r["ventana_error_computo_s"])),
        ("Detección por el monitor (s)", fmt(r["deteccion_s"])),
        ("Reintegración (s)", fmt(r["reintegracion_s"])),
        ("Falsos positivos del monitor", r["falsos_positivos"]),
        ("Ventana de error por failover de base (s)", fmt(r["ventana_error_base_s"])),
        ("Recuperación extremo a extremo tras failover (s)", fmt(r["recuperacion_base_s"])),
        ("Duración del failover de la base (s)", fmt(r["duracion_failover_base_s"])),
        ("Retardo de reconexión del pool (s)", fmt(r["retardo_reconexion_pool_s"])),
        ("Errores esporádicos fuera de las ventanas de falla", r.get("errores_esporadicos", 0)),
        ("Aceptadas perdidas", fmt(r["aceptadas_perdidas"])),
        ("Resultados ambiguos", fmt(r["resultados_ambiguos"])),
    ]
    for nombre, f in r["fases"].items():
        filas.append((f"p95 fase {nombre}, todas (ms)", fmt(f["p95_ms"])))
        filas.append((f"p95 fase {nombre}, solo exitosas (ms)", fmt(f.get("p95_exitosas_ms"))))
    lineas = [f"## Ejecución `{r['ejecución']}`", "", "| Métrica | Valor |", "|---|---|"]
    lineas += [f"| {n} | {fmt(val)} |" for n, val in filas]
    lineas += ["", "| Criterio | Cumple |", "|---|---|",
               f"| (a1) ventana de cómputo ≤ {UMBRAL_VENTANA_COMPUTO_S:.0f} s | {fmt(v['a1_ventana_computo_ok'])} |",
               f"| (a2) sin falsos positivos del monitor | {fmt(v['a2_sin_falsos_positivos'])} |",
               f"| (a3) sin aceptadas perdidas | {fmt(v['a3_sin_aceptadas_perdidas'])} |",
               f"| (b) recuperación tras failover ≤ {UMBRAL_RECUPERACION_BASE_S:.0f} s | {fmt(v['b_recuperacion_base_ok'])} |",
               f"| **Configuración cumple** | **{fmt(v['cumple'])}** |", ""]
    if r["intervalos"]:
        lineas += ["| Intervalo de error | Inicio (s) | Fin (s) | Duración (s) | Fallidas | Causa |", "|---|---|---|---|---|---|"]
        lineas += [f"| {i + 1} | {it['inicio_s']} | {it['fin_s']} | {it['duracion_s']} | {it['fallidas']} | {it['causa']} |"
                   for i, it in enumerate(r["intervalos"])]
        lineas.append("")
    if r.get("por_minuto"):
        lineas += ["| Minuto | Solicitudes | req/s | Fallidas | p95 exitosas (ms) | Transiciones a unhealthy |", "|---|---|---|---|---|---|"]
        lineas += [f"| {m['minuto']} | {m['solicitudes']} | {m['rps']} | {m['fallidas']} | {fmt(m['p95_exitosas_ms'])} | {m['unhealthy_en_el_minuto']} |" for m in r["por_minuto"]]
        lineas.append("")
    lineas += ["| Evento | t (s) | Detalle |", "|---|---|---|"]
    lineas += [f"| {e['evento']} | {e['t_s']} | {';'.join(f'{k}={v}' for k, v in e['detalle'].items())} |" for e in r["eventos"]]
    return "\n".join(lineas) + "\n"


def consolidar(directorios):
    filas = []
    for d in directorios:
        ruta = os.path.join(d, "resumen.json")
        if not os.path.exists(ruta):
            continue
        with open(ruta, encoding="utf-8") as f:
            r = json.load(f)
        v = r["veredicto"]
        filas.append("| {c} | {vc} | {det} | {rei} | {fa} | {rb} | {bp} | {pe} | {p1} | {p2} | {p3} | {d:.3f} % | {ok} |".format(
            c=r["ejecución"], vc=fmt(r["ventana_error_computo_s"]), det=fmt(r["deteccion_s"]), rei=fmt(r["reintegracion_s"]),
            fa=r["falsos_positivos"], rb=fmt(r["recuperacion_base_s"]), bp=fmt(r["retardo_reconexion_pool_s"]), pe=fmt(r["aceptadas_perdidas"]),
            p1=fmt(r["fases"].get("linea_base", {}).get("p95_exitosas_ms")), p2=fmt(r["fases"].get("una_instancia", {}).get("p95_exitosas_ms")),
            p3=fmt(r["fases"].get("failover_base", {}).get("p95_exitosas_ms")), d=r["disponibilidad"] * 100, ok=fmt(v["cumple"])))
    cabecera = ("| Ejecución | Ventana cómputo (s) | Detección (s) | Reintegración (s) | Falsos positivos del monitor | Recuperación base (s) | "
                "Retardo pool (s) | Aceptadas perdidas | p95 exitosas base (ms) | p95 exitosas 1 instancia (ms) | p95 exitosas failover (ms) | Disponibilidad | Cumple |")
    separador = "|" + "---|" * 13
    return "\n".join([cabecera, separador] + filas) + "\n"


def main():
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description="Análisis de ejecuciones del experimento EXP-D03")
    parser.add_argument("directorios", nargs="+")
    parser.add_argument("--consolidar", action="store_true", help="imprime la tabla consolidada de varias ejecuciones")
    args = parser.parse_args()
    if args.consolidar:
        sys.stdout.write(consolidar(args.directorios))
        return
    for d in args.directorios:
        r = analizar(d)
        sys.stdout.write(resumen_markdown(r))


if __name__ == "__main__":
    main()
