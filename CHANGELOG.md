# CHANGELOG

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
