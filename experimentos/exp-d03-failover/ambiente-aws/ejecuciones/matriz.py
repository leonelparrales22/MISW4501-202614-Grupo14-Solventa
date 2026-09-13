#!/usr/bin/env python3
"""Ejecuta la matriz de ejecuciones del experimento EXP-D03 en AWS.

Para cada configuración aplica sus variables con Terraform (cambia el health
check del target group en sitio y, si cambia el pool o el health, hace un
rolling update del servicio), espera a que el staging esté estable y lanza
ejecucion.py. Al final puede destruir el staging.

Ejemplos:
  python matriz.py                                   # las 6 configuraciones principales
  python matriz.py --configs M2-P1 --repeticiones 3   # repeticiones de la ganadora
  python matriz.py --configs C3-profundo --run-id C3-health-profundo
  python matriz.py --configs M2-P1 --tipo-falla stop-task --run-id C1-retiro-planeado
  python matriz.py --destruir-al-final
"""

import argparse
import os
import subprocess
import sys

from staging import DIR_EJECUCIONES, DIR_TERRAFORM

PRINCIPALES = ["M1-P1", "M1-P2", "M2-P1", "M2-P2", "M3-P1", "M3-P2"]


def terraform(argumentos):
    print(f"$ terraform {' '.join(argumentos)}", flush=True)
    subprocess.run(["terraform", *argumentos], cwd=DIR_TERRAFORM, check=True)


def aplicar(config, imagen_tag):
    variables = ["-var-file", os.path.join("configs", f"{config}.tfvars")]
    if imagen_tag:
        variables += ["-var", f"imagen_tag={imagen_tag}"]
    terraform(["apply", "-auto-approve", "-input=false", *variables])


def correr(config, run_id, extra):
    comando = [sys.executable, os.path.join(DIR_EJECUCIONES, "ejecucion.py"), "--config", config, "--run-id", run_id, *extra]
    print(f"$ {' '.join(comando)}", flush=True)
    subprocess.run(comando, check=True)


def main():
    parser = argparse.ArgumentParser(description="Matriz de ejecuciones del experimento EXP-D03")
    parser.add_argument("--configs", default=",".join(PRINCIPALES), help="lista separada por comas")
    parser.add_argument("--repeticiones", type=int, default=1)
    parser.add_argument("--run-id", default=None, help="prefijo del identificador de ejecución (por defecto la configuración)")
    parser.add_argument("--imagen-tag", default=None)
    parser.add_argument("--tipo-falla", choices=["hang", "crash", "stop-task"], default="hang")
    parser.add_argument("--sin-aplicar", action="store_true", help="no ejecuta terraform apply antes de cada configuración")
    parser.add_argument("--destruir-al-final", action="store_true")
    parser.add_argument("--k6", default="k6")
    parser.add_argument("--extra", default="", help="argumentos adicionales para ejecucion.py, entre comillas")
    args = parser.parse_args()

    configs = [c.strip() for c in args.configs.split(",") if c.strip()]
    try:
        for config in configs:
            if not args.sin_aplicar:
                aplicar(config, args.imagen_tag)
            for repeticion in range(1, args.repeticiones + 1):
                base = args.run_id or config
                run_id = base if args.repeticiones == 1 else f"{base}-r{repeticion}"
                correr(config, run_id, ["--tipo-falla", args.tipo_falla, "--k6", args.k6, *args.extra.split()])
        if args.destruir_al_final:
            terraform(["destroy", "-auto-approve", "-input=false"])
        return 0
    except subprocess.CalledProcessError as error:
        print(f"Error: el paso terminó con código {error.returncode}", file=sys.stderr)
        return error.returncode


if __name__ == "__main__":
    sys.exit(main())
