# pf-svc-reportes

Módulo **`reportes`** de AI27 (capa de soporte). Responde preguntas analíticas y
agregadas sobre la operación y **no es dueño de ningún hecho** (reportes.md (a)):
mantiene un **modelo de lectura derivado** —proyecciones propias alimentadas por
los eventos versionados de sus productores (ADR 18, cerrado)— bajo su propio
esquema `reportes`, rol `svc_reportes` y compuerta de migración. Si una
proyección discrepa de su origen, **gana el origen** (reportes.md (e), (h) R5).

Vive en el deployable **`core`** (DEC-050). Es **consumidor terminal**: no
publica eventos, no tiene `outbox` ni relay. Arista entrante: solo desde `bff`.

## Superficie pública (`reportes.go`)

`New(Pools, Deps)` · `Routes(mux)` · `Workers()` · `Consultas() ConsultasAPI`
(00 "Forma de un módulo"). Un solo pool/rol: `svc_reportes` (ADR 18 §1).

## Aristas

- **`reportes → catalogos`** (`CatalogosPort`): lectura de dimensiones por lote y
  a una fecha (reportes.md (f)). El adaptador HTTP vive en
  `adapters/http/catalogos/`; el **en proceso** vive en `pf-svc-core`. El
  presupuesto de tiempo se exporta en `PresupuestoCatalogos` para que ambos
  adaptadores usen el mismo valor (I3).
- **`reportes → identidad`** (`RevocacionPort`): **restringida** a revocación
  (`listarRevocadas` + `obtenerSesion`, DEC-051). No es dependencia de dominio
  sino de infraestructura de autorización.
- **No llama a `operacion` ni a `procesamiento`:** esos hechos llegan por evento.
  `arch-test` verifica estas aristas contra `reglas.yaml`.

## Proyecciones en V1

Materializadas en este scaffold (migración `0001_init.sql`):

| Proyección | Alimentada por | Estado |
|---|---|---|
| `hecho_viaje` | `viaje.creado/.estado_cambiado/.asignado/.reasignado/.cerrado` | tabla creada |
| `hecho_transicion` | `viaje.estado_cambiado` | tabla creada |
| `hecho_condicion` | `viaje.condicion_abierta/.condicion_cerrada` (**solo `INCIDENCIA`**) | tabla creada |
| `inbox_evento` | — | tabla creada (dedup por `evento_id`) |

Toda consulta pasa por la **puerta única de presupuesto** (`internal/presupuesto`:
tiempo y memoria por consulta, cancelación, atribución de costo). Todo reporte
filtra por **tenant antes de agregar** (P9) y devuelve su **marca de frescura**
(reportes.md (i)) como parte del resultado.

## Correr local (standalone, I5)

```bash
SERVICE_NAME=reportes HTTP_ADDR=:8080 \
  DB_REPORTES_URL=postgres://svc_reportes@localhost/ai27 \
  CATALOGOS_BASE_URL=http://localhost:8081 \
  go run ./cmd/api

curl localhost:8080/health   # {"status":"ok"}
curl localhost:8080/ready    # {"status":"ok"} | 503
```

Trabajo local multi-repo: un `go.work` **no versionado** en `repos/` (00 regla 7).
Este repo no lo crea ni lo referencia.

## Ramas

`pf-rep-m` (main) / `pf-rep-d` (desarrollo) / `pf-rep-q` (QA), según
`docs/arquitectura/repo-convention.md`. Se trabaja en `pf-rep-d`.

## Brechas de contrato abiertas (contrato primero, 00 regla 6)

Verificado contra `pf-pkg-contracts v0.1.0-tracer.3`. Lo que falta **no se
inventa**: su superficie se deja sin montar y se reporta.

| # | Superficie faltante | Efecto en el scaffold |
|---|---|---|
| R3 | **Marca de frescura** en `ReporteViajes`/`ReporteEstados` | Las rutas `/reportes/viajes` y `/reportes/estados` y sus métodos de `Consultas()` **no se montan** (la frescura no es opcional). Las operaciones existen (R2) pero el resultado contractual no la lleva |
| R4 | `GET /api/v1/reportes/cumplimiento` | Sin ruta ni tipos. Además, su alcance V1 no está definido (ADR 4 diferido a V2): **pregunta a Producto** antes de contratar |
| R5 | `GET /api/v1/reportes/flota` | Sin ruta ni tipos |
| R6 | `POST/GET /api/v1/exportaciones` | Sin ruta ni tabla; exportación masiva declarada fuera de V1 (DEC-023/036). `internal/exportacion/doc.go` con TODO |
| R8 | `catalogo.actualizado` con `reportes` como consumidor **y con versión** | `dim_catalogo` sin tabla ni consumidor. `internal/dimcatalogo/doc.go` con TODO (misma brecha que C10 de catalogos) |
| R9 | Eventos de entrega de `mensajeria` para `reportes` | `hecho_entrega` sin tabla ni consumidor. `internal/entrega/doc.go` con TODO |
| R11 | Lectura de `catalogos` **por lote** | `CatalogosPort` queda con lectura a una fecha (contrato/SLA); `dim_catalogo` bloqueada |
| R12 | Representaciones de **reconstrucción** (ADR 30) | Sin consumo de reconstrucción; punto de extensión con TODO en `internal/consumidores`. El `reconciliador` queda como esqueleto |

Discrepancias reportadas, no resueltas:

- **E6-T2 menciona "estadías"** en `hecho_condicion`: no son un valor de
  `condicion_viaje` en el contrato. Solo se materializa `INCIDENCIA`.
- **Parámetro "alcance"** en `/reportes/viajes` (reportes.md (f)): el contrato
  solo tiene `desde`/`hasta`. No se agregan parámetros; el contrato debe aclararlo.

## En espera de ADR 32

- **Adaptador real del broker V1** (suscripción durable de proyecciones y de
  `sesion.revocada`): en desarrollo se usará `bus/memory` de `pf-pkg-platform-go`;
  no se elige producto aquí (DEC-062).
- **Manifiestos de despliegue** del orquestador on-premise: fuera de alcance de
  este repo.

## Dependencias de reconstrucción (ADR 30)

La fuente canónica de la reconstrucción es el productor: `operacion` (E1-T9) para
viaje, transiciones, asignaciones y condiciones; `catalogos` (E5-T5) para
versiones y vigencias de `dim_catalogo`. Hasta que sus contratos de corrida
existan (R12), `reportes` no implementa consumo de reconstrucción.
