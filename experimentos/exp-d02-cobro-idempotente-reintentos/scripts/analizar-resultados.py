#!/usr/bin/env python3
"""Consolida resultados/raw/*.json de los 4 escenarios cualitativos
(normal, caida_total, ambiguo, ventana_vencida) y de las repeticiones del
escenario de concurrencia, en tablas Markdown para la hoja de wiki.
Uso: python3 scripts/analizar-resultados.py
"""
import json
from pathlib import Path

RAW = Path(__file__).parent.parent / "resultados" / "raw"

ESCENARIOS = [
    ("normal.json", "Normal (baseline)"),
    ("caida_total.json", "Caída total"),
    ("ambiguo_bd.json", "Respuesta ambigua"),
    ("ventana_vencida.json", "Ventana de reintentos vencida"),
]


def cargar(nombre):
    f = RAW / nombre
    if not f.exists():
        return None
    return json.loads(f.read_text())


def main():
    print("## Escenarios cualitativos (estado final en la base de datos propia)\n")
    print("| Escenario | Estado final | Intentos | Último error |")
    print("|---|---|---|---|")
    for archivo, etiqueta in ESCENARIOS:
        d = cargar(archivo)
        if d is None:
            print(f"| {etiqueta} | (sin datos) | | |")
            continue
        print(f"| {etiqueta} | {d.get('Estado', d.get('estado', '?'))} | {d.get('Intentos', d.get('intentos', '?'))} | {d.get('UltimoError', d.get('ultimo_error', '') or '—')} |")

    print()
    print("## Duplicidad real del lado del proveedor (escenario ambiguo)\n")
    debug = cargar("ambiguo_pasarela.json")
    if debug is None:
        print("(sin datos: correr `./scripts/run-escenarios.sh ambiguo` primero)")
    else:
        soporta = debug.get("soporta_idempotencia", True)
        print(f"`soporta_idempotencia` de la pasarela en esta corrida: **{soporta}**\n")
        print("| idempotency_key | Requests recibidos (red) | Cargos reales procesados | ¿Duplicado real (cargo)? |")
        print("|---|---|---|---|")
        conteos = debug.get("conteos", {})
        cargos = debug.get("cargos_reales", {})
        for key in conteos:
            c = cargos.get(key, 0)
            print(f"| {key} | {conteos[key]} | {c} | {'SÍ' if c > 1 else 'no'} |")
        print()
        print("Nota: que `Requests recibidos` sea > 1 es NORMAL y esperado tras una respuesta ambigua "
              "(el worker reintentó con la misma idempotency_key); lo que importaría como hallazgo es "
              "que `Cargos reales procesados` sea > 1, porque eso sí sería una duplicidad real de dinero.")

    print()
    print("## Repeticiones del escenario de concurrencia dirigida\n")
    concurrencia = cargar("concurrencia_repeticiones.json")
    if concurrencia is None:
        print("(sin datos: correr `./scripts/repetir-concurrencia.sh` primero)")
        return

    print("| Repetición | Filas confirmadas en BD | Requests al gateway (red) | Cargos reales procesados | ¿Duplicado real? |")
    print("|---|---|---|---|---|")
    duplicados = 0
    for i, r in enumerate(concurrencia, start=1):
        dup = r.get("duplicado_real", False)
        duplicados += 1 if dup else 0
        print(f"| {i} | {r.get('filas_confirmadas_bd')} | {r.get('requests_al_gateway')} | {r.get('cargos_reales_gateway')} | {'SÍ' if dup else 'no'} |")

    print()
    print(f"**Resumen:** {duplicados} de {len(concurrencia)} repeticiones mostraron un cobro real duplicado del lado de la pasarela "
          f"(cargos_reales > 1), pese a que la fila en `intento_cobro` siempre queda en 1 (constraint UNIQUE). Con el claim atómico "
          f"de ADR-01 (ReclamarPendiente), lo esperado es que `requests_al_gateway` también quede en 1: ningún llamador concurrente "
          f"que pierda el claim debería siquiera llegar a golpear la pasarela.")


if __name__ == "__main__":
    main()
