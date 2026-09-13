# Guion del vídeo (≤ 2 minutos) — EC-D02

> Nota: grabar **después** de correr `go test ./...`, `./scripts/run-escenarios.sh todos`
> y `./scripts/repetir-concurrencia.sh` en local con éxito. AWS solo se usa para esta
> grabación (`./scripts/deploy-ec2.sh up`), y se termina la instancia
> (`./scripts/deploy-ec2.sh down`) apenas se corte.

1. **Preámbulo (5 s):** "Este es el experimento EC-D02: cobro idempotente con reintentos,
   para garantizar exactamente un cobro exitoso por aceptación cuando la pasarela de pago
   falla o responde de forma ambigua."
2. **Plataforma (10 s):** mostrar la terminal con `./scripts/deploy-ec2.sh up` ya corrido,
   `curl http://<ip>:8280/health` respondiendo desde la instancia EC2, y la consola de AWS
   (o `aws ec2 describe-instances`) mostrando la instancia corriendo — evidencia de Golang + AWS.
3. **Código (20 s):** un vistazo rápido a `internal/store/postgres.go` (`ReclamarPendiente`,
   el claim atómico de ADR-01) y `db/schema.sql` (el `UNIQUE` sobre `idempotency_key`) —
   los dos elementos centrales de la corrección.
4. **El hallazgo (40 s):**
   - Mencionar en una frase: "el diseño ingenuo, sin este claim, sí produjo un cobro real
     duplicado" (mostrar brevemente `resultados/raw/diseno-ingenuo-evidencia-duplicado-real.txt`).
   - Correr `./scripts/toxiproxy.sh ambiguo`, disparar una aceptación, y mostrar
     `GET /debug/cobros` del stub: `requests recibidos > 1` pero `cargos_reales = 1`.
   - Correr (o mostrar ya corrido) `./scripts/repetir-concurrencia.sh` y el resumen:
     10/10 repeticiones sin duplicado real, `requests_al_gateway = 1` en cada una.
5. **Resultado y decisión (30 s):** mostrar la tabla de resultados de la wiki y decir en una
   frase que la hipótesis original se **refutó** para el diseño ingenuo y se **confirmó** tras
   la corrección (ADR-01), señalando la dependencia documentada de que la pasarela real
   soporte idempotency keys.
6. **Cierre (5 s):** enlace a la rama `Experimento2_cobroIdempotenteReintentos` y a la hoja de wiki.
