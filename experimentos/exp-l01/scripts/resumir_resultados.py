#!/usr/bin/env python3
"""Consolida los resultados de k6 de EXP-L01 en una tabla Markdown.

Lee resultados/<nombre>.json (--summary-export de k6) y, si existe,
resultados/<nombre>.log (salida de docker compose) para extraer las líneas
ESTADO_INICIAL / ESTADO_FINAL con la evidencia de la hipótesis (b).

Uso:
    python scripts/resumir_resultados.py                # tabla por ejecución
    python scripts/resumir_resultados.py --mediana      # además, mediana por combinación
    python scripts/resumir_resultados.py --csv salida.csv
"""

import argparse
import csv
import json
import re
import statistics
import sys
from pathlib import Path

# Nombre: FECHA[_ambiente]_N<n>_<perfil>_c<hit>_r<rep>. Sin ambiente = local.
PATRON_NOMBRE = re.compile(
    r"^(?P<fecha>\d{4}-\d{2}-\d{2})(?:_(?P<ambiente>[a-z]+))?_N(?P<n>\d+)_(?P<perfil>[a-z0-9]+)_c(?P<hit>[0-9.]+)_r(?P<rep>\d+)$"
)
PATRON_ESTADO = re.compile(r'(ESTADO_(?:INICIAL|FINAL))\s+(\{.*?\})"?\s*(?:source=console)?')


def stat(metricas, nombre, clave, defecto=None):
    m = metricas.get(nombre)
    if not m:
        return defecto
    return m.get(clave, defecto)


def cumple_umbrales(metricas):
    """True si todos los thresholds pasaron.

    En el --summary-export de k6 cada threshold mapea a un booleano que
    significa "falló" (LastFailed): false = cumplió, true = no cumplió.
    """
    for nombre in ("http_req_duration", "http_req_failed"):
        for _, fallo in (metricas.get(nombre, {}).get("thresholds") or {}).items():
            if fallo:
                return False
    return True


def leer_estado(ruta_log):
    """Devuelve (inicial, final) como dicts, o ({}, {}) si no se encuentran."""
    inicial, final = {}, {}
    if not ruta_log.exists():
        return inicial, final
    texto = ruta_log.read_text(encoding="utf-8", errors="replace")
    # k6 escapa las comillas dentro de msg="..."; se desescapan antes de parsear.
    for etiqueta, cuerpo in PATRON_ESTADO.findall(texto):
        try:
            d = json.loads(cuerpo.replace('\\"', '"'))
        except json.JSONDecodeError:
            continue
        if etiqueta == "ESTADO_INICIAL":
            inicial = d
        else:
            final = d
    return inicial, final


def fila(ruta_json):
    nombre = ruta_json.stem
    m = PATRON_NOMBRE.match(nombre)
    if not m:
        return None
    datos = json.loads(ruta_json.read_text(encoding="utf-8"))
    met = datos.get("metrics", {})
    ini, fin = leer_estado(ruta_json.with_suffix(".log"))

    return {
        "corrida": nombre,
        "ambiente": m["ambiente"] or "local",
        "n": int(m["n"]),
        "perfil": m["perfil"],
        "cache_hit": float(m["hit"]),
        "rep": int(m["rep"]),
        "reqs": stat(met, "http_reqs", "count"),
        "rps": stat(met, "http_reqs", "rate"),
        "p50": stat(met, "http_req_duration", "med"),
        "p95": stat(met, "http_req_duration", "p(95)"),
        "p99": stat(met, "http_req_duration", "p(99)"),
        "max": stat(met, "http_req_duration", "max"),
        "fanout_p95": stat(met, "tramo_fanout", "p(95)"),
        "cache_p95": stat(met, "tramo_cache", "p(95)"),
        "agg_p95": stat(met, "tramo_agg", "p(95)"),
        "rating_p95": stat(met, "tramo_rating", "p(95)"),
        "pct_parciales": stat(met, "perfiles_parciales", "value"),
        "pct_desde_cache": stat(met, "desde_cache", "value"),
        "pct_errores": stat(met, "http_req_failed", "value"),
        "cumple": cumple_umbrales(met),
        "conex_ini": ini.get("conexiones_abiertas"),
        "conex_fin": fin.get("conexiones_abiertas"),
        "gorout_ini": ini.get("goroutines"),
        "gorout_fin": fin.get("goroutines"),
        "vuelo_fin": fin.get("consultas_en_vuelo"),
        "cancelaciones": fin.get("cancelaciones_efectivas"),
        "perfiles_calc": fin.get("perfiles_calculados"),
    }


def f(x, dec=1, sufijo=""):
    if x is None:
        return "—"
    if isinstance(x, bool):
        return "✓" if x else "✗"
    if isinstance(x, float):
        return f"{x:.{dec}f}{sufijo}"
    return f"{x}{sufijo}"


def pct(x):
    return "—" if x is None else f"{100 * x:.1f} %"


COLUMNAS = [
    ("Ejecución", lambda r: r["corrida"]),
    ("Amb.", lambda r: r["ambiente"]),
    ("N", lambda r: r["n"]),
    ("Perfil", lambda r: r["perfil"]),
    ("Caché", lambda r: pct(r["cache_hit"])),
    ("req/s", lambda r: f(r["rps"], 1)),
    ("p50", lambda r: f(r["p50"])),
    ("p95", lambda r: f(r["p95"])),
    ("p99", lambda r: f(r["p99"])),
    ("max", lambda r: f(r["max"], 0)),
    ("Consultas en paralelo p95", lambda r: f(r["fanout_p95"])),
    ("Caché p95", lambda r: f(r["cache_p95"], 2)),
    ("% parciales", lambda r: pct(r["pct_parciales"])),
    ("% caché", lambda r: pct(r["pct_desde_cache"])),
    ("Errores", lambda r: pct(r["pct_errores"])),
    ("Cancel.", lambda r: f(r["cancelaciones"])),
    ("Conex. ini→fin", lambda r: f"{f(r['conex_ini'])}→{f(r['conex_fin'])}"),
    ("Gorout. ini→fin", lambda r: f"{f(r['gorout_ini'])}→{f(r['gorout_fin'])}"),
    ("Consultas en proceso (fin)", lambda r: f(r["vuelo_fin"])),
    ("Cumple", lambda r: f(r["cumple"])),
]


def tabla(filas, columnas=COLUMNAS):
    out = ["| " + " | ".join(c for c, _ in columnas) + " |",
           "|" + "---|" * len(columnas)]
    for r in filas:
        out.append("| " + " | ".join(str(fn(r)) for _, fn in columnas) + " |")
    return "\n".join(out)


def medianas(filas):
    """Mediana de p50/p95/p99/parciales por combinación (N, perfil, caché)."""
    grupos = {}
    for r in filas:
        grupos.setdefault((r["ambiente"], r["n"], r["perfil"], r["cache_hit"]), []).append(r)
    salida = []
    for (amb, n, perfil, hit), rs in sorted(grupos.items()):
        def med(clave):
            vals = [x[clave] for x in rs if x[clave] is not None]
            return statistics.median(vals) if vals else None
        salida.append({
            "ambiente": amb, "n": n, "perfil": perfil, "cache_hit": hit, "reps": len(rs),
            "p50": med("p50"), "p95": med("p95"), "p99": med("p99"),
            "fanout_p95": med("fanout_p95"), "pct_parciales": med("pct_parciales"),
            "cumple": all(x["cumple"] for x in rs),
        })
    return salida


COLUMNAS_MEDIANA = [
    ("Amb.", lambda r: r["ambiente"]),
    ("N", lambda r: r["n"]),
    ("Perfil", lambda r: r["perfil"]),
    ("Caché", lambda r: pct(r["cache_hit"])),
    ("Reps", lambda r: r["reps"]),
    ("p50", lambda r: f(r["p50"])),
    ("p95", lambda r: f(r["p95"])),
    ("p99", lambda r: f(r["p99"])),
    ("Consultas en paralelo p95", lambda r: f(r["fanout_p95"])),
    ("% parciales", lambda r: pct(r["pct_parciales"])),
    ("Cumple (todas)", lambda r: f(r["cumple"])),
]


def main():
    # La consola de Windows usa cp1252 por defecto y no puede imprimir → ni ✓.
    for flujo in (sys.stdout, sys.stderr):
        if hasattr(flujo, "reconfigure"):
            flujo.reconfigure(encoding="utf-8", errors="replace")

    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("carpeta", nargs="?", default="resultados")
    ap.add_argument("--mediana", action="store_true", help="agrega tabla de medianas por combinación")
    ap.add_argument("--csv", help="además escribe las filas a este CSV")
    args = ap.parse_args()

    carpeta = Path(args.carpeta)
    filas = [r for p in sorted(carpeta.glob("*_N*_r*.json")) if (r := fila(p))]
    if not filas:
        print(f"Sin resultados en {carpeta}/ (se esperan *_N<n>_<perfil>_c<hit>_r<rep>.json)", file=sys.stderr)
        return 1

    filas.sort(key=lambda r: (r["ambiente"], r["perfil"], r["cache_hit"], r["n"], r["rep"]))
    print(f"### Resultados por ejecución ({len(filas)})\n")
    print(tabla(filas))

    if args.mediana:
        print(f"\n### Mediana por combinación\n")
        print(tabla(medianas(filas), COLUMNAS_MEDIANA))

    if args.csv:
        with open(args.csv, "w", newline="", encoding="utf-8") as fh:
            w = csv.DictWriter(fh, fieldnames=list(filas[0].keys()))
            w.writeheader()
            w.writerows(filas)
        print(f"\nCSV: {args.csv}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
