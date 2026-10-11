# Runbooks

Procedimientos operativos. **Estado:** pendientes; se agregan a medida que existan los ambientes.

| Runbook | Estado |
|---|---|
| Levantar el ambiente local (PostgreSQL, Redis, LocalStack, Toxiproxy) | Pendiente (tarea T2 del plan de sprints) |
| Ejecutar pruebas de carga y resiliencia | Pendiente |
| Failover y rollback en AWS | Pendiente |
| Rotación de credenciales de socios | Pendiente |
| Aprovisionar usuarios internos y del portal del socio (grupo de Cognito y `custom:partner_id`) | Pendiente. En el Sprint 1 el alta es administrativa con Terraform (módulo `infra/terraform/modules/cognito`, HA-S01); el alta desde el sistema llega con HU-W3.1-01. Los asegurados se aprovisionan tras la verificación KYC (HU-M1.1-01) |

Plan: [Plan de trabajo por sprints](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Plan-de-trabajo-por-sprints).
