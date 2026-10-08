-- 0002_grants.sql — privilegio mínimo por tabla del esquema reportes (E0-T5).
-- Fase de grants, separada de la de estructura (0001), igual que en catalogos y
-- operacion. Patrón de referencia: pf-svc-catalogos/migrations/catalogos/0002_grants.sql.
--
-- Por qué existe: 0001 deja un ALTER DEFAULT PRIVILEGES genérico que otorga
-- SELECT, INSERT, UPDATE y DELETE sobre toda tabla, ANTES de crear las tablas, y
-- ningún privilegio sobre secuencias. Medido sobre Postgres: sin este 0002,
-- svc_reportes podía DELETE en todas sus proyecciones (contradice el modelo de
-- proyección derivada y permanente de reportes.md (e)) y NO podía insertar en
-- hecho_transicion/hecho_condicion por falta de USAGE sobre sus secuencias
-- BIGSERIAL. El invariante I1 (ADR 17) solo es real con privilegio mínimo.
--
-- Criterio (decisión del Lead E0-T5): reportes es un consumidor TERMINAL del
-- flujo vivo. SELECT sobre todo lo suyo; INSERT donde ingiere; UPDATE solo en
-- las proyecciones que mutan al avanzar un viaje (hecho_viaje, hecho_condicion);
-- DELETE en NINGUNA tabla. Ningún GRANT hacia este esquema fuera de svc_reportes
-- y ningún GRANT de svc_reportes sobre otro esquema (SPEC §A.5 R1, ADR 18):
-- op_asignacion sigue siendo la única excepción de GRANT del sistema.
--
-- NOTA — RECONSTRUCCIÓN (ADR 30): la corrida de reconstrucción histórica de
-- reportes (reconstruir proyecciones desde los productores) tendrá privilegios
-- PROPIOS, que se diseñarán con sus contratos de corrida (hoy bloqueados, R12).
-- Esos permisos NO viven aquí: cuando exista la corrida, declarará sus GRANT en
-- su propia fase y se agregará a grants_integration_test.go. Este 0002 cubre el
-- flujo vivo del consumidor, no la reconstrucción.
--
-- La aplica el MISMO rol que aplicó 0001 (svc_reportes). Es idempotente.

-- --- Partir de cero: retirar lo que 0001 otorgó de forma genérica --------------
REVOKE ALL ON ALL TABLES    IN SCHEMA reportes FROM svc_reportes;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA reportes FROM svc_reportes;
-- Una tabla futura (0003+) no hereda privilegios: cada migración que crea una
-- tabla declara sus GRANT explícitos en su propia fase de grants.
ALTER DEFAULT PRIVILEGES IN SCHEMA reportes
  REVOKE ALL ON TABLES FROM svc_reportes;

-- --- inbox_evento: deduplicación del consumidor (P13) --------------------------
-- Alta del evento_id en la misma tx que el efecto. Nunca se actualiza ni borra.
GRANT SELECT, INSERT ON reportes.inbox_evento TO svc_reportes;

-- --- hecho_viaje: proyección por viaje, un renglón que avanza con el viaje -----
-- Se inserta al primer evento del viaje y se ACTUALIZA al aplicar cada evento
-- siguiente (estado_actual, cerrado_en, aplicado_hasta). No se borra: la
-- proyección es permanente y derivada (reportes.md (e)).
GRANT SELECT, INSERT, UPDATE ON reportes.hecho_viaje TO svc_reportes;

-- --- hecho_transicion: grano fino, un renglón por transición aplicada ---------
-- Append-only: cada viaje.estado_cambiado agrega un renglón; no se edita ni borra.
GRANT SELECT, INSERT ON reportes.hecho_transicion TO svc_reportes;
GRANT USAGE ON SEQUENCE reportes.hecho_transicion_id_seq TO svc_reportes;

-- --- hecho_condicion: episodios de INCIDENCIA (V1) -----------------------------
-- Se inserta al abrir (viaje.condicion_abierta) y se ACTUALIZA al cerrar
-- (viaje.condicion_cerrada fija cerrada_en). No se borra.
GRANT SELECT, INSERT, UPDATE ON reportes.hecho_condicion TO svc_reportes;
GRANT USAGE ON SEQUENCE reportes.hecho_condicion_id_seq TO svc_reportes;
