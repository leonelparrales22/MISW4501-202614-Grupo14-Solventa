#!/usr/bin/env python3
"""Lista los recursos del experimento que siguen vivos en la cuenta y el costo
del mes en curso. Se ejecuta al cerrar cada sesión, después de `terraform destroy`,
para garantizar que nada quedó en ejecución consumiendo créditos.

Uso: python verificar_limpieza.py [--prefijo expd03] [--region us-east-1]
"""

import argparse
import datetime as dt
import sys

import boto3
from botocore.exceptions import ClientError


def recursos_vivos(sesion, prefijo):
    hallazgos = []

    elbv2 = sesion.client("elbv2")
    for lb in elbv2.get_paginator("describe_load_balancers").paginate().search("LoadBalancers[]"):
        if prefijo in lb["LoadBalancerName"]:
            hallazgos.append(("Balanceador", lb["LoadBalancerName"]))

    ecs = sesion.client("ecs")
    for arn in ecs.get_paginator("list_clusters").paginate().search("clusterArns[]"):
        if prefijo in arn:
            tareas = list(ecs.get_paginator("list_tasks").paginate(cluster=arn).search("taskArns[]"))
            hallazgos.append(("Clúster ECS", f"{arn.rsplit('/', 1)[-1]} ({len(tareas)} tareas)"))

    rds = sesion.client("rds")
    for db in rds.get_paginator("describe_db_instances").paginate().search("DBInstances[]"):
        if prefijo in db["DBInstanceIdentifier"]:
            hallazgos.append(("RDS", f"{db['DBInstanceIdentifier']} ({db['DBInstanceStatus']})"))

    ec2 = sesion.client("ec2")
    for vpc in ec2.describe_vpcs()["Vpcs"]:
        nombre = next((t["Value"] for t in vpc.get("Tags", []) if t["Key"] == "Name"), "")
        if prefijo in nombre:
            hallazgos.append(("VPC", f"{vpc['VpcId']} {nombre}"))
    for nat in ec2.describe_nat_gateways()["NatGateways"]:
        if nat["State"] not in ("deleted", "deleting"):
            hallazgos.append(("NAT Gateway", f"{nat['NatGatewayId']} ({nat['State']})"))
    for direccion in ec2.describe_addresses()["Addresses"]:
        hallazgos.append(("IP elástica", direccion.get("PublicIp", "")))

    apigw = sesion.client("apigateway")
    for api in apigw.get_paginator("get_rest_apis").paginate().search("items[]"):
        if prefijo in api["name"]:
            hallazgos.append(("API Gateway", api["name"]))

    apigwv2 = sesion.client("apigatewayv2")
    for enlace in apigwv2.get_vpc_links().get("Items", []):
        if prefijo in enlace["Name"]:
            hallazgos.append(("VPC Link", enlace["Name"]))

    secretos = sesion.client("secretsmanager")
    for s in secretos.get_paginator("list_secrets").paginate().search("SecretList[]"):
        if prefijo in s["Name"]:
            hallazgos.append(("Secreto", s["Name"]))

    ecr = sesion.client("ecr")
    for repo in ecr.get_paginator("describe_repositories").paginate().search("repositories[]"):
        if prefijo in repo["repositoryName"]:
            hallazgos.append(("Repositorio ECR", repo["repositoryName"]))

    return hallazgos


def costo_mes(sesion):
    ce = sesion.client("ce", region_name="us-east-1")
    hoy = dt.date.today()
    inicio = hoy.replace(day=1)
    fin = hoy + dt.timedelta(days=1)
    respuesta = ce.get_cost_and_usage(
        TimePeriod={"Start": inicio.isoformat(), "End": fin.isoformat()},
        Granularity="MONTHLY",
        Metrics=["UnblendedCost"],
        GroupBy=[{"Type": "DIMENSION", "Key": "SERVICE"}],
    )
    filas = []
    for grupo in respuesta["ResultsByTime"][0].get("Groups", []):
        monto = float(grupo["Metrics"]["UnblendedCost"]["Amount"])
        if monto > 0.001:
            filas.append((grupo["Keys"][0], monto))
    return sorted(filas, key=lambda f: -f[1])


def main():
    parser = argparse.ArgumentParser(description="Recursos vivos y costo del experimento EXP-D03")
    parser.add_argument("--prefijo", default="expd03")
    parser.add_argument("--region", default="us-east-1")
    args = parser.parse_args()
    sesion = boto3.Session(region_name=args.region)
    try:
        vivos = recursos_vivos(sesion, args.prefijo)
        if vivos:
            print("Recursos que siguen vivos:")
            for tipo, nombre in vivos:
                print(f"  - {tipo}: {nombre}")
        else:
            print("No queda ningún recurso del experimento en ejecución.")
        try:
            filas = costo_mes(sesion)
            total = sum(m for _, m in filas)
            print(f"\nCosto bruto del mes en curso (antes de créditos): US$ {total:.2f}")
            for servicio, monto in filas:
                print(f"  - {servicio}: US$ {monto:.2f}")
        except ClientError as error:
            print(f"\nNo se pudo consultar Cost Explorer: {error.response['Error']['Code']}")
        return 1 if vivos else 0
    except ClientError as error:
        print(f"Error de AWS: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
