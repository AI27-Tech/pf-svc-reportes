-- 0001_init.sql — esquema, rol y proyecciones iniciales de reportes.
-- Un solo esquema `reportes` y un solo rol `svc_reportes` con USAGE únicamente
-- sobre él (00 regla 4, ADR 18 §1). NINGÚN GRANT hacia esquemas de operacion,
-- catalogos ni procesamiento, ni contra primaria ni contra réplica (ADR 18):
-- op_asignacion sigue siendo la única excepción de GRANT del sistema. Sin FK
-- hacia otro esquema (SPEC §A.5 R1): los identificadores ajenos son solo
-- identificadores. Las proyecciones son permanentes y derivadas (reportes.md
-- (e)): si una discrepa de su origen, gana el origen.

CREATE SCHEMA IF NOT EXISTS reportes;

-- Rol dueño del esquema. LOGIN para que el pool se conecte con él.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_reportes') THEN
    CREATE ROLE svc_reportes LOGIN;
  END IF;
END
$$;

GRANT USAGE ON SCHEMA reportes TO svc_reportes;
ALTER DEFAULT PRIVILEGES IN SCHEMA reportes
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO svc_reportes;

-- ---------------------------------------------------------------------------
-- inbox_evento — deduplicación idempotente del consumidor por evento_id del
-- Sobre (P13, reportes.md (g); refleja inbox.InboxDDL de pf-pkg-platform-go). La
-- inserción aquí y el cambio de proyección se confirman en la MISMA transacción;
-- el acuse al broker ocurre después (al menos una vez). Reaplicar un
-- viaje.estado_cambiado duplicaría una duración: por eso es obligatoria.
-- reportes es consumidor TERMINAL: no hay tabla outbox ni relay (reportes.md (g)).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reportes.inbox_evento (
  evento_id    UUID        PRIMARY KEY,
  tipo         TEXT        NOT NULL,
  consumidor   TEXT        NOT NULL,
  procesado_en TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- hecho_viaje — un renglón por viaje. Marcas de tiempo por estado, cliente,
-- unidad y custodios, alimentado por viaje.creado/.estado_cambiado/.asignado/
-- .reasignado/.cerrado (reportes.md (e), (g)). Las columnas salen de los campos
-- de viaje.proto (viaje_id, cliente_id, unidad_id, custodios_ids, tipo); no se
-- agregan columnas que ningún evento contratado provea.
--
-- Los identificadores ajenos (cliente_id, unidad_id) son identificadores, no FK.
-- custodios_ids se guarda denormalizado como llega en el evento (array de UUID).
-- Los tokens de estado los genera contracts/estados/ (P14); ningún enum copiado.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reportes.hecho_viaje (
  viaje_id       UUID        PRIMARY KEY,
  tenant_id      TEXT        NOT NULL,
  cliente_id     UUID,
  unidad_id      UUID,
  custodios_ids  UUID[]      NOT NULL DEFAULT '{}',
  tipo           TEXT,
  estado_actual  TEXT,
  creado_en      TIMESTAMPTZ,
  cerrado_en     TIMESTAMPTZ,
  -- Frescura de la proyección: hasta qué evento se aplicó este renglón
  -- (reportes.md (i)). No es la frescura de respuesta del contrato (R3), que
  -- falta; es la interna que el caso de uso agrega a su resultado.
  aplicado_hasta TIMESTAMPTZ
);
-- Todo reporte filtra por tenant ANTES de agregar (P9): índice por tenant.
CREATE INDEX IF NOT EXISTS hecho_viaje_tenant_idx
  ON reportes.hecho_viaje (tenant_id);

-- ---------------------------------------------------------------------------
-- hecho_transicion — grano fino de transición, alimentado por
-- viaje.estado_cambiado (reportes.md (e)). Un renglón por transición aplicada.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reportes.hecho_transicion (
  id            BIGSERIAL   PRIMARY KEY,
  viaje_id      UUID        NOT NULL,
  tenant_id     TEXT        NOT NULL,
  de_estado     TEXT,
  a_estado      TEXT        NOT NULL,
  ocurrido_en   TIMESTAMPTZ NOT NULL,
  evento_id     UUID        NOT NULL
);
CREATE INDEX IF NOT EXISTS hecho_transicion_viaje_idx
  ON reportes.hecho_transicion (viaje_id);
CREATE INDEX IF NOT EXISTS hecho_transicion_tenant_idx
  ON reportes.hecho_transicion (tenant_id);

-- ---------------------------------------------------------------------------
-- hecho_condicion — SOLO INCIDENCIA. Alimentado por viaje.condicion_abierta/
-- .condicion_cerrada (reportes.md (e)). Los episodios de SIN_SENAL son V2
-- (viaje.estados.yaml: SIN_SENAL availability: v2): no se crean columnas,
-- valores ni casos de uso para ellos. Las "estadías" que menciona E6-T2 no son
-- un valor de condicion_viaje en el contrato (discrepancia reportada).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reportes.hecho_condicion (
  id            BIGSERIAL   PRIMARY KEY,
  viaje_id      UUID        NOT NULL,
  tenant_id     TEXT        NOT NULL,
  -- condicion es siempre 'INCIDENCIA' en V1 (único valor permitido en el scaffold).
  condicion     TEXT        NOT NULL DEFAULT 'INCIDENCIA'
                 CHECK (condicion = 'INCIDENCIA'),
  abierta_en    TIMESTAMPTZ NOT NULL,
  cerrada_en    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS hecho_condicion_viaje_idx
  ON reportes.hecho_condicion (viaje_id);
CREATE INDEX IF NOT EXISTS hecho_condicion_tenant_idx
  ON reportes.hecho_condicion (tenant_id);

-- ---------------------------------------------------------------------------
-- BLOQUEADO POR BRECHA DE CONTRATO — NO SE CREA EN ESTE SCAFFOLD:
--   * dim_catalogo (dimensiones de catalogo)  -> R8 (catalogo.actualizado no es
--     consumible por reportes y no lleva versión) y R11 (falta lectura por lote)
--   * hecho_entrega (entrega de mensajeria)    -> R9 (eventos de entrega no están
--     en el inventario con reportes como consumidor)
-- DIFERIDO A V2 (ADR 20) — NO SE CREA:
--   * hecho_eta (eta_snapshot de procesamiento): ETA manual único en V1
--   * episodios de SIN_SENAL en hecho_condicion
-- RECONSTRUCCIÓN (ADR 30) — SIN TABLA: sus contratos de corrida no existen (R12);
--   el consumo de reconstrucción es solo un punto de extensión en consumidores/.
-- ---------------------------------------------------------------------------
