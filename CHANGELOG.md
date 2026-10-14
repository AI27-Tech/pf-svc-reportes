# CHANGELOG

## 2026:10:14 (hora local) — alinear la promoción al estándar de los backends

**Resumen:** reportes adopta el mismo par de workflows de promoción que ya usan los otros repos de backend (`pf-svc-catalogos`, `pf-svc-operacion`, `pf-svc-identidad`, `pf-svc-bff`, `pf-svc-core`, `pf-pkg-platform-go`): `promotion-pr-dq.yml` y `promotion-pr-qm.yml`, ambos con **merge manual**. Reemplaza la propuesta previa de auto-merge en `q→m` (PR #13, `automerge-promotion.yml`), que divergía del estándar: el último salto a producción se decide y se clickea a mano, como en el resto.

**Cambios:**
- `.github/workflows/promotion-pr-dq.yml` (nuevo): mantiene abierto y al día el PR `pf-rep-d → pf-rep-q`. No fusiona; la promoción `d→q` es manual.
- `.github/workflows/promotion-pr-qm.yml` (nuevo): mantiene abierto y al día el PR `pf-rep-q → pf-rep-m`. No fusiona; la promoción a producción es manual.
- Ambos archivos son byte-idénticos a los de los demás backends (derivan la rama destino del sufijo, sin prefijos hardcodeados). `ci.yml` (build/vet/test/arch-test) no cambia: ya estaba alineado.

**Nota:** este cambio supone descartar el PR #13 (auto-merge `q→m`) sin fusionarlo; se cierra en favor del par estándar.

**Verificado:** los dos workflows `diff`-idénticos a los de `pf-svc-catalogos`. YAML válido (mismo contenido ya en producción en 6 repos). No toca código Go.

**Archivos modificados:**
- `.github/workflows/promotion-pr-dq.yml`, `.github/workflows/promotion-pr-qm.yml`
- `CHANGELOG.md`

## 2026:10:14 (hora local)

**Resumen:** E6-T1, lado escritura de la proyección de reportes (plan `.scratch/reportes-proyeccion-e6-t1`). reportes pasa a ser el primer consumidor durable real del sistema: se suscribe al bus, consume los 4 eventos `viaje.*` que `operacion` publica hoy y puebla `hecho_viaje`/`hecho_transicion`, con idempotencia por `inbox_evento`. Habilita las pruebas 4 y 8 del gate G2 (quedan para un slice posterior). Pasos 1 y 2 del plan; el paso 3 (core inyecta el Subscriber) va en PR aparte en `pf-svc-core`.

**Cambios:**
- `internal/proyeccionpg/`: adaptador Postgres de los puertos `Proyeccion`/`Inbox`/`DLQ` del consumidor, contra el pool de `svc_reportes`. `Aplicar` abre una tx, proyecta por tipo y registra el `evento_id` en `inbox_evento` en la MISMA tx (atomicidad vía `inbox.Registrar`, idempotencia por la PK); `Marcar` queda no-op. Decodificador `bus.Sobre -> consumidores.Evento` (payload JSON crudo). Mapeo por tipo: `viaje.creado` INSERT (`tenant_id = cliente_id`, DEC-495/DEC-049), `viaje.estado_cambiado` UPDATE estado + INSERT transición (tenant leído de `hecho_viaje`), `viaje.asignado`/`.reasignado` UPDATE `unidades_ids`. El borde "transición/asignación sin creado previo" devuelve `ErrTenantDesconocido` para que el bus lo cuarentene en vez de ponchar tenant (ADR 29). La DLQ del consumidor registra metadatos sanitizados en log (ADR 29 §4); la cuarentena de transporte la lleva el bus.
- `migrations/reportes/0003_hecho_viaje_unidades.sql`: agrega `hecho_viaje.unidades_ids UUID[]` para proyectar la escolta tal como llega (ADR 31 / DEC-044; los campos viejos `unidad_id`/`custodios_ids` quedaron `reserved` en el proto). Sin grant nuevo: una columna hereda los privilegios de su tabla. `unidad_id`/`custodios_ids` quedan como columnas muertas (deuda nombrada, su retiro es un refactor aparte).
- `reportes.go`: `Deps.Subscriber bus.Subscriber`; `New()` construye el consumidor durable y lo suscribe a la superficie amplia con los 4 tipos `viaje.*` con productor cuando hay Subscriber; `Workers()` lo devuelve (antes devolvía nil). El handler acusa (nil) un `ErrEventoVenenoso` ya cuarentenado por el consumidor y propaga el resto para que el bus reintente/cuarentene.
- `internal/dbtest/dbtest.go`: `Serializar` (pg_advisory_lock) para que las pruebas de integración de distintos paquetes no se pisen la base. `grants_integration_test.go` y la prueba nueva lo toman.

**Fuera de alcance (nombrado):** `hecho_condicion` y `cerrado_en` (sin productor aún: `viaje.cerrado`/`condicion.*` son E1-T5..T9); lectura HTTP (R3); paso 3 (core) y paso 4 (gate G2).

**Verificado:** gofmt limpio, `go vet ./...`, `go build ./...`, `go build ./cmd/api` (I5), arch-test@v0.1.1 OK. `go test ./...` con `REPORTES_TEST_DB` apuntando a un Postgres local: la prueba de integración del adaptador (4 eventos pueblan su fila, duplicado no duplica, dos tenants no se mezclan, borde sin creado va a `ErrTenantDesconocido`), `TestGrants` con la 0003, y la de `Workers()`. Suite estable en paralelo tras `Serializar` (3 corridas con `-count=1`). Sin DB, las de integración se saltan.

**Archivos modificados:**
- `reportes.go`
- `migrations/reportes/0003_hecho_viaje_unidades.sql`
- `internal/proyeccionpg/{decodificador.go,adaptador.go,adaptador_integration_test.go}`
- `internal/dbtest/dbtest.go`, `migrations/grants_integration_test.go`
- `workers_test.go`

## 2026:10:07 16:25

**Resumen:** Se exporta `PresupuestoRevocacion` para la arista restringida a `identidad` (RevocacionPort), por la obligación I3. Brecha detectada al cablear el prompt 09: el adaptador EN PROCESO de `pf-svc-core` para la revocación no tenía de dónde leer el presupuesto, porque el consumidor no lo exportaba.

**Cambios:**
- `ports.go`: se agrega `PresupuestoRevocacion` (`2 * time.Second`) exportado, junto al puerto `RevocacionPort`. `core` lo lee de aquí para el adaptador en proceso, en vez de duplicar el número (I3, ADR 17). El comentario distingue este presupuesto (una llamada puntual) del umbral de frescura del canal (15 s / 30 s, ADR 26).

**Verificado:** gofmt limpio, `go vet ./...`, `go build ./...`, `go test ./...`, build `-mod=readonly`, arch-test@v0.1.1 OK.

**Archivos modificados:**
- `ports.go`

## 2026:10:05 (hora local)

**Resumen:** Scaffold inicial del módulo `reportes` (prompt 06): superficie pública, esquema de proyección alimentado por eventos, puerta única de presupuesto, consumidores idempotentes, arranque standalone y CI. Contrato-primero contra `pf-pkg-contracts v0.1.0-tracer.3`; lo que falta en el contrato se deja sin montar y se reporta.

**Cambios:**
- `go.mod`: `module github.com/AI27-Tech/pf-svc-reportes`; fija `pf-pkg-contracts v0.1.0-tracer.3`, `pf-pkg-platform-go v0.1.0`, `pgx/v5`. Sin requerir ningún `pf-svc-*`.
- `reportes.go`: superficie pública `New`/`Routes`/`Workers`/`Consultas`. `Consultas()` tipada con los tipos generados de `reportes`; `ReporteViajes`/`ReporteEstados` existen (R2) pero devuelven "no implementado" y no se montan rutas por falta de la marca de frescura (R3).
- `ports.go`: `CatalogosPort` (lectura a una fecha; lote bloqueado por R11) con `PresupuestoCatalogos` exportado para paridad I3; `RevocacionPort` restringido (DEC-051).
- `migrations/reportes/0001_init.sql`: esquema `reportes`, rol `svc_reportes` con `USAGE` solo sobre él (ningún GRANT ajeno, ADR 18), `inbox_evento`, `hecho_viaje`, `hecho_transicion`, `hecho_condicion` (solo `INCIDENCIA`).
- `adapters/http/catalogos/`: adaptador HTTP de `CatalogosPort` con el cliente generado y la taxonomía de la plataforma.
- `internal/presupuesto/`: puerta única (tiempo/cancelación, con prueba de timeout → Degradado). `internal/consumidores/`: dedup por `evento_id` + proyección en una unidad atómica, DLQ para evento venenoso, punto de extensión de reconstrucción (R12); con pruebas de no-duplicación y de cuarentena. `internal/revocacion/`: proyección de tombstones detrás de un puerto de difusión. `internal/proyecciones/`: forma de resultado con frescura. `doc.go` con TODO en `entrega` (R9), `dimcatalogo` (R8/R11), `exportacion` (R6) y `reconciliador` (R12).
- `cmd/api/main.go`: arranque standalone (I5) con el pool de `svc_reportes` y el adaptador HTTP de catalogos. `tools.go`, `.github/workflows/ci.yml`, `.gitignore`, `README.md` (con la tabla de brechas R3–R12).

**Verificado:** gofmt limpio, `go vet ./...`, `go build ./...`, `go test ./...`, build de `cmd/api` (I5), build `-mod=readonly`, arch-test OK (solo importa las aristas declaradas a catalogos e identidad).

**Brechas reportadas (no inventadas):** R3 (frescura, bloquea rutas), R4/R5/R6 (cumplimiento/flota/exportaciones), R8 (`catalogo.actualizado` sin versión), R9 (entrega de mensajeria), R11 (lectura por lote), R12 (reconstrucción). En espera de ADR 32: adaptador real del broker y manifiestos de despliegue.

**Archivos modificados:**
- `go.mod`, `go.sum`, `.gitignore`
- `reportes.go`, `ports.go`, `errores.go`, `tools.go`
- `migrations/migrations.go`, `migrations/reportes/0001_init.sql`
- `adapters/http/catalogos/catalogos.go`
- `internal/{presupuesto,consumidores,revocacion,proyecciones,entrega,dimcatalogo,exportacion,reconciliador}/`
- `cmd/api/main.go`
- `.github/workflows/ci.yml`, `README.md`

## 2026:10:06 12:55

**Resumen:** Se agregó la plantilla de Pull Request del backend, adaptada al stack Go de AI27.

**Cambios:**
- Se agregó `.github/pull_request_template.md` con secciones de invariantes de arquitectura (I1-I5, contrato primero), base de datos, eventos/bus, verificación (go build/vet/test, arch-test, gofmt), CHANGELOG y checklist de convenciones.

**Archivos modificados:**
- `.github/pull_request_template.md`

## 2026:10:06 12:48

**Resumen:** Se agregó la regla de steering de CHANGELOG y este archivo, antes de que el repo tenga código, para que la convención esté vigente desde el inicio.

**Cambios:**
- Se agregó `.kiro/steering/changelog.md` (`inclusion: always`): toda modificación del repo debe registrarse aquí, en español, con marca de tiempo, resumen, cambios y archivos modificados.
- Se creó `CHANGELOG.md`.

**Archivos modificados:**
- `.kiro/steering/changelog.md`
- `CHANGELOG.md`
